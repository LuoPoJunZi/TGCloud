## TGCloud v0.0.10

本版本完善 Go 宿主管理菜单、实例快照和证书演练，容器内 Bot 继续使用 Python。

- Go 管理菜单接入公网 HTTPS、证书续期演练、Bot 成员配额、实例快照和代理备份。已有 v2/v3 HTTPS 代理继续使用 Python 管理菜单。
- 实例快照核对实例版本，并使用 SQLite 一致性备份。快照预检和代理恢复演练收到中断信号后，会等待子进程退出并清理临时解包目录。
- Certbot 续期演练按精确容器名查询，核对任务标签后清理容器，并再次确认容器已删除。

## 安装与升级

公网一键安装运行 `bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)`。可信内网在命令末尾追加 `--lan`。已有实例按安装向导或 `tgcloud` 管理菜单升级。

Compose 下载、校验和初始化命令见 [Compose 部署教程](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.10/docs/compose-guide.md)。一键安装和升级步骤见 [安装指南](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.10/docs/install.md)。

## 发布附件

GitHub Actions 从同一个版本标签生成源码包、Compose 部署包和各自的 SHA256 校验文件。源码包随附 Linux AMD64 和 ARM64 的 Go 宿主工具。
