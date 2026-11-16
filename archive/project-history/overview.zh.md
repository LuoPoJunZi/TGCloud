> 历史档案：以下为原项目记录，不是 TGCloud 0.0.1 的当前教程或验收结论。[归档说明](../README.md)。

# CloudDrive2 本地精简版

当前版本 **0.0.1**，仅保留 CloudDrive2，安装和管理直接进入 CloudDrive2，不再选择 Edition。新 GitHub 仓库名称尚未确定，尚未配置在线安装、Release 或镜像发布。

将完整源码复制到 Linux 后，在源码目录运行 `sudo bash install.sh`（公网 HTTPS）或 `sudo bash install-lan.sh`（可信内网）。独立 Compose 也只生成一个 CloudDrive2 部署包，步骤见[本地安装指南](manuals/local-clouddrive2.md)和 [Compose 当前流程](manuals/compose.md)。默认不接管旧 TG2Cloud、TG115、OpenList 或未知实例；发现身份或路径冲突时保留现场并停止。

持久队列、去重、监听、重启恢复、流式背压和磁盘保护继续保留。WebDAV 接收及大小复验不代表云盘官方最终入库或内容哈希验证。

## 原 TG2Cloud 项目说明（历史保留）

以下在线命令、双 Edition 教程、版本与验收结果属于原项目，保留用于来源、致谢和历史书签，不适用于当前本地精简版。

<p align="center">
  <img src="assets/brand/tg2cloud-logo.svg" alt="TG2Cloud — From Telegram to Your Cloud" width="520">
</p>

<p align="center">
  <strong>Telegram 文件自动转存 · CloudDrive2 / OpenList · Linux 一键部署</strong>
</p>

<p align="center">
  <a href="https://github.com/LuoPoJunZi/TG2Cloud/releases/latest"><img alt="最新版本" src="https://img.shields.io/github/v/release/LuoPoJunZi/TG2Cloud?style=flat-square&amp;label=release&amp;color=1677ff"></a>
  <a href="https://github.com/LuoPoJunZi/TG2Cloud/actions/workflows/tests.yml"><img alt="自动测试" src="https://img.shields.io/github/actions/workflow/status/LuoPoJunZi/TG2Cloud/tests.yml?branch=main&amp;style=flat-square&amp;label=tests"></a>
  <a href="LICENSE"><img alt="MIT License" src="https://img.shields.io/github/license/LuoPoJunZi/TG2Cloud?style=flat-square&amp;color=22a06b"></a>
</p>

<p align="center">
  <a href="docs/VPS-INSTALL.md"><strong>安装指南</strong></a> ·
  <a href="docs/README.md">仓库文档</a> ·
  <a href="https://github.com/LuoPoJunZi/TG2Cloud/releases">版本下载</a> ·
  <a href="https://tg2cloud.pages.dev/">文档网站</a> ·
  <a href="README_EN.md">English</a>
</p>

TG2Cloud 是一个自托管的个人文件转存工具：把文件发送或转发给自己的 Telegram Bot，由 Linux 主机自动排队、下载并写入你挂载的云存储。

Telegram → TG2Cloud → rclone → CloudDrive2 / OpenList WebDAV → 你的云存储

[管理员监听与转存计划](manuals/listening.md)默认关闭，可按来源配置筛选、命名和保存目录，并管理审批及失败恢复。

## 主要能力

- 两个 Edition 共用 Bot 核心，各自保留配置、任务数据库与 Session。
- 持久队列、去重、取消恢复、失败保留及视频说明命名。
- 普通下载与大文件流式传输，配合动态并发和磁盘保护。
- 公网 HTTPS / 可信内网部署，提供磁盘预算推荐和保留配置升级。

当前稳定版 **[v1.5.9](https://github.com/LuoPoJunZi/TG2Cloud/releases/tag/v1.5.9)**，支持 Linux；发布与兼容范围见[本版记录](releases/release-1.5.9.md)。

v1.5.9：下载停滞保护与排队诊断；[CloudDrive2 升级与实际转存验收](releases/release-1.5.9.md)。

## 开始使用

1. 按[填写信息清单](manuals/setup-checklist.md)准备 Telegram Bot、API 信息和服务器环境；不要把真实凭据填写到公共文档。
2. 选择 CloudDrive2 或 OpenList，以及公网 VPS 或可信内网入口，按向导核对安装计划后执行。
3. 在自己的网关中挂载云存储，按安装指南完成 WebDAV 配置与验收；网盘账号授权由你自行管理。
4. 向 Bot 发送或转发文件，查看接收结果；任务查询、失败恢复及验证含义见[任务使用与恢复指南](manuals/recovery.md)。

两个 Edition 共用转存能力，但网关配置不同；各自的实例数据互相独立。已有实例升级使用对应入口并保留配置，不需要先卸载。

公网／内网入口选择 Edition 后，新装进入向导，已有实例进入管理菜单；确认安装管理工具后，可直接运行 `tg2cloud`。卸载仅作用于所选 Edition；v1.4.7 源码退役独立 Shell 入口，保留统一菜单与子命令预检。菜单、更新与清理边界见[运维指南](manuals/operations.md#本地管理菜单)。

外部 WebDAV 提供手机只读访问；[统一管理说明](manuals/external-webdav.md#统一管理)介绍 v1.4.7 的 `tg2cloud` → **15. 外部 WebDAV**，安装与维护均在子菜单完成，无需另跑安装命令。旧工具须分别更新；外部 WebDAV 选择子菜单 8 升级。

## 快速安装

要求 Debian / Ubuntu、64 位原生用户态、root、Python 3.10+、curl 和可交互终端；主机需能访问 GitHub、Telegram、镜像仓库及目的端。CloudDrive2 还需要 `/dev/fuse`。

Linux 的系统要求、公网 NAT 条件与平台限制见[安装指南](manuals/vps-install.md)和[Linux 兼容说明](manuals/architectures.md)。

### 公网 VPS

准备指向 VPS 的域名，并允许 TCP 80/443；向导中选择 CloudDrive2 或 OpenList：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TG2Cloud/main/install.sh)
```

脚本先收集信息、检查并等待确认，业务代码只安装正式稳定 Release。已有实例建议先追加 `--edition clouddrive2 --check`（OpenList 使用 `openlist`）；只读检查不执行升级。核对后重新运行不带参数的入口，选择 Edition 和“安装 / 升级”并确认，可同时获得离线管理命令；显式参数仍兼容。完整参数见 [VPS 指南](manuals/vps-install.md)。

### 可信内网 / NAS

无公网域名时使用独立入口，按提示填写本机私有 IPv4；仅支持符合上述环境的 Linux 主机，不保证所有 NAS 系统兼容：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TG2Cloud/main/install-lan.sh)
```

内网 HTTP 不加密，禁止公网端口转发；不要用此入口绕过公网 HTTPS。详见[内网指南](manuals/lan-install.md)。

### 卸载预检

```bash
tg2cloud uninstall --edition all --check
```

也可运行 `tg2cloud`，选择对应 Edition → 14. 卸载当前实例。上面的子命令只预览，不卸载。实际完整卸载会删除所选实例的数据和可识别备份；需保留数据时使用 `--keep-data`，先保存清理范围之外的独立备份。详见[卸载指南](manuals/uninstall.md)。

## 使用文档

按主题查阅仓库文档；完整目录见 [docs](manuals/readme.md)，更新内容见 [发布说明](release-notes.md)。

| 主题 | 仓库文档 |
| --- | --- |
| 安装与配置 | [公网 VPS 安装 / 升级](manuals/vps-install.md) · [局域网 / NAS 安装](manuals/lan-install.md) · [填写信息清单](manuals/setup-checklist.md) |
| Docker Compose | [新手分步部署、初始化与备份](manuals/compose.md) · [可选 HTTPS 向导](manuals/compose.md#可选-https-配置向导) |
| 网关与首次转存 | [CloudDrive2 / OpenList 配置与验收](manuals/storage.md) · [Bot 使用、菜单与命令](manuals/bot-usage.md) |
| 命名与常见问题 | [视频、说明及媒体组命名](manuals/naming.md) · [FAQ](manuals/faq.md) |
| 配置与备份 | [配置字段及运行目录](manuals/configuration.md) · [备份范围与恢复预检](manuals/backup-restore.md) |
| 家庭与手机读取 | [Bot 家人授权](manuals/family-sharing.md) · [外部 WebDAV 只读入口](manuals/external-webdav.md) |
| 可选功能 | [文件分类、通知与配额](manuals/feature-enhancements.md) · [复用已有 Nginx](manuals/docker-nginx.md) |
| 运行与故障排查 | [管理命令、日志与 HTTPS 维护](manuals/operations.md) · [任务恢复、验证与备份](manuals/recovery.md) |
| 当前维护 | [v1.5.9 发布与兼容](releases/release-1.5.9.md) |
| 卸载 | [卸载指南](manuals/uninstall.md) |
| 开发与测试 | [贡献指南](policies/contributing.md) · [仓库结构](manuals/development/repository.md) · [Linux 开发与测试](manuals/development/development.md) |
| 版本与发布 | [所有版本分别做了什么](changelog.md) · [当前发布说明](release-notes.md) |
| 品牌与第三方组件 | [品牌使用规范](manuals/development/brand-guide.md) · [第三方组件说明](manuals/third-party-components.md) |

## 使用边界

- 仅转存你有权保存的内容；监听须管理员显式授权，不绕过内容保护。115 只是示例，不是唯一目标网盘。
- 云存储登录与授权在你自己的 CloudDrive2 / OpenList 中完成；TG2Cloud 无遥测或凭据回传。
- WebDAV 大小验证不等于内容哈希校验或云盘官方最终完成；重要文件需自行复核。
- 不公开 Token、密码、Session、数据库、配置或备份；升级前备份，不覆盖身份未知的实例。详见[安全政策](policies/security.md)。

## 致谢与项目来源

<table>
  <tr>
    <td align="center" width="260" valign="top" nowrap>
      <a href="https://github.com/LuoPoJunZi">
        <img src="https://github.com/LuoPoJunZi.png?size=96" width="72" height="72" alt="LuoPoJunZi"><br>
        <strong>LuoPoJunZi</strong>
      </a><br>
      <sub>TG2Cloud 项目维护者</sub>
    </td>
    <td align="center" width="260" valign="top" nowrap>
      <a href="https://github.com/whyhhh20">
        <img src="https://github.com/whyhhh20.png?size=96" width="72" height="72" alt="whyhhh20"><br>
        <strong>whyhhh20</strong>
      </a><br>
      <sub>上游 TG115 项目原作者</sub>
    </td>
  </tr>
</table>

感谢 **whyhhh20** 创建并公开上游项目 [TG115](https://github.com/whyhhh20/TG115)。

TG2Cloud 从该项目演进而来，以原项目代码为基础并保留 MIT 许可证、版权和必要致谢；当前在 Telegram → rclone → CloudDrive2/OpenList → 用户云存储链路上继续完善可靠性、部署安全和运维体验。

同时感谢 Telethon、rclone、Docker 等开源项目，以及 CloudDrive2 提供的 WebDAV 和网盘挂载能力。第三方组件分别受各自许可证与服务条款约束。

## 许可证与免责声明

本项目采用 [MIT License](../../LICENSE)。

本项目与 Telegram、CloudDrive2、OpenList、各云存储服务及其运营方没有隶属、授权或官方合作关系。使用者应自行评估账号、数据、网络、VPS 权限和第三方闭源组件风险。
