## TGCloud v0.0.3

TGCloud 0.0.3 完善 Linux 安装与 Compose 初始化界面、磁盘资源推荐和部署教程。仅支持 CloudDrive2，保持现有任务、账号、数据库和 Session 标识。

## 本版变化

- 安装状态与 Bot 运行状态分别显示，避免把尚未启动的 Bot 描述成未安装。
- 终端提示按显示宽度换行；业务就绪字段从同一列开始，主菜单只显示一次管理工具版本。
- 磁盘预算与保留空间按实际容量和余量计算，不再限制为固定 8GB／20GB；一键安装同时参考 CPU、总内存和可用内存选择推荐方案。
- Compose 初始化从部署目录所在磁盘计算预算默认值，明确说明其宿主资源检测范围。
- Compose 下载自动解析最新正式发行版，校验后直接部署到 `/root/TGCloud-Compose`。
- 重写项目首页和部署说明，移除独立第三方组件说明文件，保留 MIT 许可证、原版权声明及必要致谢。

## 安装与管理

在 Linux root 终端执行一键安装命令，普通用户先运行 `sudo -i`：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TGCloud/main/install.sh)
```

入口自动下载并校验本版正式源码。默认安装目录 `/opt/tgcloud`，管理命令 `tgcloud`。可信内网在命令末尾追加 `--lan`。Compose 的完整下载命令和初始化步骤见 [Compose 部署教程](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.3/docs/compose-guide.md)。

## 自动构建的部署附件

- `TGCloud-v0.0.3-source.tar.gz` 和 `SHA256SUMS.txt`：完整源码及校验文件。
- `TGCloud-v0.0.3-compose.tar.gz` 和 `COMPOSE-SHA256SUMS.txt`：Compose 部署包及校验文件。

附件由 GitHub Actions 从同一不可变标签生成；经过原生 AMD64／ARM64 验证后自动发布。Bot 镜像仍在部署主机本地构建，不发布 Docker 镜像。

## 已有实例与验证范围

保留已有实例配置；新的资源推荐只用于首次配置，不自动改写保存的预算。升级前创建独立备份。0.0.1／0.0.2 Compose 实例需使用新版源码工具检查并执行升级，具体命令见上方 Compose 部署教程。本版未修改 CloudDrive2 镜像摘要；更早实例如遇网关镜像差异，仍按[维护指南](https://github.com/LuoPoJunZi/TGCloud/blob/v0.0.3/docs/operations-guide.md#源码升级)处理。

自动检查覆盖安装与管理、资源计算、配置、打包、源码身份和运行边界。真实 Telegram 转存、云盘最终入库、公网 HTTPS 与长期运行仍需在实际环境验收。WebDAV 大小复验不等于内容哈希校验。
