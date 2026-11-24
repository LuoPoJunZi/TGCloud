<h1 align="center">TGCloud</h1>

<p align="center"><strong>Telegram 文件自动转存 · CloudDrive2 · Linux 一键部署</strong></p>
<p align="center">
  <a href="https://github.com/LuoPoJunZi/TGCloud/releases/latest"><img alt="最新版本" src="https://img.shields.io/github/v/release/LuoPoJunZi/TGCloud?style=flat-square&amp;label=release&amp;color=1677ff"></a>
  <a href="https://github.com/LuoPoJunZi/TGCloud/actions/workflows/tests.yml"><img alt="自动测试" src="https://img.shields.io/github/actions/workflow/status/LuoPoJunZi/TGCloud/tests.yml?branch=main&amp;style=flat-square&amp;label=tests"></a>
  <a href="LICENSE"><img alt="MIT License" src="https://img.shields.io/github/license/LuoPoJunZi/TGCloud?style=flat-square&amp;color=22a06b"></a>
</p>
<p align="center">
  <a href="docs/install.md"><strong>安装指南</strong></a> ·
  <a href="docs/compose-guide.md">Compose 部署</a> ·
  <a href="docs/README.md">使用文档</a> ·
  <a href="https://github.com/LuoPoJunZi/TGCloud/releases">版本下载</a> ·
  <a href="README_EN.md">English</a>
</p>

TGCloud 是面向 CloudDrive2 的自托管 Telegram 文件自动化工具。把文件发送或转发给自己的 Bot，TGCloud 负责排队、下载和命名，再写入 CloudDrive2 挂载的云存储。

```text
Telegram → TGCloud → rclone → CloudDrive2 WebDAV → 云存储
```

当前稳定版为 **[v0.0.10](https://github.com/LuoPoJunZi/TGCloud/releases/tag/v0.0.10)**。源码包（含原生 AMD64／ARM64 的 `tgcloud-host` 宿主管理工具）、Compose 部署包和校验文件由 GitHub Actions 验证后自动生成；Bot 镜像在部署主机本地构建。

## 主要能力

- 持久化任务队列、文件去重、取消与失败恢复，重启后保留任务状态。
- 根据文件名、说明文字和规则分类、命名，支持命名预览。
- 普通下载和大文件流式转存，结合磁盘余量、CPU 和内存控制资源使用。
- 管理员与成员权限、用户配额，以及显式授权的频道／群组监听、筛选和转存审批。
- 公网 HTTPS、可信内网和独立 Compose 部署，提供本地管理菜单、日志、诊断与备份。
- WebDAV 写入验收，以及可选的外部只读访问。

## 开始使用

1. 准备 Telegram API ID、API Hash、Bot Token 和管理员数字用户 ID，详见[安装指南](docs/install.md)。
2. 选择一键脚本或 Docker Compose，在私有终端填写配置，核对计划后确认。
3. 登录自己的 CloudDrive2 管理页，挂载云存储并创建与向导配置一致的专用 WebDAV 用户。
4. 执行“业务就绪检查”和“WebDAV 验收”，再向 Bot 发送一个自有小文件，用 `/queue` 或 `/tasks` 查看结果。
5. 到云存储官方客户端核对文件，再按需启用监听、成员授权和外部访问。

## 快速安装

要求原生 AMD64／ARM64 的 64 位 Debian／Ubuntu Linux、root 交互终端、Python 3.10+、curl、CA 证书和 `/dev/fuse`。主机需能访问 GitHub、Telegram、软件源和镜像仓库。Bot 镜像使用 Python 3.12；Windows 和 macOS 不作为生产部署平台。

### 公网 VPS

准备解析到 VPS 的域名，开放 TCP 80/443。在 root 终端执行，普通用户先运行 `sudo -i`：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)
```

入口自动下载并校验 `v0.0.10` 正式源码，直接进入安装／管理向导，无需安装 Git、输入源码目录或选择版本。资源预算依据本次探测结果推荐，执行前会再次检查。默认安装目录为 `/opt/tgcloud`；安装后运行 `tgcloud` 管理。

### 可信内网

没有公网域名时，在同一入口追加 `--lan`：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh) --lan
```

按提示填写本机可信内网地址。HTTP 入口只适用于本机或可信内网，不应做公网端口转发。已有完整源码也可运行 `sudo bash install.sh` 或 `sudo bash install.sh --lan`。详细步骤见[安装与升级](docs/install.md)。

## Docker Compose 部署

一键脚本和独立 Compose 选择一种。先准备 Docker Engine、Compose v2、FUSE、Python 3.10+、curl、tar 和 sha256sum，在 Linux root 终端执行：

```bash
mkdir -m 700 /root/TGCloud-Compose && \
cd /root/TGCloud-Compose && \
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.10/TGCloud-v0.0.10-compose.tar.gz && \
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.10/COMPOSE-SHA256SUMS.txt && \
sha256sum -c COMPOSE-SHA256SUMS.txt && \
tar --no-same-owner -xzf TGCloud-v0.0.10-compose.tar.gz --strip-components=1
```

命令下载 `v0.0.10` 正式部署包和对应的 SHA256 校验文件。部署目录固定为 `/root/TGCloud-Compose`，首次运行前须尚不存在；安装文件直接位于该目录，不再增加 `deployment/` 子目录。

解压成功后，在同一目录运行 `bash manage.sh` 进入初始化菜单。

首次运行 `manage.sh` 后，按顺序完成：初始化配置 → 启动网关 → 在后台挂载云存储并配置专用 WebDAV 用户 → 启动 Bot → 验收转存。Compose 初始化的预算默认值按部署磁盘计算，不代替完整宿主资源检查。

后续管理命令：

```bash
cd /root/TGCloud-Compose
bash manage.sh
```

分步 Docker 命令、SSH 隧道、可选 HTTPS、日志、备份和卸载见 [Compose 部署教程](docs/compose-guide.md)。已有实例请按教程中的升级步骤操作。

## 使用文档

| 主题 | 文档 |
| --- | --- |
| 安装与部署 | [一键安装与升级](docs/install.md) · [Docker Compose](docs/compose-guide.md) |
| Bot 与任务 | [Bot 使用、队列和恢复](docs/bot-guide.md) · [分类与命名](docs/naming-guide.md) |
| 监听与权限 | [频道／群组监听和筛选](docs/listening-guide.md) · [配置参数](docs/configuration-guide.md) |
| 网络与访问 | [HTTPS 与 WebDAV](docs/network-guide.md) · [安全说明](docs/security.md) |
| 日常维护 | [状态、日志与卸载](docs/operations-guide.md) · [备份与恢复](docs/backup-and-recovery.md) |
| 问题与开发 | [常见问题](docs/faq.md) · [开发、架构与测试](docs/development.md) |
| 版本记录 | [更新记录](CHANGELOG.md) · [本版发布说明](RELEASE_NOTES.md) |

完整目录见 [docs](docs/README.md)。配置保存在私有 `.env` 中；Base64 是编码，不是加密。不要将 Token、密码、Session、数据库或备份提交到仓库。

## 常见问题

**需要先克隆源码吗？** 在线安装不需要；脚本自动获取并校验正式源码包。

**Bot 在线但不能写入网盘？** 检查 CloudDrive2 挂载、专用 WebDAV 用户和目标目录，再运行菜单中的就绪检查与写入验收。

**推荐预算是固定的 8GB／20GB 吗？** 不是。向导按实际磁盘容量与余量计算预算和保留空间；一键安装还会参考 CPU、内存判断推荐方案。预算用于任务暂存，不限制网关全部缓存。

**验收通过就代表文件完全入库吗？** WebDAV 上传及大小复验不等于内容哈希校验或云盘官方最终入库，重要文件仍需在官方客户端核对。

## 致谢与许可

感谢 [whyhhh20/TG115](https://github.com/whyhhh20/TG115) 的开源贡献。源码保留 `TG115 Contributors` 的原版权声明，采用 [MIT License](LICENSE)。

感谢 Telethon、rclone、Docker、Nginx、Certbot 等项目提供基础能力。依赖与发行组件继续适用各自附带的许可证；CloudDrive2 使用条件以其官方条款为准。本项目与 Telegram、CloudDrive2 及云存储服务商没有官方隶属关系。
