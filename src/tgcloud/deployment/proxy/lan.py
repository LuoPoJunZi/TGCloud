"""LAN gateway configuration, ownership and local probes."""

from __future__ import annotations

import ipaddress
import json
import os
import re
import shlex
import shutil
import socket
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

from tgcloud.deployment import configuration, instances
from tgcloud.deployment.host import LocalSession, SafeLog
from tgcloud.deployment.profiles import ProductProfile
from tgcloud.deployment.proxy.rendering import NGINX_IMAGE

LAN_ROOT = Path("/opt/tgcloud-lan")
HTTPS_STATE = Path("/opt/tgcloud-proxy/state/domains.json")
RFC1918 = tuple(ipaddress.IPv4Network(value) for value in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"))
FILES = {"state.json", "compose.json", "nginx.conf"}
# This is an isolated container tmpfs, not a predictable host temporary file.
CONTAINER_TMP = "/tmp"  # nosec B108
TMPFS_FLAGS = "rw,noexec,nosuid,mode=1777,size=64m"


def validate_ip(value: str) -> str:
    if not isinstance(value, str):
        raise ValueError("内网地址必须是标准 IPv4 文本")  # noqa: TRY004 - invalid external configuration
    try:
        address = ipaddress.IPv4Address(value)
    except ipaddress.AddressValueError as exc:
        raise ValueError("请输入标准内网 IPv4 地址，不接受域名、URL 或端口") from exc
    if str(address) != value or not any(address in network for network in RFC1918):
        raise ValueError("只接受 RFC1918 内网 IPv4，不接受公网、回环或通配地址")
    return value


def validate_local_ip(value: str, session: LocalSession) -> str:
    value = validate_ip(value)
    code, output = session.run("ip -j -4 address show", timeout=15)
    if code:
        raise ValueError("无法读取本机网卡；需要 iproute2，不会猜测内网地址")
    for interface in json.loads(output):
        name = interface.get("ifname", "")
        if name == "lo" or name.startswith(("docker", "br-", "veth")) or "UP" not in interface.get("flags", []):
            continue
        for item in interface.get("addr_info", []):
            if item.get("family") != "inet" or item.get("local") != value:
                continue
            network = ipaddress.IPv4Network(f"{value}/{item['prefixlen']}", strict=False)
            address = ipaddress.IPv4Address(value)
            if network.prefixlen < 31 and address in {network.network_address, network.broadcast_address}:
                continue
            return value
    raise ValueError("该地址不属于本机已启用的内网网卡；虚拟机请填虚拟机地址，不填 NAS 宿主机地址")


def private_directory(path: Path) -> None:
    if path.is_symlink() or path.resolve() != path or not path.is_dir():
        raise ValueError("内网状态目录缺失或经过链接")
    info = path.stat()
    if os.name == "posix" and (info.st_uid != 0 or info.st_mode & 0o077):
        raise ValueError("内网状态目录必须归 root 所有且权限为 700")


def container_name(product: ProductProfile) -> str:
    # The stable CD2 discovery helper matches any name containing clouddrive2.
    # Do not let this Nginx be mistaken for a second CloudDrive2 gateway.
    return "tgcloud-lan-cd2"


def state_for(product: ProductProfile, address: str) -> dict:
    return {"schema": 1, "mode": "lan", "edition": product.key, "bind_ip": validate_ip(address),
            "install_dir": product.install_dir, "proxy_image": NGINX_IMAGE}


def render_files(product: ProductProfile, address: str) -> dict[str, str]:
    state = state_for(product, address)
    port = product.management_port
    directory = LAN_ROOT / product.key
    compose = {"services": {"lan": {
        "image": NGINX_IMAGE, "container_name": container_name(product), "network_mode": "host",
        "user": "101:101", "entrypoint": ["nginx"], "command": ["-g", "daemon off;"],
        "restart": "unless-stopped", "read_only": True, "cap_drop": ["ALL"],
        "security_opt": ["no-new-privileges:true"], "tmpfs": [CONTAINER_TMP + ":" + TMPFS_FLAGS],
        "labels": {"com.tgcloud.managed": "true", "com.tgcloud.role": "lan-management",
                   "com.tgcloud.edition": product.key},
        "volumes": [{"type": "bind", "source": str(directory / "nginx.conf"),
                     "target": "/etc/nginx/nginx.conf", "read_only": True}],
    }}}
    nginx = f"""worker_processes auto;
pid /tmp/nginx.pid;
error_log /dev/stderr warn;
events {{ worker_connections 1024; }}
http {{
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    access_log off;
    server_tokens off;
    client_max_body_size 16m;
    client_body_temp_path /tmp/client_body;
    proxy_temp_path /tmp/proxy;
    fastcgi_temp_path /tmp/fastcgi;
    uwsgi_temp_path /tmp/uwsgi;
    scgi_temp_path /tmp/scgi;
    map $http_upgrade $connection_upgrade {{ default upgrade; '' close; }}
    server {{
        listen {address}:{port};
        server_name {address};
        add_header X-TGCloud-LAN {product.key} always;
        if ($host != '{address}') {{ return 403; }}
        location ~* ^/dav(?:/|$) {{ return 403; }}
        location / {{
            proxy_pass http://127.0.0.1:{port};
            proxy_http_version 1.1;
            proxy_set_header Host $http_host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $remote_addr;
            proxy_set_header X-Forwarded-Proto http;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection $connection_upgrade;
            proxy_read_timeout 300s;
        }}
    }}
}}
"""
    return {"state.json": json.dumps(state, sort_keys=True) + "\n",
            "compose.json": json.dumps(compose, indent=2) + "\n", "nginx.conf": nginx}


def load_state(product: ProductProfile) -> dict | None:
    directory = LAN_ROOT / product.key
    if not LAN_ROOT.exists() and not LAN_ROOT.is_symlink():
        return None
    private_directory(LAN_ROOT)
    if not directory.exists() and not directory.is_symlink():
        return None
    private_directory(directory)
    if {path.name for path in directory.iterdir()} != FILES:
        raise ValueError("内网入口文件不完整或含额外文件；停止，不自动清理")
    state = json.loads(configuration.regular_private(directory / "state.json"))
    if not isinstance(state, dict) or state != state_for(product, state.get("bind_ip", "")) or type(state.get("schema")) is not int:
        raise ValueError("内网模式记录无效；不接管其他实例或改变安装模式")
    for name, expected in render_files(product, state["bind_ip"]).items():
        path = directory / name
        if path.is_symlink() or path.resolve() != path or not path.is_file():
            raise ValueError("内网入口文件经过链接或缺失")
        info = path.stat()
        if info.st_size > 128 * 1024 or (os.name == "posix" and (info.st_uid != 0 or info.st_mode & 0o022)):
            raise ValueError("内网入口文件权限/大小不安全")
        if path.read_text(encoding="utf-8") != expected:
            raise ValueError("内网入口配置被修改；停止，不覆盖手改配置")
    return state


def save_state(product: ProductProfile, address: str) -> None:
    if LAN_ROOT.exists() or LAN_ROOT.is_symlink():
        private_directory(LAN_ROOT)
    else:
        LAN_ROOT.mkdir(mode=0o700)
        private_directory(LAN_ROOT)
    directory = LAN_ROOT / product.key
    if directory.exists() or directory.is_symlink():
        existing = load_state(product)
        if existing != state_for(product, address):
            raise ValueError("不隐式切换内网地址")
        return
    staged = Path(tempfile.mkdtemp(prefix=".stage-" + product.key + "-", dir=LAN_ROOT))
    try:
        for name, text in render_files(product, address).items():
            path = staged / name
            with path.open("x", encoding="utf-8", newline="\n") as writer:
                path.chmod(0o644 if name == "nginx.conf" else 0o600)
                writer.write(text)
                writer.flush()
                os.fsync(writer.fileno())
        os.rename(staged, directory)
    finally:
        if staged.exists():
            # Only our mkdtemp staging tree, never a user installation.
            shutil.rmtree(staged)


def reject_https_route(product: ProductProfile) -> None:
    path = HTTPS_STATE
    if not path.exists() and not path.is_symlink():
        return
    state = json.loads(configuration.regular_private(path))
    if not isinstance(state, dict) or not isinstance(state.get("domains"), dict):
        raise ValueError("既有 HTTPS 状态无法核对，不改变访问模式")  # noqa: TRY004 - invalid external state
    if product.key in state["domains"]:
        raise ValueError("该实例已配置 HTTPS；内网脚本不转换、不移除已有域名路由")


def inspect_lan(product: ProductProfile, session: LocalSession, inventory: set[str]) -> dict | None:
    name = container_name(product)
    if name not in inventory:
        return None
    template = ('{"image":{{json .Config.Image}},"running":{{json .State.Running}},'
                '"labels":{{json .Config.Labels}},"mounts":{{json .Mounts}},'
                '"network":{{json .HostConfig.NetworkMode}},"readonly":{{json .HostConfig.ReadonlyRootfs}},'
                '"privileged":{{json .HostConfig.Privileged}},"user":{{json .Config.User}},'
                '"cap_add":{{json .HostConfig.CapAdd}},"cap_drop":{{json .HostConfig.CapDrop}},'
                '"security":{{json .HostConfig.SecurityOpt}},"tmpfs":{{json .HostConfig.Tmpfs}},'
                '"entrypoint":{{json .Config.Entrypoint}},"command":{{json .Config.Cmd}}}')
    code, output = session.run("docker container inspect --format " + shlex.quote(template) + " " + name, timeout=15)
    if code:
        raise ValueError("无法核对内网入口容器，不接管同名容器")
    item = json.loads(output)
    labels = item.get("labels") or {}
    required = {"com.tgcloud.managed": "true", "com.tgcloud.role": "lan-management",
                "com.tgcloud.edition": product.key, "com.docker.compose.project": name,
                "com.docker.compose.service": "lan", "com.docker.compose.project.working_dir": str(LAN_ROOT / product.key),
                "com.docker.compose.project.config_files": str(LAN_ROOT / product.key / "compose.json")}
    mount = [{"Type": value.get("Type"), "Source": value.get("Source"),
              "Destination": value.get("Destination"), "RW": value.get("RW")}
             for value in item.get("mounts", []) if value.get("Destination") != CONTAINER_TMP]
    expected_mount = [{"Type": "bind", "Source": str(LAN_ROOT / product.key / "nginx.conf"),
                       "Destination": "/etc/nginx/nginx.conf", "RW": False}]
    if (any(labels.get(key) != value for key, value in required.items())
            or item.get("image") != NGINX_IMAGE or item.get("network") != "host"
            or item.get("readonly") is not True or item.get("privileged") is not False
            or item.get("cap_add") or item.get("cap_drop") != ["ALL"]
            or item.get("security") not in (["no-new-privileges:true"], ["no-new-privileges"])
            or item.get("tmpfs") != {CONTAINER_TMP: TMPFS_FLAGS}
            or item.get("user") != "101:101" or item.get("entrypoint") != ["nginx"]
            or item.get("command") != ["-g", "daemon off;"] or mount != expected_mount):
        raise ValueError("同名内网入口归属或运行配置不符，停止，不接管")
    return item


def check_port(product: ProductProfile, address: str, running: bool) -> None:
    if running:
        return  # Owned, canonical container is validated separately.
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        try:
            listener.bind((address, product.management_port))
        except OSError as exc:
            raise ValueError("指定内网地址/固定管理端口不可绑定；请核对占用，不会停止外部服务或更换端口") from exc


def check_published_ports(product: ProductProfile, session: LocalSession, inventory: set[str]) -> None:
    if not inventory:
        return
    # NAT-only publications may not appear as listening sockets. Match the
    # original installer's container port ownership gate before secret input.
    code, output = session.run("docker ps --format '{{.Names}}|{{.Ports}}'", timeout=15)
    if code:
        raise ValueError("无法核对 Docker 固定端口占用，不跳过冲突检查")
    pattern = re.compile(r":" + str(product.management_port) + r"->")
    for line in output.splitlines():
        name, _, ports = line.partition("|")
        if pattern.search(ports) and name != product.storage_container:
            raise ValueError("固定管理端口已由其他容器发布；请人工核对，不自动停用或换端口")


def check_backend(instance: instances.Instance, session: LocalSession, log: SafeLog) -> None:
    code, output = session.run("docker inspect --format '{{json .HostConfig.PortBindings}}' " + instance.product.storage_container, timeout=15)
    if code:
        raise ValueError("无法核对网关回环监听")
    bindings = {key: value for key, value in (json.loads(output) or {}).items() if value}
    port = str(instance.product.management_port)
    if bindings != {port + "/tcp": [{"HostIp": "127.0.0.1", "HostPort": port}]}:
        raise ValueError("网关不再仅发布预期回环端口；停止，不把异常实例降级为内网安装")
    directory = shlex.quote(str(instance.directory))
    code, output = session.run("cd " + directory + " && INSTALL_DIR=" + directory + " bash ./manage.sh ready", stream=log, timeout=90)
    if code:
        raise ValueError("Bot/网关基础检查未通过；不冒充部署成功，不自动重装")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        return None


def request_status(product: ProductProfile, address: str, path: str, method: str = "GET") -> tuple[int, str]:
    url = f"http://{validate_ip(address)}:{product.management_port}{path}"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    request = urllib.request.Request(url, method=method)
    try:
        # URL scheme and literal RFC1918 address are validated above.
        response = opener.open(request, timeout=5)
    except urllib.error.HTTPError as exc:
        response = exc
    with response:
        return response.code, response.headers.get("X-TGCloud-LAN", "")


def check_entry(product: ProductProfile, address: str) -> None:
    for path, method in (("/", "GET"), ("/dav", "GET"), ("/dav/", "PROPFIND"), ("/DAV/", "PROPFIND")):
        code, marker = request_status(product, address, path, method)
        expected = 200 <= code < 400 if path == "/" else code == 403
        if marker != product.key or not expected:
            raise ValueError("内网管理页或 /dav 阻断未通过；不冒充部署成功")


def wait_for_entry(product: ProductProfile, address: str) -> None:
    # Nginx can still be starting just after compose up; bounded local probes
    # only, never retrying WebDAV writes or rebuilding the Bot.
    deadline = time.monotonic() + 20
    while True:
        try:
            check_entry(product, address)
            return
        except (ValueError, OSError):
            if time.monotonic() >= deadline:
                raise RuntimeError("内网入口启动后的管理页或 /dav 阻断检查未通过；数据保留，请核对日志") from None
            time.sleep(0.5)


def compose_command(product: ProductProfile, arguments: str) -> str:
    directory = shlex.quote(str(LAN_ROOT / product.key))
    return "cd " + directory + " && docker compose -p " + container_name(product) + " -f compose.json " + arguments


if __name__ == "__main__":
    import signal

    from tgcloud.deployment.lifecycle.lan import main

    def interrupted(_signal, _frame):
        raise KeyboardInterrupt

    for _name in ("SIGTERM", "SIGHUP"):
        if hasattr(signal, _name):
            signal.signal(getattr(signal, _name), interrupted)
    raise SystemExit(main())
