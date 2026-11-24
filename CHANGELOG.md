# TGCloud 更新记录

## TGCloud v0.1.0

- 公网和内网安装、升级成功后，自动同步已安装且与实例匹配的外部 WebDAV 维护工具，保留原配置和启用状态。
- Bot 回滚后，管理菜单可打开与实际实例同源的已核验 WebDAV 工具。候选在切换启动器前完成完整性检查，同版本文档更新也可同步来源记录。
- 公网和内网结果汇总分别显示附加同步和基础升级的结果。按 Ctrl+C 会显示实际停止阶段「附加工具同步」。
- Go 宿主工具新增 tgcloud-host --version，管理菜单区分工具版本和实例版本，个人监听状态也显示当前组件版本。

## TGCloud v0.0.10

- Go 管理菜单接入公网 HTTPS、证书续期演练、Bot 成员配额、实例快照和代理备份。已有 v2/v3 HTTPS 代理继续使用 Python 管理菜单。
- 实例快照核对实例版本，并使用 SQLite 一致性备份。快照预检和代理恢复演练收到中断信号后，会等待子进程退出并清理临时解包目录。
- Certbot 续期演练按精确容器名查询，核对任务标签后清理容器，并再次确认容器已删除。

## TGCloud v0.0.9

- 完成 Go + Python 架构清晰分工：Go 静态单二进制（`tgcloud-host`，纯标准库、AMD64/ARM64、`<= 8 MiB`）全面接管 Linux 宿主机运维，Python 专注容器内 Bot 核心业务并保留兼容回退。
- 按职责进一步拆分 Go 宿主包（`deps`、`report`、`acceptance`、`uninstall`、`backupops`、`webdav`、`tui` 与底层 `hostutil`），全部补齐 Linux 单元测试与非 Linux 构建桩。
- 将受管卸载（`uninstall`）、实例/共享代理备份校验与清理（`backups`）、外部 WebDAV 认证写入与挂载核验（`webdav-entry`）纳入 Go 宿主工具并接入优先调度。
- 新增原生 Go 两级 5 分组管理菜单（`menu` / `tgcloud --go-menu`）与只读脱敏诊断报告（`diagnostics`），业务设置通过 `docker exec` 调用容器内协议，实现宿主机零 Python 运维路径。

## TGCloud v0.0.8

- 全面加固 Go/Python/Shell 安装与回滚安全性：修复 APT 密钥目录 `umask` 权限、SQLite 回滚旁文件清理顺序、External WebDAV 外部 Nginx 凭据吊销同步、任务取消状态不变量、WebDAV 429/401 探测冷却、凭据脱敏与 CI 测试覆盖。
- 重构 `tgcloud` 本地管理主菜单为 5 组两级分类菜单，并统一各级子菜单显示宽度与“返回上级”导航语义。
- 按职责拆分 `hostcli/internal/{hostutil,storage,redact}`、`src/tgcloud/deployment/ops/` 与 `tests/deployment/{cli,compose,install,ops,proxy}/` 模块结构。

## TGCloud v0.0.7

- 公网和内网一键安装、升级优先使用源码包内经过校验的 Go 宿主工具，保留原有向导和 Python 回退。
- Go 宿主工具处理资源预检、部署、健康检查、备份与回滚，以及 HTTPS／内网 HTTP 管理入口和安装后验收。
- 正式源码包提供 Linux AMD64 和 ARM64 的 Go 宿主工具。独立 Docker Compose 继续使用原有部署流程。

## TGCloud v0.0.6

- 按职责整理测试与共用夹具，同步更新 pytest、unittest 和 CI 测试入口。
- 归拢安装与维护代码，拆分内网安装流程和网关配置。
- 共用 Docker 容器归属校验，将本地开发工具归入 scripts/dev。

## TGCloud v0.0.5

- 修复内网一键安装在确认后因共用安装器读取公网代理配置而中断的问题。
- 新增回归测试，覆盖确认安装、Bot 健康检查、WebDAV 验收和安装后的管理菜单。

## TGCloud v0.0.4

- 公网和可信内网共用 install.sh，内网安装追加 --lan。完整源码包仅保留统一入口。
- 同步更新安装文档、管理提示、打包和 CI，原有内网入口测试迁到统一脚本。

## TGCloud v0.0.3

- 分别展示安装状态和 Bot 运行状态，优化中文提示、长行换行和业务就绪排版。
- 依据实际磁盘计算暂存预算与保留空间，结合 CPU 和内存选择推荐方案。
- Compose 初始化采用部署盘默认值，明确资源检测范围。
- Compose 按最新正式发行版下载并校验，部署目录统一为 `/root/TGCloud-Compose`。
- 重写 README 和部署说明，移除独立第三方声明文件，保留许可证与原版权归属。
- 由 GitHub Actions 验证同一发布标签并自动生成源码、Compose 附件及 SHA256 校验文件。

## TGCloud v0.0.2

- 安装与管理界面统一使用 TGCloud，自动使用已准备的安装源码。
- HTTPS 环境检查、证书验证和入口检查显示清楚的中文步骤。
- 监听菜单统一为“监听转存管理”；保留现有配置、数据路径和部署方式。
- 更新并验证 CloudDrive2、Python 基础镜像及开发依赖的固定版本。
- 完善主分支检查，文档变更同样运行原生 AMD64／ARM64 验证。

本版 CloudDrive2 镜像摘要有变化；已有实例会停止自动升级。先备份并在隔离环境验收候选网关，再安排升级，详见[日常维护](docs/operations-guide.md#源码升级)。

## TGCloud v0.0.1

首次发布，版本标签 `v0.0.1`。

- Telegram Bot 文件转存、持久化任务、权限与配额管理。
- CloudDrive2 WebDAV 集成、大小复验及任务恢复。
- 规则分类、说明文字命名预览与授权来源监听。
- Linux 源码安装、独立 Compose、本地镜像构建及维护菜单。
- HTTPS、可选外部只读访问、诊断、快照和隔离恢复演练。
- 自动识别安装源码、中文安装结果提示，默认安装目录 `/opt/tgcloud`。
- 提供完整源码和 Compose 部署附件及 SHA256 校验；固定版本一键启动安装向导。

支持范围与运行条件以当前文档和代码为准，具体部署仍需实际环境验证。
