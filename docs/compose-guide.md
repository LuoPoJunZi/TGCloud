# TGCloud Docker Compose 部署

按“检查环境 → 下载部署包 → 初始化配置 → 挂载云存储 → 启动 Bot → 测试转存”的顺序操作。此教程只部署 CloudDrive2，适用于首次安装的独立 Compose 实例。

## 1. 准备主机与凭据

通过 SSH 登录实际运行 Docker 的 Linux 主机。普通用户先执行 `sudo -i` 进入 root 终端；已经是 root 可直接操作。本文后续命令中的 `sudo` 在 root 终端可省略。

检查环境：

```bash
docker version
docker compose version
uname -m
ls -l /dev/fuse
python3 --version
df -h
```

需要 Docker 客户端与服务端均可用、Compose v2、原生 `x86_64`／`aarch64`、FUSE 设备、Python 3.10+。默认暂存预算 20GB、保留空闲 8GB，另给镜像、日志及 CloudDrive2 缓存留足空间。暂存预算可以在初始化时调整。

未安装 Docker 时，按主机系统使用官方 [Ubuntu 安装说明](https://docs.docker.com/engine/install/ubuntu/)或 [Debian 安装说明](https://docs.docker.com/engine/install/debian/)，同时安装 Compose 插件。已有 Docker 或 NAS 容器套件按原方式维护；无需重装现有引擎。缺少 FUSE 时先解决宿主机支持，再继续部署。

提前准备以下信息：

| 信息 | 用途 |
| --- | --- |
| Telegram API ID、API Hash | 自己的 Telegram 应用信息 |
| Bot Token | 在 `@BotFather` 创建的 Bot |
| 管理员数字用户 ID | 管理自己的 Bot，不能填用户名或手机号 |
| WebDAV 用户名、密码 | 稍后在 CloudDrive2 创建相同的专用用户 |
| 保存子目录、磁盘预算 | 相对于 WebDAV 用户可见目录的保存位置 |
| 管理页访问方式 | 本机／可信内网，或可选 HTTPS |

Bot 凭据获取步骤见[安装指南](install.md)。所有密码在私有终端输入。主机需能访问 GitHub、Docker 镜像仓库、Python 软件源、Telegram 及云存储；Bot 镜像在本机构建。

## 2. 下载版本部署包

在支持 Docker Compose v2 和 FUSE 的 Linux 主机执行。需要 curl、tar、sha256sum 和 Python 3.10+；`TGCloud-compose-0.0.1` 目录须尚不存在：

```bash
mkdir -m 700 TGCloud-compose-0.0.1 &&
cd TGCloud-compose-0.0.1 &&
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.1/TGCloud-v0.0.1-compose.tar.gz &&
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.1/COMPOSE-SHA256SUMS.txt &&
sha256sum -c COMPOSE-SHA256SUMS.txt &&
mkdir deployment &&
tar --no-same-owner -xzf TGCloud-v0.0.1-compose.tar.gz --strip-components=1 -C deployment &&
cd deployment &&
sudo bash manage.sh
```

下载和校验文件保留在部署目录之外。管理菜单会检查源码身份和文件清单，在本机构建镜像并初始化配置。仅解压单个 Compose YAML 不构成完整部署包。

## 可选：从源码生成部署包

也可克隆固定版本，从源码生成相同部署内容。需要 Git 和 Python 3.10+：

```bash
git clone --depth 1 --branch v0.0.1 https://github.com/LuoPoJunZi/TGCloud.git TGCloud
cd TGCloud
python3 -B scripts/build_compose.py --output "$PWD/build/compose"
cd build/compose/clouddrive2
sudo bash manage.sh
```

输出目录必须尚不存在。生成 `clouddrive2/`、带源码摘要的 `TGCloud-v0.0.1-compose-*.tar.gz` 和 `COMPOSE-SHA256SUMS.txt`。部署包包含程序源码、构建上下文、私有配置向导和宿主管理工具。

Bot 使用 `tgcloud-local:` 加源码摘要的本地镜像名；CloudDrive2 使用 Compose 文件声明的固定摘要镜像。这里不依赖预发布的 Bot 镜像。

## 3. 构建镜像并初始化配置

下载附件时进入上方 `deployment/`，源码构建时进入 `build/compose/clouddrive2/`：

```bash
sudo bash manage.sh
```

管理菜单核验文件清单与源码身份，构建本地镜像并逐项配置。若希望按命令逐步完成，先执行：

```bash
sudo docker compose build --pull tgcloud-clouddrive2-bot setup
sudo docker compose run --rm --no-deps --pull never setup
```

`setup` 会逐项询问 Telegram 凭据、WebDAV 专用用户、保存子目录、暂存预算和访问方案。WebDAV 密码可回车自动生成；在私有终端记录它，稍后在 CloudDrive2 中填写。首次建议保持管理页回环地址 `127.0.0.1`，通过 SSH 隧道访问。

核对计划并确认后，生成 `.env`、`tgcloud-compose.json` 和访问方案。`.env` 归 root 所有、权限为 600。再次运行 `setup` 会校验并保留配置，不重新生成密码；初始化成功不代表 Telegram 登录或网盘写入已通过。

可以单独检查本地配置：

```bash
sudo docker compose run --rm --no-deps --pull never setup python -m tgcloud.compose check
```

## 4. 启动并配置 CloudDrive2

```bash
sudo docker compose up -d clouddrive2
sudo docker compose ps
```

回环地址仅能在服务器本机访问。从自己的电脑另开终端，将 `SERVER_HOST` 换成服务器地址：

```bash
ssh -N -L 19798:127.0.0.1:19798 root@SERVER_HOST
```

保持隧道运行，在电脑浏览器打开 `http://127.0.0.1:19798`。登录 CloudDrive2，完成云存储挂载；创建与初始化向导中一致的 WebDAV 用户及密码，授予 Bot 目标目录的写入权限。

Bot 使用容器内部地址 `http://tgcloud-clouddrive2:19798/dav`，不要将它改成电脑的 `127.0.0.1`。保存子目录相对于该 WebDAV 用户可见根目录，不是服务器 `/opt/` 下的文件夹。

选择可信内网地址时，在同一内网访问 `http://主机内网IPv4:19798`。HTTPS 配置使用 `sudo bash manage.sh` 的“7. HTTPS 与管理”，或随包 `https.sh`；具体步骤见[网络指南](network-guide.md)。

## 5. 启动 Bot 并测试转存

CloudDrive2 挂载和 WebDAV 用户准备完成后，再执行：

```bash
sudo docker compose up -d --no-build --pull never tgcloud-clouddrive2-bot
```

然后运行 `sudo bash manage.sh`，执行“10. 业务就绪检查”和“11. WebDAV 验收”。在自己的 Bot 私聊中发送一个小文件，用 `/queue` 或 `/tasks` 查看任务，再到云盘官方客户端核对。通过后再提交大文件或启用监听。

`setup` 是运行后退出的配置工具，不挂载 Docker socket，也不启动 Bot；不需要让它一直运行。同一个 Bot Token 只运行一个 Bot 实例。

## 6. 日常管理、日志与故障检查

重新登录 SSH 后，先回到本次的 `TGCloud-compose-0.0.1/deployment` 目录；不用重新下载或初始化。以下命令都在包含 `docker-compose.yml` 的同一目录执行：

```bash
sudo docker compose ps
sudo docker compose logs --tail 100 tgcloud-clouddrive2-bot
sudo docker compose restart tgcloud-clouddrive2-bot
sudo docker compose stop tgcloud-clouddrive2-bot
sudo docker compose up -d --no-build --pull never tgcloud-clouddrive2-bot
```

网关日志使用 `sudo docker compose logs --tail 100 clouddrive2`。Bot 在线但不能写入时，依次检查挂载状态、WebDAV 用户与权限、目标目录、磁盘余量和任务错误；不要用重新初始化配置代替排查。完整任务说明见 [Bot 指南](bot-guide.md)。

## 7. 持久化、备份与卸载

持久化目录包括 `data/`、`config/`、`downloads/`、`logs/` 和 `clouddrive/config/`。`clouddrive/mounts/` 是云挂载区域，不能当成本地快照内容递归归档。

备份优先使用 `sudo bash manage.sh` →“13. 数据备份管理”，按提示创建和校验快照；代理证书与配置单独备份。备份含 Session 和凭据，应另存至卸载范围以外的私有位置。覆盖范围和恢复限制见[备份与恢复](backup-and-recovery.md)。

继续使用本目录的 `sudo bash manage.sh`，或从完整源码运行 `python3 -B scripts/run_source.py compose --root /absolute/deployment`。脚本部署与独立 Compose 分别管理各自实例。

升级使用完整 TGCloud 源码或新生成的部署包，先备份，再核对配置、来源和候选镜像。卸载前审阅菜单给出的数据删除范围；保留独立备份，不用 `docker system prune` 代替项目卸载。

卸载入口为管理菜单“14. 卸载当前实例”。`docker compose stop` 只是停止服务；不要直接删除部署目录、数据库或云挂载目录来代替卸载。

HTTPS 与只读访问见[网络指南](network-guide.md)，备份范围见[备份与恢复](backup-and-recovery.md)。
