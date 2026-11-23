## TGCloud v0.0.7

公网和内网一键安装及升级优先使用源码包中的 Go 宿主工具。现有安装向导继续负责交互，宿主工具不可用时可沿用原 Python 流程。

- 迁移宿主资源与 Docker 预检、源码暂存、候选镜像检查、部署编排和管理命令发布。
- 升级涉及配置备份、SQLite 一致性快照、Bot 健康检查和失败回滚。
- 公网 HTTPS 和内网 HTTP 管理入口支持独立检查和修复。独立 Docker Compose 继续沿用原有流程。

## 安装与升级

公网一键安装运行 `bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)`。可信内网在命令末尾追加 `--lan`。已有实例按安装向导或 `tgcloud` 管理菜单升级。

Compose 下载、校验和初始化命令见 [Compose 部署教程](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.7/docs/compose-guide.md)。一键安装和升级步骤见 [安装指南](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.7/docs/install.md)。

## 发布附件

GitHub Actions 从同一个版本标签生成源码包、Compose 部署包和各自的 SHA256 校验文件。源码包随附 Linux AMD64 和 ARM64 的 Go 宿主工具。
