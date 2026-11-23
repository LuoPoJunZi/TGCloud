# tgcloud-host

TGCloud 的 Linux 宿主管理工具，支持通过一键脚本部署在 `/opt/tgcloud` 的公网和内网实例。它在宿主机查询本机 Docker，并在核对容器归属后执行诊断或服务操作。

在本目录用 Go 1.23+ 构建，运行时需要访问本机的 `/var/run/docker.sock`。

```bash
mkdir -p "$HOME/.local/bin"
CGO_ENABLED=0 go build -o "$HOME/.local/bin/tgcloud-host" ./cmd/tgcloud-host
sudo "$HOME/.local/bin/tgcloud-host" status
sudo "$HOME/.local/bin/tgcloud-host" check
sudo "$HOME/.local/bin/tgcloud-host" logs
sudo "$HOME/.local/bin/tgcloud-host" restart bot --yes
```

| 命令 | 退出码 |
| --- | --- |
| `status` | `0` 已读取状态（包括已停止、未安装），`2` 无法核实实例 |
| `check` | `0` 业务就绪，`1` 未就绪或观测过期，`2` 无法核实 |
| `logs` | `0` 已查看脱敏日志，`1` 无实例，`2` 有日志未能读取 |
| `start/stop/restart <bot\|gateway> --yes` | `0` 容器操作完成，`1` 无实例，`2` 命令或操作失败 |
| `preflight [--allow-missing]` | `0` 输出本机 Docker 平台和版本，`1` 环境核验未通过，`2` 命令或输出失败 |
| `prepare-dependencies <install\|upgrade>` | `0` 宿主依赖已就绪，`1` 检测或安装失败，`2` 命令用法错误 |
| `resources <install-dir> <backup-dir>` | `0` 输出资源探测结果，`1` 无法采集完整数据，`2` 命令或输出失败 |
| `prepare-image <image@sha256:digest>` | `0` 候选镜像已通过原生平台检查，`1` 准备失败，`2` 命令用法错误 |
| `build-candidate <stage-dir> <candidate-tag>` | `0` 候选 Bot 镜像及配置已通过预检，`1` 构建或预检失败，`2` 命令用法错误 |
| `uninstall {check\|run} [--keep-data\|--purge-data\|--purge-all] [--json] [--yes]` | `0` 预检或卸载完成，`1` 归属/挂载/依赖核验未通过或执行失败，`2` 命令用法错误 |
| `backups {list\|proxy-list\|proxy-create\|proxy-verify\|proxy-prune-preview\|proxy-prune}` | `0` 备份查询/创建/校验/清理完成，`1` 安全边界或校验失败，`2` 命令用法错误 |
| `webdav-entry {status\|write-auth\|verify-auth}` | `0` 外部 WebDAV 状态核对或认证文件原子写入/校验完成，`1` 校验失败 |
| `diagnostics [--install-dir <dir>] [--save <dir>]` | `0` 输出只读脱敏故障诊断报告（可选私密保存），`1` 实例未就绪或保存失败 |
| `menu [--install-dir <dir>]` | `0` 退出原生 Go 两级 5 分组管理菜单（以 `tgcloud` 名称无参启动时自动进入），`1` 启动失败 |

## Go / Python 职责分工与目录结构

- **Go (`hostcli/`)**：静态编译单二进制（仅依赖标准库、`GOPROXY=off`、`<= 8 MiB`），承担 Linux 宿主机侧的全部部署、容器生命周期、入口/证书、备份、卸载、故障诊断与原生 `tgcloud` 交互菜单；业务功能设置通过 `docker exec -i` 调用 Bot 容器内协议，宿主机不依赖 Python 业务环境。
- **Python (`src/tgcloud/`)**：承担容器内 Bot 核心业务（Telethon、队列/rclone/流式上传、SQLite 状态机、命名分类、配额、通知、容器内管理协议端点）以及兼容回退路径。

`hostcli/internal/` 按职责拆分：

- `hostutil/`：路径安全校验、原子写入、受限缓冲、`/proc/self/mountinfo` 挂载点解析等底层原语
- `storage/`：宿主档位、预算与存储容量门槛策略
- `redact/`：日志敏感字段脱敏
- `deps/`：Debian/Ubuntu 基础软件与 Docker 官方 APT 源依赖准备
- `report/`：安装收尾状态与备份统计报告
- `acceptance/`：安装后状态汇总、容器日志轮转核验与 WebDAV 写入验收
- `inspect/`：实例发现、Docker 预检、镜像准备、安装/升级事务、公网 HTTPS / 内网 LAN 入口管理
- `uninstall/`：受管实例与共享代理的 fail-closed 卸载预检与执行
- `backupops/`：实例历史备份清单与共享代理 tar/manifest/SHA256 备份、校验、演练与清理
- `webdav/`：外部 WebDAV 记录状态核对与认证文件原子写入/容器挂载摘要校验
- `tui/`：原生 Go 两级 5 分组交互菜单与脱敏诊断报告生成

`check` 沿用容器内的观测结果，WebDAV 写入验收仍通过现有管理菜单执行。独立 Docker Compose 部署继续在部署目录运行 `bash manage.sh`。

`logs` 查看最近 30 分钟的 Bot 与网关日志，每个服务最多 100 行。启停命令要求显式给出服务以及 `--yes`，与原管理工具共用实例锁。停止或重启可能中断活动任务或存储访问。

本目录的 Go 测试使用 `go test ./...`。

## 接入 TGCloud 管理菜单

正式源码包会携带 Linux AMD64 和 ARM64 的宿主管理工具。公网和内网安装把当前架构的工具放进受管菜单目录，部署机使用包内的二进制。菜单中的「查看运行状态」「查看运行日志」「服务启停管理」和「业务就绪检查」优先走 Go。缺少可用二进制时使用 Python 实现，启停一旦发出则以实际容器状态为准。

从源码在 Linux 上试用菜单接入时，先在本目录构建，再从仓库根目录运行原安装入口。只包含 Python 源码的安装仍可使用原管理菜单。

```bash
arch="$(go env GOARCH)"
mkdir -p "bin/linux-$arch"
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w -buildid=' -o "bin/linux-$arch/tgcloud-host" ./cmd/tgcloud-host
```

公网与内网安装、升级向导优先用已安装且通过校验的 Go 工具检查 Docker context、原生架构和 rootful 模式。首次安装或旧工具缺少预检命令时使用 Python 检查。`preflight --allow-missing` 在 Docker 未安装时输出 `null`，供现有首次安装流程继续准备依赖。

`resources` 采集 CPU、内存、磁盘容量、inode、目录占用和 FUSE 状态。安装目录与 Docker 数据目录分别取样。向导用原有规则计算资源推荐和安装门槛，Go 工具缺失或数据不完整时使用 Python 原有探测。

用户确认后，公网和内网共用 `check-install-resources` 报告 CPU、总内存和安装盘可用空间。内存和安装盘可用空间达到原有门槛后，才通过 `prepare-dependencies` 检查基础软件、Docker 和 Compose。全新宿主缺少 Docker 时，通过官方 APT 仓库安装并启用服务。已有可用的 Docker 会继续运行，APT 仅补装缺少的软件包。工具缺失或版本较旧时，安装器使用 Shell 流程。

依赖就绪后，`check-install-host` 核对同名容器归属、Docker 发布与宿主监听的固定端口，并复核安装目录和 Docker 数据目录的可用空间。检查通过后才进入候选构建与停机备份。缺少该子命令时使用原 Shell 复核。

`install-layout` 创建持久化目录和私有候选配置。候选预检通过后，Go 设置宿主目录权限和 Bot 数据归属。首次安装原子写入 `.env`，常规升级保留已有配置及数据。缺少该子命令时使用原 Shell 流程。

首次安装优先用 `fresh-install` 创建私有候选目录并复制经校验的部署文件。候选预检通过后，Go 在目标文件系统内准备程序、目录和初始配置，再以禁止覆盖的方式发布。发布失败会尝试撤回本次已发布文件。旧工具继续使用原 Shell 暂存和发布流程。

公网和内网升级用 `stage-upgrade` 创建私有候选目录，按来源清单复制部署文件并核验副本。暂存通过后才准备候选配置和构建候选镜像。暂存失败会清理候选目录并在停止旧 Bot 前终止升级。缺少该子命令时使用原 Shell 暂存流程。

`prepare-install-config` 准备一键部署的私有配置。常规升级使用原 `.env` 的私有副本，显式请求更新时使用输入配置。`preflight-candidate` 在旧 Bot 停机前核对候选 Compose、Dockerfile 和原生镜像，`check-install-compose` 在启动正式服务前核对已发布的 Compose 和网关选项。Go 子命令不可用时使用原脚本。

公网和内网安装在用户确认并复查实例后，会在备份和停机前准备 Bot 基础镜像和网关镜像。旧工具沿用 Python 镜像预检，可通过已有的 `prepare-image` 命令准备固定摘要镜像。Go 已开始预检后若出现错误，向导会停止在当前阶段。

候选镜像准备完成后，`build-candidate` 在临时目录构建原生 Bot 镜像，并在只读、无网络的临时容器内运行现有配置检查。构建和配置检查通过后才开始备份及停机。缺少可用的 Go 工具时，Python 调用同一配置检查。

公网和内网升级用 `commit-upgrade-backup` 生成、校验并发布程序配置归档。`quiesce-upgrade` 记录旧 Bot 的容器 ID、镜像及运行状态，再停止仍在运行的容器。`snapshot-database` 通过候选镜像生成 SQLite 一致性快照，`commit-upgrade-backup` 校验后原子发布，保留已存在的备份。程序替换前发生故障时，`resume-upgrade` 核对原容器并恢复原先的运行状态。Go 命令不可用时沿用原脚本流程。

备份就绪后，`apply-upgrade` 暂存并替换受管程序文件，常规升级保留原 `.env`。文件替换或部署验收失败时，`rollback-upgrade` 从配置归档和经过校验的 SQLite 副本恢复旧版，并核对 Bot 镜像及健康状态。Go 子命令不可用时，安装器使用原有恢复流程。

一键安装和升级在程序文件就绪后，优先用 `deploy-bot` 构建正式 Bot 镜像，沿用现有配置校验，随后启动容器并等待基础健康检查。构建、启动或检查失败时，一键升级进入现有的恢复流程。Go 子命令不可用时，安装器沿用原部署步骤。

Bot 构建前用 `start-gateway` 启动或复用受管 CloudDrive2 容器。Bot 启动后，`repair-gateway-network` 保留已有连接，按需补接受管网桥，并核对宿主 HTTP 端口和 Bot 内的 DNS/TCP 连通性。使用宿主或共享命名空间的网关要求 Bot 已处于相同命名空间。激活前失败时，安装器撤销本次新增的网桥连接，再按原流程恢复旧 Bot。Go 子命令不可用时沿用原脚本。

公网和内网的一键安装器在基础健康检查和 CloudDrive2 网络修复后调用 `accept-bot`。Go 核对部署文件、运行配置、代码指纹和版本，成功后原子写入已激活的安装标记。验收失败沿用原升级回滚流程。Go 子命令不可用时沿用原 Shell/Python 验收。

安装激活后，`report-install` 统计已保存的配置、SQLite 和环境备份，并显示本次部署的容器状态。统计失败时显示警告，成功激活的安装可继续使用。缺少该子命令时沿用原 Shell/Python 收尾流程。备份清理由管理员手动发起。

安装器在成功退出、完成失败回滚或响应中断后调用 `cleanup-install`，清理本次候选容器和镜像标签、部署暂存目录及未发布的备份文件。正式备份、运行中的容器和 SQLite 数据继续保留。Go 子命令不可用时由原脚本清理。

公网和内网向导确认部署后，优先调用 `install-transaction` 串联这些宿主操作。Go 在最终验收前核对本次计划的版本、源码摘要和升级前的配置内容。发生故障或中断时，先撤回本次网桥改动，再按执行阶段恢复原容器或回滚程序、配置和 SQLite，最后清理暂存。旧工具缺少 `install-transaction` 时，向导继续使用原一键部署脚本。

一键向导优先使用受管 Go 工具的 `discover-install` 和 `plan-install`。实例发现核对容器、挂载、运行代码和来源记录，只向向导返回私有配置的摘要。计划检查决定新装、升级、已是当前版本或目标较旧，并在确认后重新比对实例身份。向导沿用原有交互。首次运行没有受管工具时，使用同一安装包内的 Go 工具进行只读核验。Go 工具或相应命令不可用时，沿用 Python 检查流程。

一键向导通过 `install-source` 冻结和复核源码，并生成部署包。公网和内网共用原有的文件筛选、大小限制和源码摘要格式。公网代理配置处理完成后，Go 根据最终文件写入待激活的来源记录。Go 子命令不可用时使用 Python，已执行的 Go 检查失败时停止部署。

基础部署优先使用与已确认快照中的工具摘要一致、支持 `install-transaction` 的同包 Go 工具。即使尚未安装本地管理命令，该工具也可执行部署。已验证的本地 Go 工具和原脚本提供回退。

一键向导通过 `manager-runtime` 核对已有管理运行包并发布新运行包。Go 沿用现有安装清单和发布锁，在切换启动器前核对文件摘要、版本及监听帮助。切换使用同目录内的原子替换，候选校验或发布失败时原启动器可继续使用。管理菜单继续由 Python 提供，Go 不可用时使用 Python 发布流程。

一键向导通过 `installer-preflight` 校验 Debian/Ubuntu 宿主、root 和原生架构，沿用本机 Docker 与内网 Docker Engine 28+ 的条件。Go 根据资源观测计算档位、预算和容量复核，沿用现有的内存、FUSE、inode 与磁盘门槛。首次安装的私有候选配置通过标准输入写入，升级只写入保留配置标记。Go 子命令不可用时继续使用原 Python 检查和写入流程。

公网 HTTPS 与内网 HTTP 管理入口分别优先通过 `https-entry`（`probe`/`status`/`configure`）与 `lan-entry`（`state`/`check`/`save`/`activate`）执行预检、状态核对、配置与激活；安装后的 WebDAV 写入验收与运行摘要优先通过 `install-acceptance`（`summary`/`webdav`）执行。对应 Go 子命令不可用时继续使用原 Python 入口和验收流程。
