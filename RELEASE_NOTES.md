## TGCloud v0.0.5

本版修复可信内网首次安装或升级在确认后中断的问题。内网流程继续完成 CloudDrive2 网关和 Bot 的启动检查，再开放内网管理入口。

## 安装命令

在 Linux root 终端执行。公网 VPS 使用：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)
```

可信内网使用：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh) --lan
```

两条命令均下载并校验 v0.0.5 正式源码。如果已有完整源码，在源码目录运行 sudo bash install.sh，可信内网追加 --lan。默认安装目录为 /opt/tgcloud，管理命令为 tgcloud。

## Docker Compose

按 [Compose 部署教程](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.5/docs/compose-guide.md)下载最新正式版。部署目录为 /root/TGCloud-Compose，Bot 镜像在部署主机本地构建。已有实例升级使用已校验的完整源码或 Compose 包，按原管理流程保留数据。

## 自动打包附件

- TGCloud-v0.0.5-source.tar.gz 和 SHA256SUMS.txt
- TGCloud-v0.0.5-compose.tar.gz 和 COMPOSE-SHA256SUMS.txt

GitHub Actions 从同一标签执行 AMD64 和 ARM64 检查，生成并发布源码、Compose 附件和各自的校验文件。

## 升级与验证

新增回归测试覆盖内网确认安装、部署包生成、Bot 健康检查、WebDAV 验收和安装后的管理菜单。已有实例通过原部署入口升级，沿用现有配置和数据目录。
