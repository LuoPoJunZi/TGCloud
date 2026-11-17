# TGCloud

TGCloud 是面向 **CloudDrive2** 的 Telegram 文件自动化转存与管理工具。将文件发送给 Bot，或授权监听指定频道、群组，再通过 CloudDrive2 的 WebDAV 接口写入挂载的云存储。

当前版本为 **0.0.2**。[版本下载](https://github.com/LuoPoJunZi/TGCloud/releases/tag/v0.0.2)提供完整源码包和 Docker Compose 部署包及 SHA256 校验文件；Bot 镜像在部署主机本地构建。

## 核心功能

- 文件下载、队列调度、任务状态持久化与失败恢复，任务持久化到 SQLite。
- 按规则分类和命名，结合原文件名与说明文字识别标题，提供本地命名预览。
- 本地暂存与大文件流式传输，按 CPU、内存、磁盘余量调整传输窗口。
- 管理员与成员权限、独立任务视图和成员配额。
- 显式授权的频道／群组监听，来源筛选、目录与命名规则、转存计划确认。
- CloudDrive2 WebDAV 验证、HTTPS 管理入口、可选外部只读访问。
- 服务管理、脱敏日志、诊断报告、实例快照与隔离恢复演练。

写入成功和远端大小复验不代表内容哈希一致或云盘官方最终入库；重要文件请在云盘官方客户端核对。

## 运行环境

受管部署支持原生 64 位 Debian／Ubuntu 的 AMD64 和 ARM64，使用 Docker Engine、Docker Compose v2；CloudDrive2 挂载需要 `/dev/fuse`。安装与宿主管理入口需要 Python 3.10+，Bot 镜像使用 Python 3.12。

需要 Telegram API ID、API Hash、Bot Token、管理员用户 ID，以及已挂载云存储的 CloudDrive2 和专用 WebDAV 用户。Windows 和 macOS 可编辑源码、运行适用的测试；生产部署使用 Linux。

## 一键启动安装向导

在目标 Linux 主机的 **root 终端**执行（普通用户先运行 `sudo -i`）。需要 curl、Python 3.10+ 和 CA 证书：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)
```

在线入口自动下载并校验 `v0.0.2` 正式源码，直接进入安装／管理向导，无需 Git 或手动填写源码路径。仅支持 CloudDrive2，收集配置、展示计划后再确认安装。已安装后使用 `tgcloud` 管理。

没有公网域名、使用可信内网时：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh) --lan
```

已有完整源码仍可运行 `sudo bash install.sh` 或 `sudo bash install-lan.sh`。

完整步骤见[安装指南](docs/install.md)。默认实例目录为 `/opt/tgcloud`，请勿将其放进云挂载目录。

## Docker Compose

脚本安装与独立 Compose 选择一种部署方式。在已准备 Docker Compose v2 的 Linux root 终端下载并初始化部署包：

```bash
mkdir -m 700 TGCloud-compose-0.0.2 && cd TGCloud-compose-0.0.2 &&
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.2/TGCloud-v0.0.2-compose.tar.gz &&
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.2/COMPOSE-SHA256SUMS.txt &&
sha256sum -c COMPOSE-SHA256SUMS.txt &&
mkdir deployment &&
tar --no-same-owner -xzf TGCloud-v0.0.2-compose.tar.gz --strip-components=1 -C deployment &&
cd deployment && bash manage.sh
```

目录须尚不存在。Bot 镜像在本机构建，初始化后先完成 CloudDrive2 挂载与 WebDAV 配置，再启动 Bot。环境检查、分步命令、首次转存、日志和备份见 [Compose 部署教程](docs/compose-guide.md)。

## 基础配置与首次使用

1. 在 Telegram 创建 Bot，并准备自己的 API ID、API Hash 和用户 ID。
2. 通过安装向导填写身份、WebDAV 地址、账号、目标目录和暂存预算。
3. 登录 CloudDrive2，完成云挂载并启用专用 WebDAV 用户。
4. 从管理菜单执行“业务就绪检查”和“WebDAV 验收”。
5. 私聊 Bot，发送一个小文件，用 `/queue` 或 `/tasks` 查看结果，再到云盘客户端核对。

配置由向导写入私有 `.env`。Base64 是编码，不是加密；数据库、Session、密码和备份不应进入公共仓库。

## 文档导航

- [完整文档索引](docs/README.md)
- [安装部署](docs/install.md) · [Docker Compose](docs/compose-guide.md) · [配置参考](docs/configuration-guide.md)
- [Bot 与任务](docs/bot-guide.md) · [分类与智能命名](docs/naming-guide.md) · [监听与筛选](docs/listening-guide.md)
- [HTTPS 与 WebDAV](docs/network-guide.md) · [维护与卸载](docs/operations-guide.md)
- [备份与恢复](docs/backup-and-recovery.md) · [安全说明](docs/security.md) · [开发与架构](docs/development.md)

## 常见问题

**一键入口需要先下载源码吗？** 不需要。通过在线命令运行 `install.sh` 时自动下载并校验正式源码包；在完整源码目录运行时直接使用本地资源。

**Bot 在线但任务不能上传？** 先检查 CloudDrive2 挂载、专用用户、目标目录和磁盘余量，再运行管理菜单中的就绪检查与 WebDAV 验收。

**大文件超过暂存预算怎么办？** 可申请流式传输，但仍受资源窗口、来源访问权限与目的端状态限制。更多问题见 [FAQ](docs/faq.md)。

## 开源许可

源码采用 [MIT 许可证](LICENSE)。第三方组件、原版权及其许可要求见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。CloudDrive2 使用条件以其官方条款为准。
