## TGCloud v0.0.6

这次调整按职责整理仓库目录，便于定位部署代码、开发工具和回归测试。

- 测试按 Bot、部署、集成、命名、仓库、运行时和扩展分组，提取共用夹具并同步 CI 测试入口。
- 公网安装、内网安装、升级、备份和卸载归入部署生命周期模块。内网网关配置与安装流程分开，代理管理和卸载共用 Docker 归属校验。
- 本地性能基准和依赖锁生成工具归入 scripts/dev。源码和 Compose 包的构建入口沿用现有命令。

## 安装与升级

从当前源码安装时，在完整源码目录运行 sudo bash install.sh，可信内网追加 --lan。管理命令为 tgcloud。已发布稳定版通过 README 的一键命令和 Compose 下载说明获取。

已有实例沿用部署指南中的升级流程。相关命令和路径见 [安装指南](https://github.com/LuoPoJunZi/TGCloud/blob/main/docs/install.md) 与 [Compose 部署教程](https://github.com/LuoPoJunZi/TGCloud/blob/main/docs/compose-guide.md)。

## 发布附件

发布流程从同一版本标签生成源码包、Compose 部署包和各自的 SHA256 校验文件。
