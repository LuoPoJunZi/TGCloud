"""Human-facing installer presentation; parsers still consume raw command results."""
from __future__ import annotations

import re
from dataclasses import dataclass, field

from tgcloud.deployment.host import SafeLog

WARNING = re.compile(r"\bwarn(?:ing)?\b|警告", re.IGNORECASE)
ERROR = re.compile(r"\[TGCloud\]\[ERROR\]|(?:^|\s)(?:ERROR:|FATAL:|E: |Err:\d+)|Permission denied|No space left on device|^操作失败：")
BUILD_LINE = re.compile(r"^#\d+\s")
KNOWN_WARNINGS = (
    ("pip", re.compile(r"Running pip as (?:the )?['\"]root['\"] user", re.IGNORECASE), "镜像构建中的 pip root 提醒；不代表 Bot 的运行身份，以步骤退出和最终检查为准。"),
    ("ca", re.compile(r"skipping ca-certificates\.crt.*(?:exactly one certificate|exactly one.*CRL)", re.IGNORECASE), "CA 合集无法作为单证书索引；不等于域名证书失效，仍须检查后续 TLS。"),
    ("uid", re.compile(r"(?:uid|UID) 10001.*(?:greater than|outside).*SYS_UID_MAX"), "固定 UID 超出系统账号默认范围；仍须核对账号创建与容器检查结果。"),
)


def classify_install_line(line: str, *, base: bool = False) -> tuple[str, str, str]:
    """Presentation only: never infer a command's success from a warning string."""
    line = re.sub(r"\x1b\[[0-?]*[ -/]*[@-~]", "", line)
    if ERROR.search(line):
        return "错误", "", ""
    if base and BUILD_LINE.match(line):
        for key, pattern, explanation in KNOWN_WARNINGS:
            if pattern.search(line):
                return "提醒", key, explanation
    if WARNING.search(line):
        if base and re.search(r"^\[TGCloud\].*安装文件系统总容量低于推荐的 (?:20|50)GB", line):
            return "提醒", "capacity", "推荐总容量不是硬性门槛；仍须通过现有容量与保留空间检查。"
        return "待核对", "unknown", "未列入已知提醒规则，不能据此判断本次操作成功或无害。"
    if line.startswith("[TGCloud]"):
        return "信息", "", ""
    return "", "", ""


@dataclass
class OperationReport:
    """Bounded, per-operation evidence; no raw output, credentials or persistence."""
    edition: str
    label: str
    previous: str
    target: str
    action: str
    mode: str
    stage: str = "执行前复核"
    completed: bool = False
    interrupted: bool = False
    failure_stage: str = ""
    checks: dict[str, str] = field(default_factory=dict)
    alerts: set[str] = field(default_factory=set)
    readiness: dict[str, str] | None = None
    cli: dict | None = None
    log_rotation: dict[str, str] | None = None

    def observe_line(self, line: str) -> tuple[str, str, str]:
        result = classify_install_line(line, base=self.stage == "基础安装与升级")
        _, key, _ = result
        if key:
            self.alerts.add(key)
        return result

    def observe_base(self, code: int, output: str) -> None:
        for line in output.splitlines():
            self.observe_line(line)
        markers = {line for line in output.splitlines() if line.startswith("TGCLOUD_")}
        if "TGCLOUD_DATABASE_BACKUP=FAILED" in markers:
            self.checks["backup"] = "failed"
        elif code == 0 and "TGCLOUD_DATABASE_BACKUP=OK" in markers:
            self.checks["backup"] = "ok"
        health = {line.partition("=")[2] for line in markers if line.startswith("TGCLOUD_BOT_HEALTH=")}
        versions = {line.partition("=")[2] for line in markers if line.startswith("TGCLOUD_VERSION=")}
        code_states = {line.partition("=")[2] for line in markers if line.startswith("TGCLOUD_CODE=")}
        if code == 0 and health == {"healthy"}:
            self.checks["health"] = "ok"
        if code == 0 and code_states == {"OK"} and versions == {self.target}:
            self.checks["code"] = "ok"

    def render(self, log) -> None:
        labels = {"ok": "通过", "failed": "未通过", "unknown": "未核实", "not_run": "未执行", "na": "不适用"}
        def state(key):
            return labels[self.checks.get(key, "unknown")]
        title = "安装结果" if self.action == "install" else "升级结果" if self.action == "upgrade" else "维护结果"
        outcome = "操作中断；请核对当前状态，不假定已回退" if self.interrupted else (
            "执行未全部完成；请核对当前状态，不假定已回退" if self.failure_stage else
            "本次执行完成；不等于全部业务验收通过" if self.completed else "尚无完整执行结果")
        version = f"{self.label} · {self.previous or '未安装'} → {self.target}" if self.completed and not self.interrupted else (
            f"{self.label} · 原版本 {self.previous or '未安装'} · 目标 {self.target}（实际运行版本请复核）")
        lines = [f"\n── {title} ──\n", version, "流程结果：" + outcome]
        if self.failure_stage:
            lines.append("停止阶段：" + self.failure_stage)
        lines += ["基础安装与升级：" + state("base"), "原 .env 保留：" + state("config"),
                  "升级前数据库备份：" + state("backup"), "运行代码与版本：" + state("code"),
                  "Bot 容器健康：" + state("health"),
                  ("本机 HTTPS：" if self.mode == "https" else "本机内网入口：") + state("entry")]
        lines.append("Bot 实际版本：" + ("v" + self.target if self.checks.get("code") == "ok" else "未核实"))
        if self.cli is None:
            lines.append("本地管理工具：未核实；请使用本次安装入口更新管理工具")
        else:
            cli_version = "v" + self.cli["version"] if self.cli.get("version") else "未安装或未核实"
            lines.append("本地管理工具：" + cli_version + ("（与目标一致）" if self.cli.get("matches") else "（与目标不一致）"))
            command_state = {"ok": "版本查询、监听入口通过", "legacy": "旧版功能已核对；未提供的命令未记为通过"}
            lines.append("管理工具命令：" + command_state.get(self.cli.get("state"), "未通过，请更新管理工具"))
        if self.mode == "lan":
            lines.append("公网 HTTPS：不适用（内网模式）")
        known = self.alerts - {"unknown"}
        if known:
            confirmed = self.checks.get("base") == "ok"
            lines.append(("[提醒] " if confirmed else "[待核对] ") + f"{len(known)} 类常见提醒；"
                         + ("本次未阻断基础步骤，不代表后续验收均通过。" if confirmed else "基础步骤未确认通过，不能判作本次无害。"))
        if "unknown" in self.alerts:
            lines.append("[待核对] 有未识别的 WARNING，请核对上方脱敏原文。")
        from tgcloud.deployment.diagnostics import log_rotation_lines
        lines += ["\n── 容器日志轮转 ──\n", *log_rotation_lines(self.log_rotation)]
        lines += ["\n── 转存验收 ──\n"]
        if self.readiness is None:
            lines.append("业务就绪：未核实（无本次有效观察；可稍后在管理菜单重新检查）")
        else:
            ready = all(value == "ok" for value in self.readiness.values())
            lines.append("业务就绪：" + ("基础条件通过（最近观察，不代表写入）" if ready else "尚未全部确认（最近观察）"))
            from tgcloud.deployment.readiness import LABELS, STATES
            lines.extend("  " + LABELS[key] + "：" + STATES[value] for key, value in self.readiness.items())
        lines += ["WebDAV 写入与大小复验：" + state("webdav"), "实际 Telegram 转存：待人工验收",
                  ("公网访问：待外部网络验收" if self.mode == "https" else "局域网客户端访问：待其他设备验收"),
                  "\n下一步："]
        if self.checks.get("entry") != "ok":
            lines.append("先在“HTTPS 与管理”核对入口；不要卸载或删除数据来重试。")
        if self.readiness is None or not all(value == "ok" for value in self.readiness.values()):
            lines.append("在“业务就绪检查”查看近期连接、调度和容量；冷却中不重复探测。")
        if self.checks.get("webdav") != "ok":
            lines.append("先完成网关挂载和专用账号配置，再选择“WebDAV 验收”；需要确认，会创建并清理随机测试文件。")
        lines += ["向 Bot 转发一个自有小文件，查看任务结果，并在云存储官方客户端核对文件。",
                  "WebDAV 大小复验不等于哈希校验或云盘官方最终入库。", "本地管理：tgcloud"]
        log("\n".join(lines))


def numbered_menu(items) -> str:
    """Left-align both the number column and the label column, without tabs."""
    rows = [(str(number) + ".", label) for number, label in items]
    width = max((len(number) for number, _ in rows), default=0)
    return "\n".join(f"{number:<{width}} {label}" for number, label in rows)


def page_heading(title: str, product, version: str = "") -> str:
    context = "TGCloud" + (" · 版本 " + version if version else "")
    return f"\n── {title} ──\n\n{context}\n"


def terminal_report(markdown: str) -> str:
    """Only presentation changes; the saved diagnostic remains Markdown."""
    lines = markdown.splitlines()
    if lines and lines[0].startswith("# "):
        lines = lines[1:]  # The surrounding management page already has a title.
    return "\n".join(re.sub(r"^#{1,3} (.+)$", r"── \1 ──", line) for line in lines).lstrip("\n")


def friendly_marker(line: str) -> str:
    """Render protocol output without changing the command's returned bytes."""
    match = re.fullmatch(r"TGCLOUD_([A-Z0-9_]+)=(.*)", line)
    if match is None:
        return "[待核对] 收到未识别的检查结果，请查看诊断报告。"
    key, value = match.groups()
    if key == "STAGE":
        stage = {"GATEWAY": "启动 CloudDrive2 网关", "BOT": "构建并启动 TGCloud Bot",
                 "HEALTH": "检查 Bot 基础健康",
                 "HTTPS_ENV": "检查 HTTPS 部署环境", "ACME": "启动证书签发验证",
                 "CERTIFICATE": "申请或复用 HTTPS 证书", "HTTPS_VERIFY": "检查 HTTPS 管理入口"}.get(value)
        return "\n── " + stage + " ──" if stage else "[待核对] 安装阶段未识别，请查看诊断报告。"
    if key == "VERSION":
        return "程序版本：v" + value if re.fullmatch(r"\d+\.\d+\.\d+", value) else "程序版本：未核实"
    if key == "INSTALL_DIR":
        return "安装目录：" + value if re.fullmatch(r"/opt/[A-Za-z0-9._/-]+", value) else "安装目录：未核实"
    if key == "REPAIR_CONTAINER":
        return "正在核对 CloudDrive2 受管容器的网络。"
    if key == "STORAGE_GATEWAY":
        return "存储网关：CloudDrive2" if value == "clouddrive2" else "存储网关：未核实"
    if key == "TEST_PATH":
        return "随机测试文件验收流程结束。"
    if key == "CLEANUP_WARNING":
        return "[待核对] 临时测试文件清理未确认，请在授权目标目录检查残留。"
    if key == "RESULT" and value == "SUCCESS":
        return "基础部署已完成；继续检查管理入口与业务状态。"
    if key == "LAN_BASE" and value == "OK；TGCLOUD_LAN_ENTRY=OK；HTTPS=NOT_APPLICABLE":
        return "基础服务与内网入口检查通过；公网 HTTPS 不适用。"
    labels = {
        "CODE": "运行代码校验", "CONFIG": "配置检查", "DATABASE_CHECK": "数据库检查",
        "DATABASE_BACKUP": "数据库备份", "BOT_HEALTH": "Bot 基础健康", "RESULT": "基础部署",
        "REPAIR": "CloudDrive2 网络检查", "STATUS": "服务状态", "UPDATE": "程序更新",
        "APPLY_CONFIG": "配置应用", "BACKUPS": "备份检查", "BACKUP_PRUNE": "备份清理",
        "CONFIGURATION": "配置检查", "DESTINATION": "WebDAV 写入验收",
        "CLI_CHECK": "安装只读预检", "LAN_CHECK": "内网只读预检",
        "PROXY_STATUS_SCRIPT": "HTTPS 状态脚本", "LAN_ENTRY": "内网入口检查",
        "WEBDAV_AUTH": "WebDAV 账号认证", "WEBDAV_LIST": "WebDAV 目标目录访问",
        "WEBDAV": "WebDAV 账号与目录检查", "WEBDAV_WRITE": "WebDAV 测试文件写入",
        "WEBDAV_SIZE": "WebDAV 临时文件大小复验", "UPLOAD": "文件上传与大小复验",
        "WEBDAV_MOVE": "WebDAV 改名与最终文件复验", "RENAME": "文件改名检查",
        "WEBDAV_DELETE": "WebDAV 测试文件删除检查", "DELETE": "临时文件清理",
    }
    label = labels.get(key)
    status = re.split(r"[；:：]", value, maxsplit=1)[0]
    states = {
        "OK": "通过", "SUCCESS": "通过", "FAILED": "未通过", "MISMATCH": "未通过（与预期不一致）",
        "MISMATCH_KEYS": "未通过（配置字段不一致）", "healthy": "通过", "unhealthy": "未通过",
        "starting": "启动中", "exited": "已停止", "missing": "未找到", "STOPPED": "已停止",
        "UNKNOWN": "未核实", "unknown": "未核实", "PRESERVED": "已保留原配置",
        "APPLY_REQUESTED": "已请求应用新配置",
    }
    if label is None or status not in states:
        return "[待核对] 收到未识别的检查结果，请查看诊断报告。"
    prefix = "[错误] " if status in {"FAILED", "MISMATCH", "MISMATCH_KEYS", "unhealthy"} else ""
    suffix = "（不代表部署或转存验收）" if key in {"CLI_CHECK", "LAN_CHECK"} else ""
    if key == "DESTINATION" and status == "FAILED" and ":" in value:
        suffix += "；" + value.partition(":")[2].strip()
    return prefix + label + "：" + states[status] + suffix


class InstallerLog:
    def __init__(self, log: SafeLog, *, friendly: bool = True) -> None:
        self.log = log
        self.friendly = friendly
        self.operation: OperationReport | None = None
        self.cli: dict | None = None

    def register(self, config) -> None:
        self.log.register(config)

    def clean(self, text: str) -> str:
        return self.log.clean(text)

    def __call__(self, text: str) -> None:
        # Never alter session.run's returned output: machine completion markers
        # remain mandatory even when the interactive presentation is Chinese.
        lines = []
        for line in text.splitlines():
            if self.operation is not None:
                previous_alerts = self.operation.alerts.copy()
                kind, key, explanation = self.operation.observe_line(line)
                if not line.startswith("TGCLOUD_") and kind:
                    line = "[" + kind + "] " + line + ("\n       " + explanation if explanation and key not in previous_alerts else "")
            if not self.friendly:
                lines.append(line)
                continue
            if line.startswith("正式 Release："):
                line = line.split("；", 1)[0].replace("正式 Release：", "正式稳定版本：")
            elif line.startswith("TGCLOUD_CLI_RESULT=BASE_HTTPS_OK"):
                line = "基础安装与 HTTPS 检查通过；WebDAV 和真实转存以单独验收为准。\n下次管理请运行：tgcloud"
            elif line.startswith("TGCLOUD_LAN_RESULT=BASE_LAN_OK"):
                line = "基础安装与内网入口检查通过；WebDAV 和真实转存以单独验收为准。\n下次管理请运行：tgcloud"
            elif re.match(r"TGCLOUD_(?:CLI|LAN)_RESULT=", line):
                label, _, detail = line.partition("；")
                translations = {"NO_CHANGE": "当前无需变更", "CANCELLED": "已取消", "INTERRUPTED": "操作中断"}
                key = label.split("=", 1)[1]
                failure = label.replace("TGCLOUD_CLI_RESULT=FAILED：", "操作失败：").replace("TGCLOUD_LAN_RESULT=FAILED：", "操作失败：")
                line = translations.get(key, failure if key.startswith("FAILED：") else "[待核对] 操作结果未识别，请查看诊断报告。") + ("；" + detail if detail else "")
                if key.startswith("FAILED"):
                    line = "[错误] " + line
            elif line.startswith("TGCLOUD_CLI_WEBDAV=NOT_RUN"):
                line = "WebDAV 验收已跳过，可稍后运行 tgcloud 进行验收。"
            elif line.startswith("TGCLOUD_CLI_WEBDAV=OK"):
                line = "WebDAV 写入与大小复验通过；请在云存储官方客户端确认最终文件。"
            elif line.startswith("TGCLOUD_CLI_BASE_HTTPS=OK"):
                line = "HTTPS 管理入口本机检查通过；公网访问仍需外部网络检查。"
            elif line.startswith("TGCLOUD_"):
                line = friendly_marker(line)
            lines.append(line)
        self.log("\n".join(lines))


def edition_menu(ui, log, *, default: str = "1", both: bool = False, title: str = "安装向导") -> str:
    # Kept for old callers; the single product never presents a chooser.
    return "1"
