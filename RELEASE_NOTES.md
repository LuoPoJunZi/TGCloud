## TGCloud v0.0.9

本版本完成 Go 宿主工具与 Python 容器业务的三阶段架构分工优化：宿主机运维与原生管理菜单全面由静态编译的 Go 工具承担，容器内 Bot 业务继续由 Python 提供。

- **Go 宿主包重构与职责收敛**：将 `hostcli/internal/` 清晰拆分为 `hostutil`、`storage`、`redact`、`deps`、`report`、`acceptance`、`inspect`、`uninstall`、`backupops`、`webdav`、`tui`，仅依赖 Go 标准库，双架构静态编译体积均控制在 8 MiB 以内。
- **剩余宿主机运维全面纳入 Go**：新增 `tgcloud-host uninstall`（fail-closed 卸载预检与执行）、`backups`（实例备份清单及与 Python 兼容的代理 tar/manifest/SHA256 备份、恢复演练与清理）与 `webdav-entry`（外部 WebDAV 状态核对、`0640` 认证文件原子写入与容器挂载摘要核验），管理入口优先走 Go。
- **原生 Go 5 分组交互菜单与诊断**：新增 `tgcloud-host menu`（以及 `tgcloud --go-menu`）与 `diagnostics`，提供与原菜单一致的 5 组两级交互与脱敏诊断报告；用户授权、监听、分类与通知等业务配置通过 `docker exec` 调用 Bot 容器内协议，实现宿主机零 Python 业务依赖。

## 安装与升级

公网一键安装运行 `bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)`。可信内网在命令末尾追加 `--lan`。已有实例按安装向导或 `tgcloud` 管理菜单升级。

Compose 下载、校验和初始化命令见 [Compose 部署教程](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.9/docs/compose-guide.md)。一键安装和升级步骤见 [安装指南](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.9/docs/install.md)。

## 发布附件

GitHub Actions 从同一个版本标签生成源码包、Compose 部署包和各自的 SHA256 校验文件。源码包随附 Linux AMD64 和 ARM64 的 Go 宿主工具。
