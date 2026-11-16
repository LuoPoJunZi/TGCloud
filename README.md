<p align="center"><img src="assets/brand/tgcloud-logo.svg" width="560" alt="TGCloud"></p>

# TGCloud

将 Telegram 文件转存到自己挂载的云存储。当前版本 **0.0.1（本地整理版，未发布）**，仅支持 CloudDrive2。

Telegram → 私人 Bot / 已授权监听 → rclone → CloudDrive2 WebDAV → 你的云存储

## 开始使用

将完整源码放到 Linux 主机的独立目录，在源码目录选择所需入口：

```bash
sudo bash install.sh       # 公网 HTTPS
sudo bash install-lan.sh   # 可信内网
```

直接进入 TGCloud 的 CloudDrive2 管理菜单，无需选择版本。当前从完整本地源码安装；尚未发布在线安装脚本、Release 或 Bot 镜像。已有其他项目实例不会自动迁移或接管。

独立 Compose 从当前源码生成唯一的 CloudDrive2 部署包：

```bash
python3 -B scripts/build_compose.py --output /absolute/new-compose-output
```

步骤见[安装指南](docs/install.md)、[Compose 指南](docs/compose-guide.md)和[文档目录](docs/README.md)。安装后的本地管理命令为 `tgcloud`。

完成安装后查看 [Bot 与任务](docs/bot-guide.md)及[存储配置](docs/configuration-guide.md)。

## 保留的能力

- 持久队列、去重、失败保留、取消及重启恢复。
- 普通下载、大文件流式传输、背压和磁盘保护。
- 私聊鉴权、管理员监听、来源访问检查和用户隔离。
- 公网 HTTPS、可信内网、独立 Compose、备份与只读附加入口。

CloudDrive2 接收和大小复验不等于云盘官方最终入库，也不等于内容哈希校验。仅转存你有权保存的内容；账号授权在自己的网关配置，不公开凭据、Session、数据库或备份。

## 项目与历史

新项目地址为 [LuoPoJunZi/TGCloud](https://github.com/LuoPoJunZi/TGCloud)。当前仓库和文档使用 TGCloud 名称；新仓库上线、CI、发布和实机验收均须另行确认。

旧项目教程、发布事实和验收记录保存在[历史档案](archive/README.md)。TGCloud 0.0.1 的本地完整回归检查现已通过；平台条件项和验证范围见[开发说明](docs/development.md)。历史验收不能代替本项目实机验收。

本项目承接原 [TG2Cloud](https://github.com/LuoPoJunZi/TG2Cloud) 的 CloudDrive2 实现，保留 [whyhhh20/TG115](https://github.com/whyhhh20/TG115) 上游致谢、原版权与 [MIT 许可证](LICENSE)。感谢 Telethon、rclone、Docker 与 CloudDrive2。

[English](README_EN.md) · [更新记录](CHANGELOG.md) · [开发说明](docs/development.md)
