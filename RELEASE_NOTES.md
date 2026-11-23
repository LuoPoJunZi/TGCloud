## TGCloud v0.0.8

本版本重点完成全仓安全与稳定性治理，并将 `tgcloud` 本地管理菜单重构为结构清晰的 5 组两级菜单。

- 加固 Go 宿主工具与 Python／Shell 运行时边界：修复 APT 密钥目录权限、SQLite 回滚旁文件清理顺序、外部 Nginx WebDAV 凭据吊销同步、任务取消状态不变量、WebDAV 429／401 冷却与凭据脱敏。
- 将 `tgcloud` 主菜单收敛为“服务运行管理、访问入口管理、业务功能设置、健康检查诊断、实例维护备份”5 组两级菜单，统一各级子菜单对齐宽度与“返回上级”导航。
- 拆分 `hostcli/internal/{hostutil,storage,redact}`、`src/tgcloud/deployment/ops/` 与分组测试目录，提升可维护性并补齐回归测试。

## 安装与升级

公网一键安装运行 `bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)`。可信内网在命令末尾追加 `--lan`。已有实例按安装向导或 `tgcloud` 管理菜单升级。

Compose 下载、校验和初始化命令见 [Compose 部署教程](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.8/docs/compose-guide.md)。一键安装和升级步骤见 [安装指南](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.8/docs/install.md)。

## 发布附件

GitHub Actions 从同一个版本标签生成源码包、Compose 部署包和各自的 SHA256 校验文件。源码包随附 Linux AMD64 和 ARM64 的 Go 宿主工具。
