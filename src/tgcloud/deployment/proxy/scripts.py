"""Shell probe, status, lock, and transaction script builders for managed HTTPS."""
from __future__ import annotations

import re
import shlex
import signal
import sys
import threading
from contextlib import contextmanager

from tgcloud.deployment.profiles import ProductProfile

from .certificates import _CERTIFICATE_CHECK_SCRIPT, PROXY_ROOT
from .rendering import (
    NGINX_IMAGE,
    PROXY_CERTBOT_CONTAINER,
    PROXY_NGINX_CONTAINER,
    certbot_bindings,
)
from .state import parse_markers, validate_domain

HOST_TOOL_PACKAGES = {
    "ss": "iproute2", "ip": "iproute2", "curl": "curl",
    "getent": "libc-bin", "python3": "python3", "timeout": "coreutils",
    "flock": "util-linux",
}

_ANSI_ESCAPE_RE = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")


@contextmanager
def defer_interruptions():
    """Finish bounded commit/rollback commands before honoring CLI cancellation.

    Background workers never install signal handlers. SIGKILL and lost connections are
    not recoverable here; ordinary INT/TERM/HUP must not split a state commit.
    """
    previous = {}
    pending = False

    def defer(_signum, _frame):
        nonlocal pending
        pending = True

    try:
        if threading.current_thread() is threading.main_thread():
            for name in ("SIGINT", "SIGTERM", "SIGHUP"):
                number = getattr(signal, name, None)
                if number is not None and signal.getsignal(number) != signal.SIG_IGN:
                    previous[number] = signal.signal(number, defer)
        yield
    finally:
        for number, handler in previous.items():
            signal.signal(number, handler)
        if pending and sys.exc_info()[0] is None:
            raise KeyboardInterrupt


def host_tools_check_script(tools: tuple[str, ...]) -> str:
    if not tools or any(tool not in HOST_TOOL_PACKAGES for tool in tools):
        raise ValueError("未知宿主依赖。")
    return f"""
missing_tools=""
for tool in {shlex.join(tools)}; do
  if ! command -v "$tool" >/dev/null 2>&1; then missing_tools="$missing_tools $tool"; fi
done
if [[ -n "$missing_tools" ]]; then
  printf 'TGCLOUD_PROXY_MISSING_TOOLS=%s\\n' "${{missing_tools# }}"
  exit 3
fi
"""


def raise_missing_host_tools(output: str) -> None:
    missing = parse_markers(output).get("PROXY_MISSING_TOOLS", "").split()
    if missing:
        tools = [tool for tool in missing if tool in HOST_TOOL_PACKAGES]
        packages = sorted({HOST_TOOL_PACKAGES[tool] for tool in tools})
        raise RuntimeError(
            "VPS 缺少宿主依赖：" + ", ".join(tools or ["未知工具"])
            + "。尚未执行 HTTPS 变更；这不是 DNS 或端口冲突。"
            + ("请以 root 手动执行 apt-get update && apt-get install -y "
               + " ".join(packages) + "，然后重新检测。" if packages else "请核对系统依赖。")
        )


def command_failure_summary(
    output: str,
    *,
    secrets: tuple[str, ...] = (),
    max_lines: int = 16,
    max_chars: int = 1600,
) -> str:
    """Return bounded, single-purpose diagnostics safe for the visible log."""
    clean = _ANSI_ESCAPE_RE.sub("", output).replace("\r", "")
    for secret in secrets:
        if secret:
            clean = clean.replace(secret, "[已脱敏]")
    lines = [line.strip() for line in clean.splitlines() if line.strip()]
    summary = "\n".join(lines[-max_lines:])
    if len(summary) > max_chars:
        summary = "…" + summary[-max_chars:]
    return summary


def environment_probe_command(product: ProductProfile, domain: str) -> str:
    domain = validate_domain(domain)
    return "bash -c " + shlex.quote(
        f"""
set -u
{host_tools_check_script(tuple(HOST_TOOL_PACKAGES))}
if docker inspect {shlex.quote(product.storage_container)} >/dev/null 2>&1 \
  && curl -fsS --max-time 5 http://127.0.0.1:{product.management_port}/ >/dev/null 2>&1; then
  printf 'TGCLOUD_PROXY_CORE=OK\\n'
else
  printf 'TGCLOUD_PROXY_CORE=MISSING\\n'
fi
if docker compose version >/dev/null 2>&1; then
  printf 'TGCLOUD_PROXY_DOCKER=OK\\n'
else
  printf 'TGCLOUD_PROXY_DOCKER=MISSING\\n'
fi
managed=false
managed_pids=""
if [[ "$(docker inspect --format '{{{{index .Config.Labels \"com.tgcloud.managed\"}}}}' {PROXY_NGINX_CONTAINER} 2>/dev/null || true)" == true \
  && "$(docker inspect --format '{{{{index .Config.Labels \"com.tgcloud.role\"}}}}' {PROXY_NGINX_CONTAINER} 2>/dev/null || true)" == reverse-proxy \
  && "$(docker inspect --format '{{{{.HostConfig.NetworkMode}}}}' {PROXY_NGINX_CONTAINER} 2>/dev/null || true)" == host \
  && "$(docker inspect --format '{{{{.State.Running}}}}' {PROXY_NGINX_CONTAINER} 2>/dev/null || true)" == true ]]; then
  managed=true
  managed_pids=" $(docker top {PROXY_NGINX_CONTAINER} -eo pid 2>/dev/null | awk 'NR > 1 {{print $1}}' | paste -sd' ' -) "
fi
for port in 80 443; do
  if ! command -v ss >/dev/null 2>&1; then owner=UNKNOWN
  else
    listeners="$(ss -H -ltnp "( sport = :$port )" 2>/dev/null || true)"
    if [[ -z "$listeners" ]]; then owner=FREE
    elif [[ "$managed" == true ]]; then
      listener_pids="$(printf '%s\n' "$listeners" | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u | paste -sd' ' -)"
      if [[ -z "$listener_pids" || -z "${{managed_pids// /}}" ]]; then owner=UNKNOWN
      else
        owner=OWN
        for pid in $listener_pids; do
          case "$managed_pids" in *" $pid "*) ;; *) owner=FOREIGN; break ;; esac
        done
      fi
    else owner=FOREIGN
    fi
  fi
  printf 'TGCLOUD_PROXY_PORT_%s=%s\\n' "$port" "$owner"
done
dns_a="$(getent ahostsv4 {shlex.quote(domain)} 2>/dev/null | awk '{{print $1}}' | sort -u | paste -sd, -)"
dns_aaaa="$(getent ahostsv6 {shlex.quote(domain)} 2>/dev/null | awk '{{print $1}}' | grep : | sort -u | paste -sd, -)"
local_ips="$(ip -o addr show scope global 2>/dev/null | awk '{{print $4}}' | cut -d/ -f1 | sort -u | paste -sd, -)"
printf 'TGCLOUD_PROXY_DNS_A=%s\\nTGCLOUD_PROXY_DNS_AAAA=%s\\nTGCLOUD_PROXY_LOCAL_IPS=%s\\n' "$dns_a" "$dns_aaaa" "$local_ips"
"""
    )


def _certbot_mount_check_command() -> str:
    # This standard-library reader never prints Docker input or private paths.
    # Use the same contract as Compose and strict resource ownership checks.
    program = f"""import json, sys
try:
    actual = {{}}
    for mount in json.load(sys.stdin):
        if mount.get('Type') == 'tmpfs':
            continue
        destination = mount.get('Destination')
        if mount.get('Type') != 'bind' or destination in actual:
            sys.exit(1)
        actual[destination] = mount.get('Source')
    sys.exit(0 if actual == {certbot_bindings(PROXY_ROOT)!r} else 1)
except Exception:
    sys.exit(1)
"""
    return ("docker container inspect --format " + shlex.quote("{{json .Mounts}}")
            + " " + PROXY_CERTBOT_CONTAINER + " | python3 -c " + shlex.quote(program))


def status_command(product: ProductProfile, domain: str) -> str:
    domain = validate_domain(domain)
    certificate_path = f"/etc/letsencrypt/live/{domain}/fullchain.pem"
    certificate_check = (
        f"docker exec {PROXY_CERTBOT_CONTAINER} python -c "
        f"{shlex.quote(_CERTIFICATE_CHECK_SCRIPT)} "
        f"{shlex.quote(certificate_path)} {shlex.quote(domain)}"
    )
    return "bash -c " + shlex.quote(
        f"""
set +u
root={shlex.quote(PROXY_ROOT)}
core=FAILED
ports=FAILED
nginx=FAILED
certbot=FAILED
renewal=FAILED
syntax=FAILED
https_state=FAILED
redirect=FAILED
dav_state=FAILED
backend=FAILED
cert=FAILED
cert_end=""
if docker inspect {shlex.quote(product.storage_container)} >/dev/null 2>&1 \
  && curl -fsS --max-time 5 http://127.0.0.1:{product.management_port}/ >/dev/null 2>&1; then core=OK; else core=FAILED; fi
if [[ "$(docker inspect --format '{{{{.State.Running}}}}' {PROXY_NGINX_CONTAINER} 2>/dev/null || true)" == true ]]; then nginx=OK; else nginx=FAILED; fi
if [[ "$(docker inspect --format '{{{{.State.Running}}}}' {PROXY_CERTBOT_CONTAINER} 2>/dev/null || true)" == true ]] \
  && ({_certbot_mount_check_command()}) >/dev/null 2>&1; then certbot=OK; else certbot=FAILED; fi
if [[ -s "$root/certbot/letsencrypt/renewal/{domain}.conf" ]]; then renewal=OK; else renewal=FAILED; fi
managed_pids=" $(docker top {PROXY_NGINX_CONTAINER} -eo pid 2>/dev/null | awk 'NR > 1 {{print $1}}' | paste -sd' ' -) "
owns_port() {{
  local listeners listener_pids pid
  listeners="$(ss -H -ltnp "( sport = :$1 )" 2>/dev/null || true)"
  listener_pids="$(printf '%s\n' "$listeners" | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u | paste -sd' ' -)"
  [[ -n "$listeners" && -n "$listener_pids" && -n "${{managed_pids// /}}" ]] || return 1
  for pid in $listener_pids; do
    case "$managed_pids" in *" $pid "*) ;; *) return 1 ;; esac
  done
}}
if [[ "$nginx" == OK \
  && "$(docker inspect --format '{{{{index .Config.Labels \"com.tgcloud.managed\"}}}}' {PROXY_NGINX_CONTAINER} 2>/dev/null || true)" == true \
  && "$(docker inspect --format '{{{{index .Config.Labels \"com.tgcloud.role\"}}}}' {PROXY_NGINX_CONTAINER} 2>/dev/null || true)" == reverse-proxy \
  && "$(docker inspect --format '{{{{.HostConfig.NetworkMode}}}}' {PROXY_NGINX_CONTAINER} 2>/dev/null || true)" == host ]] \
  && owns_port 80 && owns_port 443; then ports=OK; else ports=FAILED; fi
if docker compose -f "$root/docker-compose.yml" exec -T nginx nginx -t >/dev/null 2>&1; then syntax=OK; else syntax=FAILED; fi
https="$(curl --noproxy '*' --resolve {domain}:443:127.0.0.1 -sS -o /dev/null -w '%{{http_code}}' --max-time 15 https://{domain}/ 2>/dev/null || true)"
http_headers="$(curl --noproxy '*' --resolve {domain}:80:127.0.0.1 -sS -o /dev/null -D - --max-time 15 http://{domain}/ 2>/dev/null || true)"
http_code="$(printf '%s' "$http_headers" | awk 'NR==1 {{print $2}}')"
location="$(printf '%s' "$http_headers" | awk 'BEGIN{{IGNORECASE=1}} /^Location:/ {{gsub(/\\r/,\"\",$2); print $2; exit}}')"
dav="$(curl --noproxy '*' --resolve {domain}:443:127.0.0.1 -sS -o /dev/null -w '%{{http_code}}' --max-time 15 https://{domain}/dav/ 2>/dev/null || true)"
binding="$(ss -H -ltn '( sport = :{product.management_port} )' 2>/dev/null || true)"
if [[ -n "$binding" ]] && printf '%s\n' "$binding" | awk '$4 !~ /^127[.]0[.]0[.]1:/ && $4 !~ /^\\[::1\\]:/ {{bad=1}} END {{exit bad}}'; then backend=OK; else backend=FAILED; fi
if cert_end="$({certificate_check} 2>/dev/null)"; then cert=OK; else cert=FAILED; fi
[[ "$https" =~ ^[123][0-9][0-9]$ ]] && https_state=OK || https_state=FAILED
[[ "$http_code" =~ ^30[18]$ && "$location" == https://{domain}/* ]] && redirect=OK || redirect=FAILED
[[ "$dav" == 403 ]] && dav_state=OK || dav_state=FAILED
printf 'TGCLOUD_PROXY_CORE=%s\\nTGCLOUD_PROXY_PORTS=%s\\n' "$core" "$ports"
printf 'TGCLOUD_PROXY_NGINX=%s\\nTGCLOUD_PROXY_CERTBOT=%s\\nTGCLOUD_PROXY_RENEWAL=%s\\nTGCLOUD_PROXY_NGINX_TEST=%s\\n' "$nginx" "$certbot" "$renewal" "$syntax"
printf 'TGCLOUD_PROXY_HTTPS=%s\\nTGCLOUD_PROXY_REDIRECT=%s\\nTGCLOUD_PROXY_DAV_BLOCK=%s\\n' "$https_state" "$redirect" "$dav_state"
printf 'TGCLOUD_PROXY_BACKEND_LOCAL=%s\\nTGCLOUD_PROXY_CERT=%s\\nTGCLOUD_PROXY_CERT_END=%s\\n' "$backend" "$cert" "$cert_end"
for field in ATTEMPT RESULT NEXT; do
  value="$(awk -F= -v key="$field" '$1 == key {{print $2; exit}}' "$root/certbot/status/{domain}.check" 2>/dev/null || true)"
  if [[ "$value" =~ ^[0-9]{{1,12}}$ || "$value" == OK || "$value" == FAILED ]]; then
    printf 'TGCLOUD_PROXY_RENEW_%s=%s\\n' "$field" "$value"
  fi
done
for field in success renewed; do
  value="$(cat "$root/certbot/status/{domain}.$field" 2>/dev/null || true)"
  if [[ "$value" =~ ^[0-9]{{1,12}}$ ]]; then
    printf 'TGCLOUD_PROXY_RENEW_%s=%s\\n' "${{field^^}}" "$value"
  fi
done
printf 'TGCLOUD_PROXY_STATUS=OK\\n'
"""
    )


def verification_statuses(markers: dict[str, str]) -> tuple[tuple[str, str], ...]:
    mapping = (
        ("基础服务", "PROXY_CORE"),
        ("80/443 由共享代理持有", "PROXY_PORTS"),
        ("Nginx 容器", "PROXY_NGINX"),
        ("Certbot 容器", "PROXY_CERTBOT"),
        ("当前域名续期配置", "PROXY_RENEWAL"),
        ("Nginx 配置", "PROXY_NGINX_TEST"),
        ("HTTPS 管理页", "PROXY_HTTPS"),
        ("HTTP 跳转", "PROXY_REDIRECT"),
        ("公网 /dav 阻断", "PROXY_DAV_BLOCK"),
        ("后端仅回环监听", "PROXY_BACKEND_LOCAL"),
        ("证书域名与有效期", "PROXY_CERT"),
    )
    return tuple((title, "通过" if markers.get(key) == "OK" else "失败") for title, key in mapping)


def install_config_command(remote: str, *, external_auth: bool = False) -> str:
    """Render the privileged, syntax-checkable config installation transaction."""
    # The remote path is random, privately created with mode 0700, and never user supplied.
    if not re.fullmatch(
        r"/tmp/tgcloud-proxy-[0-9a-f]{32}", remote  # nosec B108
    ):
        raise ValueError("共享代理临时目录格式不正确。")
    auth_mount = '-v "$root/external-auth:/etc/nginx/external-auth:ro"' if external_auth else ""
    script = f"""
set -e
root={shlex.quote(PROXY_ROOT)}
install -d -m 700 "$root" "$root/state" "$root/nginx" "$root/nginx/conf.d" "$root/certbot" "$root/certbot/letsencrypt" "$root/certbot/lib" "$root/certbot/log" "$root/certbot/status"
# Nginx workers run without root. Only the public ACME challenge webroot is
# traversable; account data, private keys and Certbot state remain mode 0700.
install -d -m 755 "$root/certbot/www"
install -d -m 700 "$root/nginx/conf.d.next"
find "$root/nginx/conf.d.next" -maxdepth 1 -type f -delete
install -m 644 {shlex.quote(remote)}/nginx.conf "$root/nginx/nginx.conf.next"
install -m 600 {shlex.quote(remote)}/docker-compose.yml "$root/docker-compose.yml.next"
for source in {shlex.quote(remote)}/conf.d/*.conf; do
  install -m 644 "$source" "$root/nginx/conf.d.next/$(basename "$source")"
done
docker run --rm --network host --read-only \
  --tmpfs /var/cache/nginx --tmpfs /var/run --tmpfs /tmp \
  -v "$root/nginx/nginx.conf.next:/etc/nginx/nginx.conf:ro" \
  -v "$root/nginx/conf.d.next:/etc/nginx/conf.d:ro" \
  -v "$root/certbot/www:/var/www/certbot:ro" \
  -v "$root/certbot/letsencrypt:/etc/letsencrypt:ro" \
  {auth_mount} \
  {NGINX_IMAGE} nginx -t
rm -rf "$root/nginx/conf.d.previous"
install -d -m 700 "$root/nginx/conf.d.previous"
cp -a "$root/nginx/conf.d/." "$root/nginx/conf.d.previous/" 2>/dev/null || true
find "$root/nginx/conf.d" -maxdepth 1 -type f -name '*.conf' -delete
cp -a "$root/nginx/conf.d.next/." "$root/nginx/conf.d/"
rm -rf "$root/nginx/conf.d.next"
mv "$root/nginx/nginx.conf.next" "$root/nginx/nginx.conf"
mv "$root/docker-compose.yml.next" "$root/docker-compose.yml"
"""
    return "bash -c " + shlex.quote(script)


def proxy_lock_command() -> str:
    """Hold one VPS-wide flock until the owning SSH channel is released."""
    return "bash -c " + shlex.quote(f"""
set -eu
umask 077
command -v flock >/dev/null 2>&1 || {{ printf 'TGCLOUD_REMOTE_LOCK=UNAVAILABLE\\n'; exit 69; }}
install -d -m 700 {PROXY_ROOT}
exec 9>{PROXY_ROOT}/transaction.lock
flock -n 9 || {{ printf 'TGCLOUD_REMOTE_LOCK=BUSY\\n'; exit 75; }}
trap 'exit 0' HUP INT TERM
printf 'TGCLOUD_REMOTE_LOCK=ACQUIRED\\n'
# sudo can leave an unused password line on stdin when credentials are cached.
# Ignore every line except the explicit release marker; never echo input.
while IFS= read -r line; do
  [[ "$line" != TGCLOUD_REMOTE_LOCK_RELEASE ]] || break
done
""")
