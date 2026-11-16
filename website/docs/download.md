# 本地包与发布状态

**TGCloud v0.0.1** 当前待发布，没有正式 Release、Bot 镜像或在线一键安装资产可供验证。

## 本地生成

在完整源码目录运行 `python3 -B scripts/build_compose.py --output /absolute/new-output`。只生成 CloudDrive2 包，并附 `COMPOSE-SHA256SUMS.txt`；文件名带当前源码摘要。不要用旧项目发布资产替代。

## 使用入口

完整源码使用 `sudo bash install.sh` 或 `sudo bash install-lan.sh`；生成的包使用 `sudo bash manage.sh`。当前没有仅下载一个脚本即可从网上安装业务代码的承诺。

## 后续发布

仓库目标是 [LuoPoJunZi/TGCloud](https://github.com/LuoPoJunZi/TGCloud)。建立并验证新项目发布链之前，文档不提供虚构的版本下载地址或 immutable SHA。
