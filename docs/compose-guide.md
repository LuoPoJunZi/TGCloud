# TGCloud Compose

当前为 0.0.1 本地源码版，提供唯一的 CloudDrive2 Compose 部署包。与脚本部署互不接管。

## 生成部署包

在完整源码目录执行，不要求先运行 Docker：

```bash
python3 -B scripts/build_compose.py --output /absolute/new-compose-output
```

输出包含 `clouddrive2/`、一份带源码摘要的归档及 `COMPOSE-SHA256SUMS.txt`。输出目录必须是新目录，不覆盖已有文件。包内包含当前源码和构建上下文；Bot 镜像在本机构建，不下载旧项目 Bot 镜像。

## 初始化与启动

进入生成的 `clouddrive2/`，在 Linux 私有交互终端执行：

```bash
sudo bash manage.sh
```

菜单核验完整清单和源码摘要后构建候选，逐项生成私有配置。也可以按顺序执行：

```bash
docker compose build --pull tgcloud-clouddrive2-bot setup
docker compose run --rm --no-deps --pull never setup
docker compose up -d clouddrive2
# 先在网关后台完成云挂载和专用 WebDAV 用户配置，再启动 Bot：
docker compose up -d --no-build --pull never tgcloud-clouddrive2-bot
```

默认管理端口绑定回环地址。远程管理使用 HTTPS 向导或受控 SSH 隧道；可信内网访问在初始化中选择，不直接暴露公网 HTTP。

## 维护和升级

继续使用本目录的 `bash manage.sh`。升级时选择同项目完整本地源码或新 Compose 包，核对身份、暂停与活动任务，备份后构建并确认切换。源码摘要不同的同版本也需要升级确认。

`.env`、rclone 配置、数据库、Session、下载、网关授权和备份均为私有数据，不进入公共源码或构建上下文。WebDAV 大小复验不等于内容哈希或云盘官方最终入库。

当前归档是本地候选，不是已发布 Release；缺少 Linux Docker、FUSE、真实网关及 Telegram 条件时，构建通过不能替代实机验收。
