"""Isolated reverse proxy transaction fixtures."""
from __future__ import annotations

import json
from contextlib import nullcontext
from types import SimpleNamespace

from tgcloud.deployment.profiles import CLOUDDRIVE2_PRODUCT
from tgcloud.deployment.proxy.manager import (
    DomainProxyManager,
    ProxyEnvironment,
    ProxyReport,
    empty_state,
    normalize_state,
)


def external_state(domain, base=None):
    state = json.loads(json.dumps(base if base is not None else empty_state()))
    state.update(version=2, external_webdav={"clouddrive2": {
        "kind": "external_webdav", "owner": "tgcloud.external_webdav", "edition": "clouddrive2",
        "instance_dir": "/opt/tgcloud", "domain": domain, "enabled": True,
        "backend_port": 19798, "auth_file": "external-auth/clouddrive2.htpasswd"}})
    return normalize_state(json.dumps(state))


class TransactionManager(DomainProxyManager):
    def __init__(self, product=CLOUDDRIVE2_PRODUCT) -> None:
        session = SimpleNamespace(hold_lock=lambda *_args, **_kwargs: nullcontext())
        super().__init__(session, product, {"vps_host": "203.0.113.10"}, lambda _line: None)
        self.current = empty_state()
        self.activations: list[tuple[dict, str]] = []
        self.commits: list[dict] = []
        self.ensured_states: list[dict] = []
        self.compose_calls: list[str] = []
        self.stop_called = False
        self.fail_certificate = False
        self.fail_final_activation = False
        self.fail_commit = False
        self.valid_certificate = True

    def read_state(self):
        return json.loads(json.dumps(self.current))

    def _check_compose_owner(self, state):
        pass  # Isolated transaction harness; real ownership probes have separate regressions.

    def probe(self, domain: str) -> ProxyEnvironment:
        return ProxyEnvironment(
            "OK", "OK", "FREE", "FREE", ("203.0.113.10",), (), ("203.0.113.10",)
        )

    def _activate(self, state, *, candidate="") -> None:
        self.activations.append((json.loads(json.dumps(state)), candidate))
        if self.fail_final_activation and not candidate and state != self.current:
            raise RuntimeError("nginx -t failed")

    def _obtain_certificate(self, domain: str, email: str) -> None:
        if self.fail_certificate:
            raise RuntimeError("certbot failed")

    def _certificate_valid(self, domain: str) -> bool:
        return self.valid_certificate

    def _ensure_state_file(self, state) -> None:
        self.ensured_states.append(json.loads(json.dumps(state)))

    def _compose(self, arguments: str, *, timeout: float = 180):
        self.compose_calls.append(arguments)
        if arguments.startswith("stop"):
            self.stop_called = True
        return 0, ""

    def _commit_state(self, state) -> None:
        if self.fail_commit:
            raise RuntimeError("state commit failed")
        self.commits.append(json.loads(json.dumps(state)))
        self.current = json.loads(json.dumps(state))

    def _install_config(self, state, *, candidate="") -> None:
        self.activations.append((json.loads(json.dumps(state)), candidate))

    def _verify_remaining_routes(self, state) -> None:
        pass  # Real status/transport behavior is tested separately.

    def status(self, state=None) -> ProxyReport:
        current = state or self.current
        item = current["domains"].get(self.product.key)
        if not item:
            return ProxyReport("", "not_configured", "none", ())
        return ProxyReport(item["domain"], "healthy", "ok", (("HTTPS", "通过"),))
