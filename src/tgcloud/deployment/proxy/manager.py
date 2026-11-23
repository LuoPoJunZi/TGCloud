"""Shared, transactional HTTPS management for both TGCloud editions.

Rendering and parsing helpers are deterministic. Offline Linux CLI regressions
exercise safety decisions without contacting Docker, DNS, Nginx, Certbot or
Let's Encrypt; the host execution boundary is injected.
"""

from __future__ import annotations

from .certificates import (
    _CERTIFICATE_CHECK_SCRIPT,
    PROXY_ROOT,
    dry_run_cleanup_script,
    parse_certificate_enddate,
    renewal_dry_run_command,
    renewal_summary,
    renewal_warnings,
)
from .state import (
    _EMAIL_RE,
    _FQDN_RE,
    _MARKER_RE,
    EDITION_BACKEND_PORTS,
    STATE_VERSION,
    SUPPORTED_EDITIONS,
    DomainRoute,
    ProxyEnvironment,
    ProxyReport,
    _marker_addresses,
    certificate_domains,
    dns_diagnostic,
    dns_matches_environment,
    empty_state,
    has_routes,
    nat_public_ipv4_candidate,
    normalize_state,
    parse_environment,
    parse_markers,
    public_endpoint_error,
    routes_from_state,
    state_with_route,
    state_without_route,
    validate_domain,
    validate_email,
    validate_public_ipv4,
)

__all__ = ['CERTBOT_IMAGE', 'EDITION_BACKEND_PORTS', 'NGINX_IMAGE', 'PROXY_CERTBOT_CONTAINER', 'PROXY_NGINX_CONTAINER', 'PROXY_ROOT', 'STATE_VERSION', 'SUPPORTED_EDITIONS', '_CERTIFICATE_CHECK_SCRIPT', '_EMAIL_RE', '_FQDN_RE', '_MARKER_RE', 'DomainProxyManager', 'DomainRoute', 'ProxyEnvironment', 'ProxyReport', '_marker_addresses', 'dns_diagnostic', 'dns_matches_environment', 'dry_run_cleanup_script', 'empty_state', 'normalize_state', 'parse_certificate_enddate', 'parse_environment', 'parse_markers', 'render_bootstrap_conf', 'render_certbot_loop', 'render_compose', 'render_config_set', 'render_default_conf', 'render_nginx_conf', 'render_route_conf', 'renewal_dry_run_command', 'renewal_summary', 'renewal_warnings', 'routes_from_state', 'state_with_route', 'state_without_route', 'validate_domain', 'validate_email']

import hashlib
import json
import re
import shlex
import signal  # noqa: F401 - re-exported for compatibility with test patches
import tempfile
import uuid
from collections.abc import Callable
from contextlib import AbstractContextManager
from dataclasses import replace
from pathlib import Path
from typing import Any, Protocol

from tgcloud.deployment.profiles import PRODUCTS, ProductProfile, require_product

from .ownership import MissingCertbotStatusMount, proxy_identity
from .rendering import (
    CERTBOT_IMAGE,
    NGINX_IMAGE,
    PROXY_CERTBOT_CONTAINER,
    PROXY_NGINX_CONTAINER,
    render_bootstrap_conf,
    render_certbot_loop,
    render_compose,
    render_config_set,
    render_default_conf,
    render_nginx_conf,
    render_route_conf,
)
from .scripts import (
    HOST_TOOL_PACKAGES,  # noqa: F401 - re-exported for compatibility
    _certbot_mount_check_command,  # noqa: F401 - re-exported for compatibility
    command_failure_summary,
    defer_interruptions,
    environment_probe_command,
    host_tools_check_script,
    install_config_command,
    proxy_lock_command,
    raise_missing_host_tools,
    status_command,
    verification_statuses,
)


class RemoteLike(Protocol):
    def run(
        self,
        command: str,
        *,
        sudo: bool = False,
        stream: Callable[[str], None] | None = None,
        timeout: float | None = None,
    ) -> tuple[int, str]: ...

    def sftp(self) -> Any: ...

    def hold_lock(
        self, command: str, *, sudo: bool = False, timeout: float = 15
    ) -> AbstractContextManager[None]: ...


class DomainProxyManager:
    """Orchestrate a shared proxy through an already authenticated SSH session."""

    def __init__(
        self,
        session: RemoteLike,
        product: ProductProfile,
        values: dict[str, str],
        log: Callable[[str], None],
        *,
        confirm_public_ipv4: Callable[[str, ProxyEnvironment], str] | None = None,
    ) -> None:
        require_product(product)
        self.session = session
        self.product = product
        self.values = values
        self.log = log
        self.use_sudo = False
        self.confirm_public_ipv4 = confirm_public_ipv4
        self._nat_confirmation: tuple[str, str, tuple[str, ...], tuple[str, ...], frozenset[str]] | None = None
        self.external_plan: dict | None = None
        self._entry_state: dict | None = None
        self._expected_compose_sha256: str | None = None
        self.installer_source: Path | None = None

    def external_entry(self, state=None):
        from .external_entry import selected_entry
        return selected_entry(self, state)

    def prepare(self) -> None:
        code, output = self.session.run("id -u", timeout=10)
        uid = output.strip().splitlines()[-1] if output.strip() else ""
        if code != 0 or not uid.isdigit():
            raise RuntimeError("无法确认 VPS 用户权限。")
        self.use_sudo = uid != "0"
        if self.use_sudo and not self.values.get("sudo_password"):
            code, _ = self.session.run("sudo -n true", timeout=10)
            if code != 0:
                raise RuntimeError("配置 HTTPS 需要 root 或可用的 sudo 权限。")

    def _run(self, command: str, *, timeout: float = 60) -> tuple[int, str]:
        return self.session.run(command, sudo=self.use_sudo, timeout=timeout)

    def read_state(self) -> dict[str, Any]:
        command = f"""
if [[ -f {PROXY_ROOT}/state/domains.json ]]; then
  cat {PROXY_ROOT}/state/domains.json
elif docker container inspect {PROXY_NGINX_CONTAINER} >/dev/null 2>&1 \
  || [[ -f {PROXY_ROOT}/docker-compose.yml ]] \
  || find {PROXY_ROOT}/nginx/conf.d -maxdepth 1 -type f -name '*.conf' -print -quit 2>/dev/null | grep -q .; then
  printf 'TGCLOUD_PROXY_STATE_MISSING_WITH_RUNTIME=1\\n'
  exit 3
fi
"""
        code, output = self._run("bash -c " + shlex.quote(command), timeout=10)
        if code == 3 or "TGCLOUD_PROXY_STATE_MISSING_WITH_RUNTIME=1" in output:
            raise RuntimeError(
                "检测到共享代理运行文件但缺少 domains.json。为避免覆盖未知域名，已停止；请先人工核对 /opt/tgcloud-proxy。"
            )
        if code != 0:
            raise RuntimeError("无法读取共享代理状态；不会覆盖现有配置。")
        state = normalize_state(output)
        self._entry_state = state
        return state

    def _native_managed(self, state=None) -> bool:
        current = self._entry_state if state is None else state
        return (self.installer_source is not None and not self.use_sudo and self.external_plan is None
                and current is not None and current["version"] == 1)

    def _native_arguments(self, operation: str, domain: str) -> tuple[str, ...]:
        return ("https-entry", operation, self.values.get("install_dir", self.product.install_dir),
                NGINX_IMAGE, CERTBOT_IMAGE, validate_domain(domain))

    def _native_read(self, operation: str, domain: str, state=None) -> dict | None:
        if not self._native_managed(state):
            return None
        from tgcloud.cli import host_cli
        raw = host_cli.read_install_check(*self._native_arguments(operation, domain))
        return None if raw is None else json.loads(raw)

    def _native_status(self, domain: str, state=None) -> tuple[int, str, dict | None] | None:
        if not self._native_managed(state):
            return None
        from tgcloud.cli import host_cli
        if not host_cli.supports("https-entry"):
            return None
        result = host_cli.invoke(*self._native_arguments("status", domain))
        if result is None:
            return None
        code, output = result
        if code != 0:
            return code, output, {}
        try:
            parsed = json.loads(output)
        except ValueError:
            return 1, output, {}
        return (0, "", parsed) if isinstance(parsed, dict) else (1, output, {})

    def _native_tool(self) -> Path | None:
        if self.installer_source is None or not self._native_managed():
            return None
        from tgcloud.cli import host_cli
        return host_cli.deployment_tool("https-entry", self.installer_source)

    def recheck_install_entry(self, domain: str, *, present: bool, configure: bool) -> None:
        if not domain or self._entry_state is None or self.external_plan is not None:
            return
        expected = self._entry_state
        if self.read_state() != expected:
            raise ValueError("确认后共享 HTTPS 状态已变化；请重新预检")
        if not configure:
            if self.status(expected).state != "healthy":
                raise ValueError("确认后 HTTPS 入口检查未通过；请先单独修复")
            return
        environment = self.probe(domain)
        if present:
            self._validate_environment(environment)
        else:
            error = public_endpoint_error(environment, self.values["vps_host"])
            if error:
                raise ValueError(error + "\n" + dns_diagnostic(environment, self.values["vps_host"]))

    def probe(self, domain: str) -> ProxyEnvironment:
        observed = self._native_read("probe", domain)
        if observed is None:
            code, output = self._run(environment_probe_command(self.product, domain), timeout=30)
            raise_missing_host_tools(output)
            if code != 0:
                raise RuntimeError("无法完成域名、Docker 与端口环境检测。")
            environment = parse_environment(output)
        else:
            environment = ProxyEnvironment(
                observed["core"], observed["docker"], observed["port_80"], observed["port_443"],
                tuple(observed["dns_a"]), tuple(observed["dns_aaaa"]), tuple(observed["local_addresses"]),
            )
        # A plain port probe remains a pure probe. Entry callers read state
        # explicitly; write paths always re-read it under the shared lock.
        adapter = self.external_entry(self._entry_state or {"version": 1, "domains": {}})
        if adapter:
            environment = adapter.environment(domain, environment)
        if self._nat_confirmation is not None:
            saved_domain, address, dns_a, dns_aaaa, local = self._nat_confirmation
            if (domain == saved_domain and environment.dns_a == dns_a and environment.dns_aaaa == dns_aaaa
                    and local <= set(environment.local_addresses)
                    and nat_public_ipv4_candidate(environment) == address):
                return replace(environment, confirmed_public_ipv4=address)
            self._nat_confirmation = None
        return environment

    def confirm_nat(self, domain: str, environment: ProxyEnvironment) -> ProxyEnvironment:
        """Confirm only during interactive preflight, before the write lock."""
        if (dns_matches_environment(environment, self.values["vps_host"]) or not environment.ports_safe
                or self.confirm_public_ipv4 is None or not nat_public_ipv4_candidate(environment)):
            return environment
        supplied = self.confirm_public_ipv4(domain, environment)
        if not supplied:
            return environment
        address = validate_public_ipv4(supplied)
        if address != nat_public_ipv4_candidate(environment):
            raise ValueError("云控制台公网 IPv4 与域名 A 记录不一致；未放行 HTTPS。")
        self._nat_confirmation = (domain, address, environment.dns_a, environment.dns_aaaa,
                                  frozenset(environment.local_addresses))
        return replace(environment, confirmed_public_ipv4=address)

    def _validate_environment(self, environment: ProxyEnvironment) -> None:
        if environment.core != "OK":
            raise RuntimeError(f"请先完成 {self.product.display_name} 基础部署并确认管理页在 VPS 回环端口可访问。")
        if environment.docker != "OK":
            raise RuntimeError("VPS 上的 Docker Compose 不可用，请先完成基础环境部署。")
        error = public_endpoint_error(environment, self.values["vps_host"])
        if error:
            raise RuntimeError(error + "\n" + dns_diagnostic(environment, self.values["vps_host"]))

    def _upload_files(self, files: dict[str, str]) -> str:
        token = uuid.uuid4().hex
        remote = f"/tmp/tgcloud-proxy-{token}"  # nosec B108
        code, _ = self.session.run(f"mkdir -m 700 {remote}", timeout=15)
        if code != 0:
            raise RuntimeError("无法创建 HTTPS 配置临时目录。")
        sftp = self.session.sftp()
        try:
            for relative, content in files.items():
                parts = relative.split("/")
                directory = remote
                for part in parts[:-1]:
                    directory += "/" + part
                    try:
                        sftp.mkdir(directory, 448)
                    except OSError:
                        pass
                target = f"{remote}/{relative}"
                with tempfile.NamedTemporaryFile(
                    mode="w", encoding="utf-8", newline="\n", delete=False
                ) as handle:
                    handle.write(content)
                    local = Path(handle.name)
                try:
                    sftp.put(str(local), target)
                    sftp.chmod(target, 384)
                finally:
                    local.unlink(missing_ok=True)
        finally:
            sftp.close()
        return remote

    def _install_config(self, state: dict[str, Any], *, candidate: str = "") -> None:
        self._expected_compose_sha256 = None
        from tgcloud.deployment import architecture
        architecture.check_docker(self.session)
        for image in (NGINX_IMAGE, CERTBOT_IMAGE):
            architecture.ensure_image(self.session, image)
        files = render_config_set(state, candidate_domain=candidate)
        active_domains = certificate_domains(state)
        external_auth = state.get("version") == 2
        files["docker-compose.yml"] = render_compose(active_domains, external_auth=external_auth)
        remote = self._upload_files(files)
        try:
            code, _ = self._run(install_config_command(remote, external_auth=external_auth), timeout=180)
            if code != 0:
                raise RuntimeError("无法安装共享代理配置。")
            self._expected_compose_sha256 = hashlib.sha256(files["docker-compose.yml"].encode("utf-8")).hexdigest()
        finally:
            self.session.run(f"rm -rf -- {remote}", timeout=20)

    def _compose(self, arguments: str, *, timeout: float = 180) -> tuple[int, str]:
        result = self._run(
            f"docker compose -f {PROXY_ROOT}/docker-compose.yml {arguments}",
            timeout=timeout,
        )
        if result[0] == 0 and arguments.startswith("up "):
            from tgcloud.deployment import architecture
            if "certbot" in arguments:
                architecture.check_container(self.session, PROXY_CERTBOT_CONTAINER, require_running=True)
                self._ensure_certbot_mounts(timeout=timeout)
            if "nginx" in arguments:
                architecture.check_container(self.session, PROXY_NGINX_CONTAINER, require_running=True)
        return result

    def _inspect_certbot_mounts(self) -> None:
        template = ('{"labels":{{json .Config.Labels}},"network":{{json .HostConfig.NetworkMode}},'
                    '"mounts":{{json .Mounts}}}')
        code, output = self._run(
            "docker container inspect --format " + shlex.quote(template) + " " + PROXY_CERTBOT_CONTAINER,
            timeout=20,
        )
        if code:
            raise RuntimeError("无法读回 Certbot 挂载；不确认共享代理部署成功。")
        try:
            item = json.loads(output)
            if not isinstance(item, dict):
                raise TypeError
            proxy_identity(PROXY_CERTBOT_CONTAINER, item)
        except (TypeError, AttributeError, KeyError, json.JSONDecodeError):
            raise RuntimeError("Certbot 身份或挂载元数据不可验证；不自动重建。") from None

    def _ensure_certbot_mounts(self, *, timeout: float) -> None:
        from tgcloud.deployment import architecture
        try:
            self._inspect_certbot_mounts()
            return
        except MissingCertbotStatusMount:
            expected = self._expected_compose_sha256
            if not expected:
                raise
        # Only an exact, known incomplete layout can reach this branch. The
        # current generated Compose must still be the file installed by this
        # transaction; do not recreate against unknown or concurrently edited input.
        code, output = self._run("sha256sum " + shlex.quote(PROXY_ROOT + "/docker-compose.yml"), timeout=15)
        if code or not output.split() or output.split()[0] != expected:
            raise RuntimeError("共享 Compose 已变化或无法核对；不自动重建 Certbot。")
        self.log("Certbot 缺少续期状态挂载；正在按已校验的受管配置重建该容器。")
        code, _ = self._run(
            f"docker compose -f {PROXY_ROOT}/docker-compose.yml up -d --force-recreate certbot", timeout=timeout,
        )
        if code:
            raise RuntimeError("Certbot 受管重建失败；未确认共享代理部署成功。")
        architecture.check_container(self.session, PROXY_CERTBOT_CONTAINER, require_running=True)
        self._inspect_certbot_mounts()  # A failed readback propagates; no retry loop.

    def _activate(self, state: dict[str, Any], *, candidate: str = "") -> None:
        self._install_config(state, candidate=candidate)
        code, _ = self._compose("config --quiet", timeout=30)
        if code != 0:
            raise RuntimeError("共享代理 Compose 配置校验失败。")
        code, _ = self._compose("up -d nginx", timeout=180)
        if code != 0:
            raise RuntimeError("Nginx 容器启动失败。")
        code, _ = self._compose("exec -T nginx nginx -t", timeout=30)
        if code != 0:
            raise RuntimeError("Nginx 配置自检失败。")
        code, _ = self._compose("exec -T nginx nginx -s reload", timeout=30)
        if code != 0:
            raise RuntimeError("Nginx 安全重载失败。")

    def _certificate_exists(self, domain: str) -> bool:
        code, _ = self._run(
            f"test -s {PROXY_ROOT}/certbot/letsencrypt/live/{shlex.quote(domain)}/fullchain.pem "
            f"-a -s {PROXY_ROOT}/certbot/letsencrypt/live/{shlex.quote(domain)}/privkey.pem",
            timeout=10,
        )
        return code == 0

    def _renewal_config_exists(self, domain: str) -> bool:
        code, _ = self._run(
            f"test -s {PROXY_ROOT}/certbot/letsencrypt/renewal/{shlex.quote(domain)}.conf",
            timeout=10,
        )
        return code == 0

    def _certificate_valid(self, domain: str) -> bool:
        if not self._certificate_exists(domain) or not self._renewal_config_exists(domain):
            return False
        certificate_path = f"/etc/letsencrypt/live/{domain}/fullchain.pem"
        code, _ = self._run(
            "docker run --rm --read-only --cap-drop ALL "
            "--security-opt no-new-privileges --tmpfs /tmp "
            f"-v {PROXY_ROOT}/certbot/letsencrypt:/etc/letsencrypt:ro "
            f"--entrypoint python {CERTBOT_IMAGE} -c "
            f"{shlex.quote(_CERTIFICATE_CHECK_SCRIPT)} "
            f"{shlex.quote(certificate_path)} {shlex.quote(domain)}",
            timeout=90,
        )
        return code == 0

    def _obtain_certificate(self, domain: str, email: str) -> None:
        certificate_exists = self._certificate_exists(domain)
        if self._certificate_valid(domain):
            self.log("检测到该域名已有且仍有效的证书，将直接复用。")
            return
        registration = (
            "--email " + shlex.quote(email)
            if email
            else "--register-unsafely-without-email"
        )
        command = (
            "run --rm --entrypoint certbot certbot certonly --webroot "
            "-w /var/www/certbot --non-interactive --agree-tos --no-eff-email "
            f"{registration} {'--force-renewal ' if certificate_exists else ''}"
            f"--cert-name {shlex.quote(domain)} -d {shlex.quote(domain)}"
        )
        code, output = self._compose(command, timeout=300)
        if code != 0:
            detail = command_failure_summary(output, secrets=(email,))
            if detail:
                self.log("Certbot 签发详情（已脱敏）：\n" + detail)
            raise RuntimeError(
                "Let's Encrypt 证书签发失败。请检查运行日志中的 Certbot 具体原因；"
                "旧域名配置未提交。"
            )

    def _commit_state(self, state: dict[str, Any]) -> None:
        state = normalize_state(json.dumps(state))
        content = json.dumps(state, ensure_ascii=True, indent=2, sort_keys=True) + "\n"
        remote = self._upload_files({"domains.json": content})
        staged = f"{PROXY_ROOT}/state/.domains-{uuid.uuid4().hex}.json"
        try:
            code, _ = self._run(
                f"install -d -m 700 {PROXY_ROOT} {PROXY_ROOT}/state && "
                f"install -m 600 {remote}/domains.json {staged} && "
                f"mv -f -- {staged} {PROXY_ROOT}/state/domains.json",
                timeout=15,
            )
            if code != 0:
                raise RuntimeError("共享代理状态文件提交失败；已开始回退。")
        finally:
            # Cleanup must not turn an already committed state into a rollback.
            # Otherwise domains.json and the restored runtime would disagree.
            for cleanup, command in (
                (self._run, f"rm -f -- {staged}"),
                (self.session.run, f"rm -rf -- {remote}"),
            ):
                try:
                    code, _ = cleanup(command, timeout=20)
                    if code != 0:
                        self.log("警告：HTTPS 临时文件清理未完成；请人工核对，状态提交结果保持不变。")
                except Exception:  # noqa: BLE001 - best-effort cleanup boundary
                    self.log("警告：HTTPS 临时文件清理未完成；请人工核对，状态提交结果保持不变。")

    def _ensure_state_file(self, state: dict[str, Any]) -> None:
        """Mark a newly created proxy as managed before the first mutation."""
        code, _ = self._run(f"test -f {PROXY_ROOT}/state/domains.json", timeout=10)
        if code == 0:
            return
        if code != 1:
            raise RuntimeError("无法确认共享代理状态文件是否存在。")
        self._commit_state(state)

    def status(self, state: dict[str, Any] | None = None) -> ProxyReport:
        current = state or self.read_state()
        adapter = self.external_entry(current)
        if adapter:
            return adapter.status(current)
        item = current["domains"].get(self.product.key)
        if not item:
            return ProxyReport("", "not_configured", "当前实例尚未配置域名访问。", ())
        domain = item["domain"]
        native = self._native_status(domain, current)
        if native is not None:
            code, output, markers = native
        else:
            code, output = self._run(status_command(self.product, domain), timeout=90)
            markers = parse_markers(output)
        if code != 0 or markers.get("PROXY_STATUS") != "OK":
            self.log(
                "TGCLOUD_PROXY_STATUS_SCRIPT=FAILED: "
                f"exit={code}, completion_marker="
                f"{'present' if markers.get('PROXY_STATUS') == 'OK' else 'missing'}"
            )
            detail = command_failure_summary(output)
            if detail:
                self.log("域名状态检查脚本详情（已脱敏）：\n" + detail)
            return ProxyReport(
                domain,
                "running_error",
                "域名状态检查脚本未完整执行；没有把缺失结果误判为业务失败。",
                (("状态检查脚本", "失败"),),
                tuple(sorted(markers.items())),
            )
        statuses = verification_statuses(markers)
        if any(value != "通过" for _, value in statuses):
            state_name = "certificate_error" if markers.get("PROXY_CERT") == "FAILED" else "running_error"
            return ProxyReport(
                domain,
                state_name,
                "域名路由存在，但至少一项运行检查未通过。",
                statuses,
                tuple(sorted(markers.items())),
            )
        expiry = ""
        days_text = ""
        warnings = list(renewal_warnings(markers))
        if markers.get("PROXY_CERT_END"):
            expiry, days = parse_certificate_enddate(markers["PROXY_CERT_END"])
            days_text = f"，剩余约 {days} 天"
            if days <= 21:
                warnings.append("证书剩余不足 22 天，请检查自动续期是否成功。")
        return ProxyReport(
            domain,
            "healthy",
            f"VPS 本机 https://{domain} 自检通过；证书到期日 {expiry}{days_text}。"
            "公网连通性仍需从你的电脑或外部网络单独验收。"
            + "".join("\n提醒：" + text for text in warnings) + renewal_summary(markers),
            statuses,
            tuple(sorted(markers.items())),
            tuple(warnings),
        )

    def detect(self, domain: str) -> ProxyReport:
        environment = self.confirm_nat(domain, self.probe(domain))
        statuses = (
            (f"{self.product.display_name} 基础服务", "通过" if environment.core == "OK" else "失败"),
            ("Docker Compose", "通过" if environment.docker == "OK" else "失败"),
            ("80 端口", "通过" if environment.port_80 in {"FREE", "OWN"} else "失败"),
            ("443 端口", "通过" if environment.port_443 in {"FREE", "OWN"} else "失败"),
            ("DNS 指向当前 VPS", "通过" if dns_matches_environment(environment, self.values["vps_host"]) else "失败"),
        )
        state = "detected" if all(value == "通过" for _, value in statuses) else "detected_error"
        return ProxyReport(
            domain,
            state,
            "环境检测完成。" + dns_diagnostic(environment, self.values["vps_host"]),
            statuses,
        )

    def configure(self, domain: str, email: str) -> ProxyReport:
        domain = validate_domain(domain)
        email = validate_email(email)
        helper = self._native_tool()
        if helper is not None:
            expected = self._entry_state["domains"].get(self.product.key, {}).get("domain", "")
            confirmed = (self._nat_confirmation[1] if self._nat_confirmation and self._nat_confirmation[0] == domain else "")
            arguments = self._native_arguments("configure", domain)
            command = "exec " + shlex.join((str(helper), *arguments, email, confirmed, expected))
            code, output = self.session.run(command, stream=self.log, timeout=2300, shutdown_grace=440, stream_recovery=True)
            if code or "TGCLOUD_HTTPS=OK" not in output.splitlines():
                raise RuntimeError("共享 HTTPS 未通过；基础服务和数据保留，可用 --configure-https 单独修复。")
            return self.status()
        with self.session.hold_lock(proxy_lock_command(), sudo=self.use_sudo):
            return self._configure_locked(domain, email)

    def _check_compose_owner(self, state: dict[str, Any], *, all_routes: bool = False) -> None:
        """Run under the global lock before a generic writer changes a route."""
        from .maintenance import parse_compose_registry, validate_compose_routes

        # A bounded, read-only host probe. No downloaded code or instance .env is
        # executed. Refuse uncertain transactions and linked/private-file aliases.
        script = f"""import stat, sys
from pathlib import Path
root = Path({PROXY_ROOT!r})
path = root / 'state/compose-instances.json'
journal = root / 'state/compose-transaction.json'
if journal.exists() or journal.is_symlink():
    raise SystemExit(3)
if path.exists() or path.is_symlink():
    if path.resolve() != path or any(parent.is_symlink() or parent.exists() and not parent.is_dir() for parent in path.parents):
        raise SystemExit(4)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o077 or info.st_nlink != 1 or info.st_size > 65536:
        raise SystemExit(5)
    sys.stdout.buffer.write(path.read_bytes())
"""
        code, output = self._run("python3 -c " + shlex.quote(script), timeout=10)
        if code:
            raise RuntimeError("Compose HTTPS 归属或事务状态无法安全核对；停止，不覆盖共享入口。")
        if not output.strip():
            return
        try:
            registry = parse_compose_registry(output)
            validate_compose_routes(registry, state)
        except (ValueError, TypeError):
            raise RuntimeError("Compose HTTPS 归属清单无法识别或与路由不一致；停止，不覆盖共享入口。") from None
        if all_routes and registry["instances"]:
            raise RuntimeError("共享代理仍承载 Compose HTTPS；请先用对应部署包移除这些路由，再安排入口迁移。")
        if self.product.key in registry["instances"]:
            raise RuntimeError("当前实例的 HTTPS 由独立 Compose 部署管理；请使用原部署包的 HTTPS 工具修改或移除。")

    def _configure_locked(self, domain: str, email: str) -> ProxyReport:
        self.log("TGCLOUD_STAGE=HTTPS_ENV")
        previous = self.read_state()
        self._check_compose_owner(previous)
        adapter = self.external_entry(previous)
        if adapter:
            return adapter.configure(domain, email, already_locked=True)
        target = state_with_route(previous, self.product, domain)
        environment = self.probe(domain)
        self._validate_environment(environment)
        self._ensure_state_file(previous)
        bootstrap_state = previous
        repairing_existing_certificate = False
        current_route = previous["domains"].get(self.product.key)
        if (
            current_route
            and current_route["domain"] == domain
            and not self._certificate_valid(domain)
        ):
            bootstrap_state = state_without_route(previous, self.product.key)
            repairing_existing_certificate = True
            self.log("现有域名的证书或续期配置不可用；将使用安全 ACME 引导模式修复。")
        self.log("环境检查通过；正在启用仅用于 ACME 的安全 HTTP 引导配置。")
        committed = False
        try:
            self.log("TGCLOUD_STAGE=ACME")
            self._activate(bootstrap_state, candidate=domain)
            self.log("TGCLOUD_STAGE=CERTIFICATE")
            self._obtain_certificate(domain, email)
            self._activate(target)
            code, _ = self._compose("up -d certbot", timeout=180)
            if code != 0:
                raise RuntimeError("证书续期容器启动失败。")
            self.log("TGCLOUD_STAGE=HTTPS_VERIFY")
            report = self.status(target)
            if report.state != "healthy":
                failed = [title for title, value in report.statuses if value != "通过"]
                for title, value in report.statuses:
                    self.log(f"TGCLOUD_PROXY_CHECK={title}:{value}")
                raise RuntimeError(
                    "域名访问最终自检未通过："
                    + "、".join(failed or ["状态输出不完整"])
                    + "。详细结果已写入运行日志；域名配置未提交。"
                )
            with defer_interruptions():
                self._commit_state(target)
                committed = True
            return report
        except (Exception, KeyboardInterrupt):
            if committed:
                # Cancellation after the atomic commit must not restore an old
                # runtime while domains.json already describes the new route.
                raise
            try:
                with defer_interruptions():
                    if has_routes(previous):
                        try:
                            self._activate(previous)
                        except Exception:
                            if not repairing_existing_certificate:
                                raise
                            self._activate(bootstrap_state, candidate=domain)
                        code, _ = self._compose("up -d certbot", timeout=120)
                    else:
                        self._install_config(previous)
                        code, _ = self._compose("stop nginx certbot", timeout=60)
                    if code:
                        raise RuntimeError("代理回退容器操作失败")
            except (Exception, KeyboardInterrupt) as rollback_error:  # noqa: BLE001 - rollback boundary
                self.log(f"警告：共享代理自动回退未完全成功：{type(rollback_error).__name__}")
            raise

    def _verify_remaining_routes(self, state: dict[str, Any]) -> None:
        for edition in state["domains"]:
            retained = DomainProxyManager(
                self.session, PRODUCTS[edition], self.values, self.log
            )
            retained.use_sudo = self.use_sudo
            report = retained.status(state)
            if report.state != "healthy":
                raise RuntimeError(
                    f"剩余 {PRODUCTS[edition].display_name} 的 HTTPS 自检未通过；已开始回退。"
                )
        if state.get("external_webdav"):
            from tgcloud.addons.external_webdav.verification import (
                check_public_denials,
            )
            for route in state["external_webdav"].values():
                check_public_denials(route)

    def apply_shared_state_locked(self, target: dict[str, Any], *, domain: str = "", email: str = "", verify=None,
                                  verify_remaining: bool = True) -> None:
        """Called under the existing proxy lock; preserve every other owner.

        External callers revoke authentication before changing credentials. A
        failed activation never restores an enabled external route.
        """
        adapter = self.external_entry()
        if adapter:
            adapter.transaction(target, domain=domain, email=email, verify=verify, verify_remaining=verify_remaining)
            return
        target = normalize_state(json.dumps(target))
        previous = self.read_state()
        fallback = normalize_state(json.dumps(previous))
        key = self.product.key
        if key in fallback.get("external_webdav", {}):
            fallback["external_webdav"][key]["enabled"] = False
        self._ensure_state_file(previous)
        committed = False
        try:
            if domain and not self._certificate_valid(domain):
                self._validate_environment(self.probe(domain))
                self._activate(previous, candidate=domain)
                self._obtain_certificate(domain, validate_email(email))
            self._activate(target)
            code, _ = self._compose("up -d certbot", timeout=180)
            if code:
                raise RuntimeError("共享续期配置未启动。")
            if verify_remaining:
                self._verify_remaining_routes(target)
            if verify:
                verify()
            with defer_interruptions():
                self._commit_state(target)
                committed = True
        except (Exception, KeyboardInterrupt):
            if not committed:
                with defer_interruptions():
                    try:
                        if has_routes(fallback):
                            self._activate(fallback)
                            self._compose("up -d certbot", timeout=180)
                        else:
                            self._install_config(fallback)
                            self._compose("stop nginx certbot", timeout=60)
                        self._commit_state(fallback)
                    except Exception:  # noqa: BLE001 - rollback failure must not mask original error
                        self.log("外部入口变更失败且代理回退未确认；认证已撤销，请核对共享代理。")
            raise

    def _backup_helper(self, action: str, *, archive: str = "", keep: int = 5, plan: str = "") -> dict[str, str]:
        if action not in {"backup", "check", "drill", "list", "prune-preview", "prune"}:
            raise ValueError("未知备份操作。")
        if archive and not re.fullmatch(r"proxy-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{32}\.tar\.gz", archive):
            raise ValueError("备份文件名无效。")
        if type(keep) is not int or not 1 <= keep <= 50:
            raise ValueError("保留份数必须为 1～50。")
        if action == "prune" and not re.fullmatch(r"[0-9a-f]{64}", plan):
            raise ValueError("请先预览并确认清理计划。")
        code, output = self._run(
            "bash -c " + shlex.quote(host_tools_check_script(("python3", "timeout"))), timeout=15
        )
        raise_missing_host_tools(output)
        if code:
            raise RuntimeError("无法完成共享代理维护的宿主依赖检查。")
        self.log("TGCLOUD_STAGE=PROXY_BACKUP")
        source = Path(__file__).with_name("maintenance.py").read_text(encoding="utf-8")
        remote = self._upload_files({"proxy_maintenance.py": source})
        arguments = [action, "--keep", str(keep)]
        if archive:
            arguments += ["--archive", archive]
        if plan:
            arguments += ["--plan", plan]
        try:
            code, output = self._run(
                "timeout --signal=TERM --kill-after=15s 150s python3 "
                + shlex.quote(remote + "/proxy_maintenance.py") + " " + shlex.join(arguments), timeout=190
            )
            markers = parse_markers(output)
            if code != 0 or markers.get("PROXY_BACKUP") != action.upper().replace("-", "_") + "_OK":
                raise RuntimeError("共享代理备份操作未通过：请核对完整性、磁盘空间及清理计划。源文件或备份清单变化时需重新预览；清理可能部分完成，请刷新清单核对。")
            return markers
        finally:
            try:
                code, _ = self.session.run(f"rm -rf -- {shlex.quote(remote)}", timeout=30)
                if code != 0:
                    self.log("警告：维护临时脚本清理未完成，请恢复连接后检查。")
            except Exception:  # noqa: BLE001 - cleanup must not mask original failure
                self.log("警告：维护连接中断；远端备份操作设有执行时限，请恢复连接后检查。")

    @staticmethod
    def _backup_items(value: object) -> list[dict]:
        if not isinstance(value, list) or len(value) > 500:
            raise RuntimeError("备份清单无法识别。")
        for item in value:
            if (not isinstance(item, dict) or set(item) != {"name", "created_utc", "size_bytes", "integrity"}
                    or not isinstance(item["name"], str)
                    or not re.fullmatch(r"proxy-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{32}\.tar\.gz", item["name"])
                    or not isinstance(item["created_utc"], str)
                    or not re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2} UTC", item["created_utc"])
                    or type(item["size_bytes"]) is not int or not 0 <= item["size_bytes"] <= 1024 ** 3
                    or item["integrity"] not in {"OK", "FAILED"}):
                raise RuntimeError("备份清单包含无法识别的数据。")
        return value

    def manage_backups(self, action: str, *, keep: int = 5, plan: str = "") -> dict:
        if action not in {"list", "prune-preview", "prune"}:
            raise ValueError("未知备份管理操作。")
        with self.session.hold_lock(proxy_lock_command(), sudo=self.use_sudo):
            markers = self._backup_helper(action, keep=keep, plan=plan)
            try:
                if action == "list":
                    return {"items": self._backup_items(json.loads(markers["PROXY_BACKUP_ITEMS"]))}
                result = json.loads(markers["PROXY_BACKUP_PLAN"])
                if (not isinstance(result, dict) or set(result) != {"keep", "retain", "remove", "plan_id"}
                        or type(result["keep"]) is not int or result["keep"] != keep or not isinstance(result["plan_id"], str)
                        or not re.fullmatch(r"[0-9a-f]{64}", result["plan_id"])
                        or not isinstance(result["retain"], list) or len(result["retain"]) > keep
                        or any(not isinstance(name, str) or not re.fullmatch(r"proxy-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{32}\.tar\.gz", name) for name in result["retain"])):
                    raise RuntimeError("清理计划无法识别。")
                self._backup_items(result["remove"])
                names = [item["name"] for item in result["remove"]] + result["retain"]
                if len(names) != len(set(names)) or (result["remove"] and not result["retain"]):
                    raise RuntimeError("清理计划必须保留有效备份，且不能包含重复文件。")
                return {"plan": result}
            except (ValueError, KeyError, TypeError) as exc:
                raise RuntimeError("备份管理输出无法识别。") from exc

    def maintenance(self, action: str, *, archive: str = "") -> str:
        adapter = self.external_entry()
        if adapter:
            if action == "renew_dry_run":
                return adapter.maintain(dry_run=True)
            with self.session.hold_lock(proxy_lock_command(), sudo=self.use_sudo):
                if action == "backup":
                    return "外部入口私密元数据／专属站点备份：" + str(adapter.backup(self.read_state())) + "；证书仍保留在原挂载中，不复制其他项目私钥。"
                if action in {"check", "drill"}:
                    return adapter.check_backup(archive=archive, drill=action == "drill")
                raise ValueError("未知的外部入口维护操作。")
        # Explicit local-only maintenance; never restore over running services.
        if action not in {"backup", "check", "drill", "renew_dry_run"}:
            raise ValueError("未知的共享代理维护操作。")
        with self.session.hold_lock(proxy_lock_command(), sudo=self.use_sudo):
            state = self.read_state()
            if action == "renew_dry_run":
                item = state["domains"].get(self.product.key)
                if not item:
                    raise RuntimeError("当前实例尚未配置域名，不能演练续期。")
                domain = validate_domain(item["domain"])
                self.log("TGCLOUD_STAGE=RENEW_DRY_RUN")
                task_id = uuid.uuid4().hex
                try:
                    code, output = self._run(renewal_dry_run_command(domain, task_id), timeout=600)
                except Exception:
                    try:
                        cleanup = "cleanup() { " + dry_run_cleanup_script(task_id) + " }; cleanup"
                        cleanup_code, cleanup_output = self._run("bash -c " + shlex.quote(cleanup), timeout=55)
                        if cleanup_code != 0 or parse_markers(cleanup_output).get("PROXY_TASK_CLEANUP") != "OK":
                            self.log("警告：本次维护任务清理未确认，请检查一次性续期容器。")
                    except Exception:  # noqa: BLE001 - disconnected SSH cannot confirm cleanup
                        self.log("警告：连接中断，暂时无法确认维护任务清理；远端任务设有 9 分钟执行时限，请恢复连接后检查。")
                    raise
                if parse_markers(output).get("PROXY_TASK_CLEANUP") != "OK":
                    raise RuntimeError("维护任务清理未确认，请检查本次一次性续期容器；不要重复演练。")
                if code != 0:
                    self.log("续期演练详情（已脱敏）：\n" + command_failure_summary(output))
                    raise RuntimeError("续期 dry-run 未通过；请检查 ACME 公网连通与 Certbot 日志。")
                return "续期 dry-run 通过；没有替换正式证书，也不代表长期自动续期已经验收。"
            markers = self._backup_helper(action, archive=archive)
            path = markers.get("PROXY_BACKUP_PATH", "")
            if not re.fullmatch(r"/opt/tgcloud-proxy-backups/proxy-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{32}\.tar\.gz", path):
                raise RuntimeError("备份输出路径无法识别。")
            title = {"backup": "私密备份及完整性检查通过", "check": "所选/最近备份完整性检查通过", "drill": "所选/最近备份隔离解包与文件校验通过"}[action]
            return f"{title}。\n备份仅保存在 VPS：{path}\n含证书私钥，不要上传至 GitHub。"

    def remove(self) -> ProxyReport:
        with self.session.hold_lock(proxy_lock_command(), sudo=self.use_sudo):
            return self._remove_locked()

    def _remove_locked(self) -> ProxyReport:
        previous = self.read_state()
        self._check_compose_owner(previous)
        adapter = self.external_entry(previous)
        if adapter:
            return adapter.remove_locked()
        target = state_without_route(previous, self.product.key)
        if target == previous:
            return ProxyReport("", "not_configured", "当前实例没有可移除的域名路由。", ())
        removed = previous["domains"][self.product.key]["domain"]
        try:
            if has_routes(target):
                self._activate(target)
                code, _ = self._compose("up -d certbot", timeout=180)
                if code != 0:
                    raise RuntimeError("剩余域名的证书续期容器更新失败；已开始回退。")
                self._verify_remaining_routes(target)
                self._commit_state(target)
            else:
                self._install_config(target)
                code, _ = self._compose("stop nginx certbot", timeout=60)
                if code != 0:
                    raise RuntimeError("最后一个域名已移除，但共享代理容器停止失败。")
                self._commit_state(target)
        except Exception:
            if has_routes(previous):
                self._activate(previous)
                self._compose("up -d certbot", timeout=120)
            raise
        return ProxyReport(
            "",
            "not_configured",
            f"已移除 {removed} 的 {self.product.display_name} 路由；证书文件默认保留。",
            (),
        )
