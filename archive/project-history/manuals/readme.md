> 历史档案：以下为原项目记录，不是 TGCloud 0.0.1 的当前教程或验收结论。[归档说明](../../README.md)。

# TG2Cloud 文档目录

当前本地精简版只支持 CloudDrive2，保留 Linux 一键脚本和独立 Docker Compose，安装与管理不再选择 Edition。新仓库未命名，尚未配置在线发布；请先阅读[本地 CloudDrive2 安装指南](local-clouddrive2.md)。

以下保留原项目专题教程和历史验收记录。原项目的在线命令、双 Edition 说明和已发布版本不代表本地精简版；当前部署以本地指南和 Compose 文档开头为准。

## 第一次使用

| 目标 | 阅读顺序 |
| --- | --- |
| 公网 VPS 安装 | [填写信息清单](setup-checklist.md) → [Linux 环境要求](architectures.md) → [公网安装](vps-install.md) |
| 可信内网或 NAS | [内网安装](lan-install.md) → [网关配置](storage.md) |
| 自己管理 Docker Compose | [Compose 新手教程](compose.md)：安装 Docker、初始化、可选 HTTPS、启动与升级 |
| 配好网关，完成首次转存 | [CloudDrive2 / OpenList](storage.md) → [Bot 使用](bot-usage.md) |

一键脚本与 Compose 是不同部署方式，已有实例按原方式升级，不能相互覆盖或接管。

v1.5.9 提供下载停滞保护与排队诊断修复。用户已确认 CloudDrive2 一键脚本升级与实际转存通过，详细证据及其他待验收范围见[本版记录](../releases/release-1.5.9.md)。两种部署仍按原方式升级。

## 使用与可选功能

- [Bot 使用、菜单和命令](bot-usage.md)：发送文件、查看任务、失败重试和恢复。
- [文件命名与媒体组](naming.md)：原名、说明、多语言标题、媒体组与命名边界。
- [管理员监听](listening.md)：个人账号授权、来源、筛选草稿、审批和来源目录。
- [家庭共享](family-sharing.md)：管理员与成员授权、共用保存目录和独立读取账号。
- [文件分类、通知与配额](feature-enhancements.md)：可选设置、默认值与权限。
- [外部 WebDAV](external-webdav.md)：独立账号、只读访问、手机连接与维护。
- [复用已有 Docker Nginx](docker-nginx.md)：支持条件、证书责任与配置事务。

## 运维和排查

- [运行状态与维护](operations.md)：`tg2cloud` 菜单、日志、服务、凭据和配置变更。
- [Compose 安全维护](compose-management.md)：独立部署的离线菜单、资源诊断、升级和备份。
- [任务恢复与验证](recovery.md)：取消后重新转存、远端冲突、遗留文件和磁盘保护。
- [配置与运行目录](configuration.md)：环境变量、账号边界、数据库及 Session。
- [备份与恢复预检](backup-restore.md)：一致性快照、隔离检查、恢复前准备与回退。
- [卸载指南](uninstall.md)：范围预检、完整清理、保留数据和依赖保护。
- [常见问题](faq.md)：按安装、网关、任务和数据问题快速定位。

## 开发与发布

- [v1.5.9 发布与兼容记录](../releases/release-1.5.9.md)：下载无进度保护、就绪降级与排队原因。
- [v1.5.8 发布与兼容记录](../releases/release-1.5.8.md)：两种部署的共享兼容、Compose 管理与安全维护；实机验收安排在发布后。

- [贡献指南](../policies/contributing.md)
- [仓库结构与维护规范](development/repository.md)
- [Linux 开发与测试](development/development.md)
- [发布检查清单](development/release-checklist.md)
- [当前 v1.5.7 发布与兼容记录](../releases/release-1.5.7.md)：监听冷却恢复、来源提示与实际日志限额；CI、资产与未执行的实机项目。
- [v1.5.6 发布与兼容记录](../releases/release-1.5.6.md)：运行安全与诊断维护、发布和未执行的实机验收范围。
- [v1.5.5 发布记录](../releases/release-1.5.5.md)：前版命名／监听增强及历史验收证据。
- [v1.5.4 发布与兼容记录](../releases/release-1.5.4.md)：监听下载修复、验证边界与升级步骤。
- [v1.5.3 发布与兼容记录](../releases/release-1.5.3.md)：实际 CI、资产摘要与尚未执行的实机清单。
- [所有 TG2Cloud 版本分别做了什么](../changelog.md)：保留版本事实；旧验收资料以固定历史提交链接查阅，不作为当前教程。
- [当前发布说明](../release-notes.md)

## 项目与许可

- [项目首页](../overview.zh.md) · [English README](../overview.en.md)
- [品牌规范](development/brand-guide.md)
- [第三方组件](third-party-components.md) · [安全政策](../policies/security.md) · [MIT 许可证](../../../LICENSE)

本目录是仓库的完整阅读入口，网站提供独立的同主题教程。上游致谢、版权和许可证保留；现有实例的数据库及配置兼容说明保留在对应指南，不提供自动导入其他项目数据的承诺。不要公开凭据、Session、数据库或备份。
