## TGCloud v0.1.0

本版本让已安装的外部 WebDAV 维护工具随主实例更新，并统一 Go 宿主工具与 TGCloud 的版本标识。

- 公网和内网安装、升级成功后，自动同步已安装且与实例匹配的外部 WebDAV 维护工具，保留原配置和启用状态。
- Bot 回滚后，管理菜单可打开与实际实例同源的已核验 WebDAV 工具。候选在切换启动器前完成完整性检查，同版本文档更新也可同步来源记录。
- 公网和内网结果汇总分别显示附加同步和基础升级的结果。按 Ctrl+C 会显示实际停止阶段「附加工具同步」。
- Go 宿主工具新增 tgcloud-host --version，管理菜单区分工具版本和实例版本，个人监听状态也显示当前组件版本。

## 安装与升级

公网一键安装运行 `bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)`。可信内网在命令末尾追加 `--lan`。已有实例按安装向导或 `tgcloud` 管理菜单升级。

Compose 下载、校验和初始化命令见 [Compose 部署教程](https://github.com/LuoPoJunZi/TGCloud/blob/v0.1.0/docs/compose-guide.md)。一键安装和升级步骤见 [安装指南](https://github.com/LuoPoJunZi/TGCloud/blob/v0.1.0/docs/install.md)。

## 发布附件

GitHub Actions 从同一个版本标签生成源码包、Compose 部署包和各自的 SHA256 校验文件。源码包随附 Linux AMD64 和 ARM64 的 Go 宿主工具。
