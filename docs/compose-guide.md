# TGCloud Docker Compose 部署

## 下载版本部署包

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

## 从源码生成部署包

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

## 初始化与启动

下载附件时进入上方 `deployment/`，源码构建时进入 `build/compose/clouddrive2/`：

```bash
sudo bash manage.sh
```

菜单核验文件清单与源码身份，构建本地镜像并逐项配置。也可按以下顺序操作：

```bash
sudo docker compose build --pull tgcloud-clouddrive2-bot setup
sudo docker compose run --rm --no-deps --pull never setup
sudo docker compose up -d clouddrive2
```

先通过回环、内网或 HTTPS 管理入口完成云挂载和 WebDAV 专用用户配置，再启动 Bot：

```bash
sudo docker compose up -d --no-build --pull never tgcloud-clouddrive2-bot
```

初始化生成的 `.env` 为私有文件；需要时使用 sudo 运行管理命令，不放宽文件权限。`setup` 是短时配置工具，不挂载 Docker socket，也不启动 Bot。

## 服务与数据

```bash
sudo docker compose ps
sudo docker compose logs --tail 100 tgcloud-clouddrive2-bot
sudo docker compose restart tgcloud-clouddrive2-bot
```

持久化目录包括 `data/`、`config/`、`downloads/`、`logs/` 和 `clouddrive/config/`。`clouddrive/mounts/` 是云挂载区域，不能当成本地快照内容递归归档。

## 维护、升级与卸载

继续使用本目录的 `sudo bash manage.sh`，或从完整源码运行 `python3 -B scripts/run_source.py compose --root /absolute/deployment`。脚本部署与独立 Compose 分别管理各自实例。

升级使用完整 TGCloud 源码或新生成的部署包，先备份，再核对配置、来源和候选镜像。卸载前审阅菜单给出的数据删除范围；保留独立备份，不用 `docker system prune` 代替项目卸载。

HTTPS 与只读访问见[网络指南](network-guide.md)，备份范围见[备份与恢复](backup-and-recovery.md)。
