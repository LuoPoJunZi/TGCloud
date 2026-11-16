# 从完整本地源码开始

当前为 0.0.1 本地源码版，只支持 CloudDrive2。完整源码目录必须包含 `scripts/run_source.py`、`src/tgcloud/`、`packaging/` 和依赖锁文件；只复制一个 Shell 文件不能安装。

## 环境

使用符合现有预检的原生 Linux AMD64 / ARM64、Python 3.10+ 标准库、交互终端及 root 权限。CloudDrive2 需要 `/dev/fuse`。具体内存、磁盘和 inode 条件由预检核对，任务预算不是磁盘分区。Docker、基础镜像、依赖和网关镜像仍可能需要网络下载。

## 公网 HTTPS

准备指向主机的域名及 80/443 入站条件，在完整源码目录执行：

```bash
sudo bash install.sh
```

直接进入 CloudDrive2 管理菜单，选择安装与升级，按提示填写完整本地源码的绝对目录、资源预算与 Telegram / WebDAV 配置，预览后确认。

## 可信内网

```bash
sudo bash install-lan.sh
```

填写本机实际配置的私有 IPv4。内网 HTTP 不加密，不应通过公网端口转发暴露。

## 只读预检

```bash
sudo python3 -B scripts/run_source.py install --check --source-dir /absolute/TGCloud
sudo python3 -B scripts/run_source.py lan --check --lan-ip 192.168.1.10 --source-dir /absolute/TGCloud
```

不需要 Edition 参数。预检不执行安装或 WebDAV 写入。完整安装后使用 `tgcloud` 管理；源码仍可用 `python3 -B scripts/run_source.py manage` 打开菜单。

## Docker Compose

使用独立 Compose 时，先从完整源码生成 CloudDrive2 包，再在包内运行 `sudo bash manage.sh`。步骤和启动顺序见 [Compose 指南](/deploy/compose/)。

## 首次转存

在自己的 CloudDrive2 管理页挂载云存储，创建专用 WebDAV 用户并核对根目录和写入权限，再单独确认 WebDAV 写入验收，最后转存一个有权保存的小文件。详见[配置](/deploy/clouddrive2/)和 [Bot 使用](/usage/commands/)。

当前不接管其他项目或身份未知的旧实例，不执行自动迁移。新项目仓库名称已确定，但在线安装与正式发布仍待配置，不能使用旧项目 Release 代替本地源码。
