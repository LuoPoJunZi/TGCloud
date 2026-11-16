> 历史档案：以下为原项目记录，不是 TGCloud 0.0.1 的当前教程或验收结论。[归档说明](../../README.md)。

# CloudDrive2 本地版安装与维护

这是从 TG2Cloud 精简的 CloudDrive2 单版本项目，当前源码版本为 0.0.1。新 GitHub 仓库名称尚未确定，当前不提供在线一键下载地址或正式发布资产。原项目的版本、实机验收和镜像只作为历史记录。

## Linux 一键入口

将完整源码复制到一个新的 Linux 目录。宿主要求 Python 3.10+ 标准库、root、原生 AMD64 / ARM64 用户态、受支持的 Debian / Ubuntu 环境及 CloudDrive2 所需 `/dev/fuse`。具体资源预算由部署预检确认。

公网 HTTPS 安装，在源码目录执行：

```bash
sudo bash install.sh
```

可信内网或符合环境的 NAS：

```bash
sudo bash install-lan.sh
```

两者均直接进入 CloudDrive2 管理菜单。选择“安装与升级”，按提示提供完整本地源码的绝对目录，逐项填写信息、预览并确认。首次安装后先在 CloudDrive2 中完成云存储挂载与专用 WebDAV 用户配置，再单独确认写入验收。网盘账号、Cookie、OAuth 等只在用户自己的网关中配置。

只读预检也直接使用 CloudDrive2，不需要指定 Edition：

```bash
sudo python3 -B scripts/run_source.py install --check --source-dir /absolute/source
sudo python3 -B scripts/run_source.py lan --check --lan-ip 192.168.1.10 --source-dir /absolute/source
```

内网地址须是本机实际配置的私有 IPv4。`--check` 只预检，不进行安装或 WebDAV 写入。公网 HTTPS 与可信内网 HTTP 保持不同入口；内网 HTTP 不用于公网端口转发。

## Compose

在完整源码目录执行以下命令，生成唯一的 CloudDrive2 包：

```bash
python3 -B scripts/build_compose.py --output /absolute/new-compose-output
```

进入输出内的 `clouddrive2/` 并运行 `sudo bash manage.sh`。本地镜像构建、初始化、启动、HTTPS、备份和升级步骤见 [Compose 当前流程](compose.md#当前本地-clouddrive2-版)。

## 数据和旧项目边界

保留现有 CloudDrive2 运行身份 `/opt/tg2cloud-clouddrive2`、`tg2cloud-clouddrive2-bot`、`tg2cloud-clouddrive2` 和 `tg2cloud-clouddrive2-net`。新项目不自动接管占用这些身份的旧安装；来源、配置、容器或代理状态无法核实时停止。不要通过删除旧目录或容器来绕过冲突检查。

已由本地版创建的实例从同项目完整源码升级，保留原始配置、数据库、Session 和历史任务。同版本源码变动也核对摘要并执行升级计划。升级失败保留日志和备份；不以重建数据库或 Session 掩盖迁移问题。

OpenList 安装和管理已退役，旧 OpenList 目录、授权、下载及云端文件均不自动删除或迁移。脚本与 Compose 互不接管。重要数据请在任何实际维护前另存独立备份。

## 开发验证

```bash
python -B .github/scripts/check_python.py all
```

测试仅使用合成数据和隔离资源。缺少 Linux、Docker、rclone、Telegram 或真实网关条件时应分别记录未执行项。构建或离线测试通过不代表实机转存、长期稳定性、内容哈希或官方云端最终入库已验收。
