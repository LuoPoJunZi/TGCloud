## TGCloud v0.0.4

本版统一 Linux 安装入口。公网直接运行 install.sh，可信内网在同一命令后追加 --lan。完整源码包移除独立的 install-lan.sh。

## 安装命令

在 Linux root 终端执行。公网 VPS 使用：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)
```

可信内网使用：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh) --lan
```

两条命令均下载并校验 v0.0.4 正式源码。如果已有完整源码，在源码目录运行 sudo bash install.sh，可信内网追加 --lan。默认安装目录为 /opt/tgcloud，管理命令为 tgcloud。

## Docker Compose

按 [Compose 部署教程](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.4/docs/compose-guide.md)下载最新正式版。部署目录为 /root/TGCloud-Compose，Bot 镜像在部署主机本地构建。已有实例升级使用已校验的完整源码或 Compose 包，按原管理流程保留数据。

## 自动打包附件

- TGCloud-v0.0.4-source.tar.gz 和 SHA256SUMS.txt
- TGCloud-v0.0.4-compose.tar.gz 和 COMPOSE-SHA256SUMS.txt

GitHub Actions 从同一标签执行 AMD64 和 ARM64 检查，生成并发布源码、Compose 附件和各自的校验文件。

## 升级与验证

本版变更限于内网入口、文档和发布检查。升级使用当前受管实例的既有路径。入口回归覆盖本地和在线分流、终端输入及参数转发、失败时停止和源码包验证。
