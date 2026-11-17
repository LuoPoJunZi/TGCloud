## TGCloud v0.0.1

TGCloud 0.0.1 首次发布，仅支持 CloudDrive2。Bot 镜像在部署主机本地构建。

## 功能

TGCloud 提供面向 CloudDrive2 的 Telegram 文件转存、持久化任务与恢复、资源控制、分类与规则命名、成员权限及授权监听。

## 部署

使用完整源码运行 Linux 安装向导，或生成独立 Compose 包并在本机构建 Bot 镜像。支持原生 AMD64／ARM64 Debian／Ubuntu；CloudDrive2 挂载需要 FUSE。

一键启动公网安装向导：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install-online.sh)
```

在 Linux root 终端执行，普通用户先运行 `sudo -i`。入口自动下载并校验本版正式源码，不需要 Git。默认安装目录 `/opt/tgcloud`，管理命令 `tgcloud`，可信内网在上述命令末尾追加 `--lan`。

## 下载附件

- `TGCloud-v0.0.1-source.tar.gz` 和 `SHA256SUMS.txt`：完整源码。
- `TGCloud-v0.0.1-compose.tar.gz` 和 `COMPOSE-SHA256SUMS.txt`：带本地构建上下文和管理工具的 Compose 部署包。

下载后先按对应校验文件运行 `sha256sum -c`。Compose 包解压到独立空目录后运行 `sudo bash manage.sh`；下载、初始化、CloudDrive2 配置和直接 Docker 命令见 [Compose 部署指南](https://github.com/LuoPoJunZi/TGCloud/blob/main/docs/compose-guide.md)。

## 验证范围

使用当前自动化检查和对应环境集成测试验证变更。真实 Telegram、网盘最终入库、公网 HTTPS 与长期运行结果需另外验收。安装、配置、备份和许可说明见 [文档索引](https://github.com/LuoPoJunZi/TGCloud/blob/main/docs/README.md)。
