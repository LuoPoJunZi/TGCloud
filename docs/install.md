# TGCloud 安装指南

## 环境准备

使用原生 64 位 Debian／Ubuntu AMD64 或 ARM64 主机，准备 root／sudo、curl、Python 3.10+ 和 CA 证书。受管网关要求 Docker Compose v2、可用的 `/dev/fuse` 和足够磁盘空间。安装器会检查本机及 Docker 平台；不使用 QEMU 模拟部署。

Debian／Ubuntu 可先安装源码入口所需工具：

```bash
sudo apt-get update
sudo apt-get install -y curl python3 ca-certificates
```

## 准备 Telegram 凭据

1. 在 Telegram 联系官方 `@BotFather`，发送 `/newbot`，按提示设置名称和用户名，私下保存返回的 Bot Token。创建流程见 [Telegram 官方 Bot 教程](https://core.telegram.org/bots/tutorial)。
2. 使用自己的账号登录 [my.telegram.org](https://my.telegram.org)，进入 **API development tools** 创建应用，取得 API ID 和 API Hash。字段含义见 [Telegram 官方应用申请说明](https://core.telegram.org/api/obtaining_api_id)。
3. 准备管理员账号的数字用户 ID。`ALLOWED_USER_ID` 填该账号的 ID；用户名、手机号和 Bot ID 不能代替它。
4. 打开刚创建的 Bot 私聊并点击“开始”。安装时将 API ID、API Hash、Bot Token 和管理员 ID 分别填入对应字段。

密码和 Token 在私有终端逐项输入，不放进安装命令。个人账号监听授权在完成基本转存后另行配置。

## 运行安装向导

先运行 `sudo -i` 进入 root 终端；已经是 root 时无需重复。公网 VPS 准备指向本机的域名并放行 TCP 80/443，然后执行：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install-online.sh)
```

入口自动下载 `v0.0.1` 正式源码包，校验 SHA256 并安全解包到私有临时目录，再运行原有交互向导；不要求安装 Git、克隆仓库或输入源码目录。退出时清理本次临时源码，不删除运行实例。下载、校验失败会停止，不改用开发分支业务代码。

仅安装 CloudDrive2，不出现版本选择；向导检查容器、目录和资源，展示计划后由你确认安装或升级。已有实例会按当前工具的归属与配置规则检查，不重新初始化数据。

无公网域名时，使用同一入口的可信内网向导：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install-online.sh) --lan
```

按提示填写本机私有 IPv4。内网 HTTP 不加密，不向公网映射管理端口。后续维护使用 `tgcloud`；需要重新下载正式源码运行向导时，可再次运行上述在线命令。

## 已有完整源码

完整源码内仍使用原有 `sudo bash install.sh` 或 `sudo bash install-lan.sh`。需要固定版本的 Git 工作副本时另行安装 Git，并执行：

```bash
git clone --depth 1 --branch v0.0.1 https://github.com/LuoPoJunZi/TGCloud.git TGCloud && cd TGCloud && sudo bash install.sh
```

目标 `TGCloud` 源码目录须尚不存在。这是可选的源码安装方式。

## 从版本源码包安装

不使用 Git 时，在一个空下载目录执行以下命令。需要 curl、tar 和 sha256sum；脚本仍从完整源码运行：

```bash
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.1/TGCloud-v0.0.1-source.tar.gz &&
curl -fLO https://github.com/LuoPoJunZi/TGCloud/releases/download/v0.0.1/SHA256SUMS.txt &&
sha256sum -c SHA256SUMS.txt &&
tar --no-same-owner -xzf TGCloud-v0.0.1-source.tar.gz &&
cd TGCloud-v0.0.1 &&
sudo bash install.sh
```

默认目录是 `/opt/tgcloud`，Bot 容器为 `tgcloud-clouddrive2-bot`，网关为 `tgcloud-clouddrive2`。程序来源和文件摘要记录在实例内的 `tgcloud-source.json`。身份不明或已有非受管文件的目录会被拒绝。

从完整源码运行脚本时会自动识别源码目录，无需重复输入。新默认目录只用于新安装；已有实例按自身目录核验，不自动搬迁数据库或 Session。检测到其他目录中的残留 TGCloud 数据时会停止新建实例。

可信内网部署使用 `sudo bash install-lan.sh`，按向导填写主机内网 IPv4。公网管理优先采用 HTTPS；两种入口使用相同的 Bot 与 CloudDrive2 核心。

## 配置 CloudDrive2

网关管理端口默认仅绑定主机回环地址。可从自己的电脑建立 SSH 隧道，将 `SERVER_HOST` 换成主机地址：

```bash
ssh -N -L 19798:127.0.0.1:19798 root@SERVER_HOST
```

浏览器打开 `http://127.0.0.1:19798`，完成 CloudDrive2 登录与云存储挂载。启用专用 WebDAV 用户，确认该用户可访问计划使用的目录；Bot 写入账号应具备相应写入权限。

Bot 容器内默认地址为 `http://tgcloud-clouddrive2:19798/dav`。目标路径相对于 WebDAV 用户可见目录填写，不填写 VPS 文件系统路径。字段详见[配置参考](configuration-guide.md)。

## 验证与开始使用

安装完成后运行 `sudo tgcloud`，检查服务状态，执行“10. 业务就绪检查”和“11. WebDAV 验收”。验收会在授权后创建并清理随机测试文件。

向自己的 Bot 发送一个小文件，查看任务状态，再在云盘官方客户端确认。通过后再增加任务量或启用监听。完整步骤见 [Bot 指南](bot-guide.md)。

## 管理与其他部署方式

日常维护使用 `sudo tgcloud`。配置变化通过菜单应用，避免直接修改运行中的数据库或 Session。独立容器部署见 [Compose 指南](compose-guide.md)，证书与反向代理见[网络指南](network-guide.md)。
