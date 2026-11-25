## TGCloud v0.1.1

本版本优化本地管理菜单界面展示与全流程中文反馈，并完善独立 Compose 部署包下载后的目录清理命令。

- 简化 Go 原生宿主菜单、Python 管理菜单和 Compose 管理菜单标题样式，主菜单统一显示 `TGCloud版本`、`Bot  系统状态` 与 `安装目录`，去除各级标题中的冗余后缀。
- 将管理菜单中的 `TGCLOUD_*` 协议标记（含 WebDAV 写入验收全部步骤、证书续期演练、代理检查及卸载预检与结果）和英文容器／状态字段统一转换为直观的中文说明，同时保留底层命令行协议兼容。
- 更新文档中的 Compose 部署包下载与校验命令，解压完成后自动清理根目录下的归档包与校验文件，避免初始化目录检查拦截。

## 安装与升级

公网一键安装运行 `bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)`。可信内网在命令末尾追加 `--lan`。已有实例按安装向导或 `tgcloud` 管理菜单升级。

Compose 下载、校验和初始化命令见 [Compose 部署教程](https://github.com/LuoPoJunZi/TGCloud/blob/v0.1.1/docs/compose-guide.md)。一键安装和升级步骤见 [安装指南](https://github.com/LuoPoJunZi/TGCloud/blob/v0.1.1/docs/install.md)。

## 发布附件

GitHub Actions 从同一个版本标签生成源码包、Compose 部署包和各自的 SHA256 校验文件。源码包随附 Linux AMD64 和 ARM64 的 Go 宿主工具。
