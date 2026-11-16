> 历史档案：以下为原项目记录，不是 TGCloud 0.0.1 的当前教程或验收结论。[归档说明](../../README.md)。

# v1.5.9 发布与兼容记录

状态：2026-11-15，v1.5.9 已正式发布。GitHub CI、同标签原生双架构检查及正式资源下载核验完成；发布后用户提供日志并确认 CloudDrive2 一键脚本实例的 v1.5.8 → v1.5.9 升级与实际转存验收通过。真实用户 VPS 的已验收范围和未执行项目见下文，不扩展为其他后端或部署方式已通过。

## 修复与兼容

普通下载与流式取源增加连续 300 秒有效字节无进度保护，覆盖消息读取与附件下载；旧协程真正退出后才按既有次数限制重试。正常慢速大文件没有总时长上限，重复计数不续时，明确监听冷却和流式下游背压单独处理。取消无法完成时保留名额并告警，不启动第二个写入者。

业务就绪增加聚合下载观察，停滞时 queue=failed、ready=false；容器心跳与传输健康分别判断。诊断区分等待下载名额和等待停滞下载回收，不覆盖显式重试、个人额度、预算及磁盘原因。旧快照兼容并把新观察显示为未核实，不输出私人文件名或凭据。

业务 schema 3、原配置、任务、下载和两个 Session 保留，无数据库迁移、新依赖或安装参数变化。一键脚本与独立 Compose 共用 Bot 核心，仍按各自安装、升级、备份与卸载边界操作。两网关提交和远端不确定结果保留策略不变；不更改网关或代理镜像。已核验原 v1.5.8 Tag 提交 `142176eb6a88e152cd2c54591af139fbe6be24fb`，生成可信升级基线，不自动执行下载的历史 Python。

## 本地验证

- `python -B .github/scripts/check_python.py all`：1664 项，1629 通过、35 条件跳过、0 失败；仓库检查、Ruff 和 compileall 通过。
- 下载保护与诊断专项 28 项全部通过，覆盖正常慢速、停滞、重复计数、明确冷却、流式背压、重复取消、有限重试、后续调度、两网关远端保留和脱敏。
- 裸 Python 3.10 专项 215 项，206 通过、9 条件跳过；宿主入口仍只用标准库。
- Bash／ShellCheck、Bandit、源码清单和网站 37 项测试通过。
- 干净公开副本组装两 Edition 一键脚本上下文与两份 format 3 Compose 候选包；各 143 个受管 Python 文件，Bot 核心一致，21 次裸 Python 3.10 入口帮助检查通过。合成镜像引用不等于正式镜像。

发布前本机没有原生 Linux Docker、rclone 和真实网关运行条件；相关条件跳过不算通过，原生检查由 GitHub CI 执行。下面另列发布后的用户实机结果，不用后续实机结论改写此前测试范围；故障回退和长期吞吐尚未执行。

## helper A、入口 B 与发布资产

辅助 A `16a51c86dd948fe897ace17e464e3ab2828ca953` 已公开，80 个 helper 文件的远端 Git blob 和 3 个匿名 raw 样本逐字节核验。入口 B 固定该真实不可变提交，不自指或使用 moving main。

A 的五组 CI 全部通过：[原生两架构](https://github.com/LuoPoJunZi/TG2Cloud/actions/runs/43686325993)、[Python 3.10／3.13 CLI](https://github.com/LuoPoJunZi/TG2Cloud/actions/runs/43686326044)、[仓库](https://github.com/LuoPoJunZi/TG2Cloud/actions/runs/43686326004)、[文档](https://github.com/LuoPoJunZi/TG2Cloud/actions/runs/43686325981)、[网站](https://github.com/LuoPoJunZi/TG2Cloud/actions/runs/43686325987)。[PR #34](https://github.com/LuoPoJunZi/TG2Cloud/pull/34)保留 A/B 审阅历史；入口 B `0bd9ec6c6c5a829a2f98c060c1d9f42a7486bff3` 的五组 CI 与同提交 main 检查通过。

[同标签原生双架构与源码发布](https://github.com/LuoPoJunZi/TG2Cloud/actions/runs/43687312562)、[Compose 原生镜像与部署包发布](https://github.com/LuoPoJunZi/TG2Cloud/actions/runs/43687618216)均成功。正式 Tag 指向 `0bd9ec6c6c5a829a2f98c060c1d9f42a7486bff3`，不移动旧标签或替换历史资产。

下载全部五份正式资产，核对 GitHub 资产 digest、两份 SHA256 清单与实际字节。源码 409 个文件逐字节匹配 Tag Git blob，包含新 watchdog；两份 format 3 包逐文件匹配同标签生成器，helper、模板、版本、提交及镜像一致。匿名读取 GHCR index、两份子 manifest 与配置，核对原生平台、1.5.9 版本和相同 revision。校验和不是数字签名。

共享镜像：`ghcr.io/luopojunzi/tg2cloud@sha256:d66e255bfffdb0c41bc0207b7eca4d1fda87ca6e75de46f701f7200c27c30353`。

| 资产 | SHA256 |
| --- | --- |
| `COMPOSE-SHA256SUMS.txt` | `ef902521ca6367bcb491d699c44b93cded926eb8a5290d69f7e23cf295b63613` |
| `SHA256SUMS.txt` | `4badad6f19d69c869c770b99cc92bf048e341d30f6c5feeff3ea6e3b60de30a1` |
| `TG2Cloud-v1.5.9-compose-clouddrive2.tar.gz` | `db8f8c86e8be6a1d9387f22b759a9c48bd3147ee3cfd7cfa41f70b40b16c40d4` |
| `TG2Cloud-v1.5.9-compose-openlist.tar.gz` | `baac7a894d93c6058ee57cb818667384448afe76e8db307e8ffb459eb99e7f2b` |
| `TG2Cloud-v1.5.9-source.tar.gz` | `453a535c490d855ac861a380db315833dba743606e580484b8cc5ab51a83cf99` |

- Linux AMD64 子镜像：`sha256:2b1e13bbd57dd3e416a1241ed15f559427319c4fbab4c285ca0275aeed346700`。
- Linux ARM64 子镜像：`sha256:9c8113c15efc860e660d7dab65bc7f2ea072ac2df11906386a536591035ebce1`。

以上是自动化与实际发布资产核验；发布后的用户实机验收单独记录如下。

正式资源包括：

- `TG2Cloud-v1.5.9-source.tar.gz`
- `SHA256SUMS.txt`
- `TG2Cloud-v1.5.9-compose-clouddrive2.tar.gz`
- `TG2Cloud-v1.5.9-compose-openlist.tar.gz`
- `COMPOSE-SHA256SUMS.txt`

## 用户实机验收（2026-11-15）

证据来自用户在对话中提供的升级日志、两次只读 readiness／diagnostics 输出，以及“实际转存已完成，验收通过”的明确确认。本次由用户登录并操作 VPS，维护者没有执行远程升级；公开记录仅保留脱敏摘要。

环境为 Debian 13、Linux（AMD64）、CloudDrive2 一键脚本实例，从 v1.5.8 保留配置升级至 v1.5.9。此结果不代表独立 Docker Compose、OpenList、其他架构或全新安装已完成同版实机验收。

| 项目 | 证据与结果 |
| --- | --- |
| 升级前保护 | 用户关键 SQLite 备份成功，升级器另创建并校验业务数据库一致性快照。 |
| 保留配置升级 | 可信 v1.5.8 基线与实例核验通过，`TG2CLOUD_CONFIG=PRESERVED`、`TG2CLOUD_CODE=OK`、`TG2CLOUD_RESULT=SUCCESS`；原 CloudDrive2 容器及登录、挂载数据沿用。 |
| Bot／CLI 版本 | 安装摘要与独立版本查询均确认 Bot、CLI 为 1.5.9，管理工具与目标一致。 |
| 容器运行 | `running`、`healthy`，`OOM=false`、重启次数 0；属于本次观察结果，不代表长期运行统计。 |
| 本机 HTTPS | 原管理入口本机自检通过，现有证书剩余约 37 天；没有将续期检查通过记为本次发生续签。 |
| 业务就绪 | 升级摘要保留了此前主动暂停状态；恢复调度后的两次快照五项检查均为 `ok`、`ready=true`。 |
| 监听健康 | 两次快照均为 8 个来源、`healthy`，无陈旧来源、补拉缺口、来源重试或当前冷却。 |
| 排队原因 | 首次 `queued=67`、`active=4`，后续 `queued=64`、`active=8`；下载窗口由 4 增至 8，上传窗口均为 1，等待原因均为 `download_slots`，没有再笼统归为其他原因。 |
| 下载保护观察 | 两次均有 `timeout_seconds=300`，`stalled=0`、`cancelling=0`、`oldest_idle_seconds=0`；保护观察已加载，本次没有触发停滞回收。 |
| 资源与失败诊断 | 两次容量检查通过，任务预算保持 12 GiB，后续快照磁盘可用约 27 GiB；诊断未列出最近 7 天的失败任务分类。 |
| 实际转存 | 用户明确确认实际转存完成、验收通过；未提供逐项回执，不拆分认定手动与监听、每个来源或媒体类型均已单独通过。 |

排队减少和窗口增加仅作为调度快照证据，不据此计算完成数量、吞吐或固定并发上限；实际完成结论来自用户确认。大小复验也不等于内容哈希验证或云盘官方最终入库确认。

日志中的磁盘总容量低于推荐值为非阻断提醒，本次硬性容量检查通过。Bot 日志轮转已生效；旧网关、共享 Nginx 和 Certbot 未采用项目轮转限额属于旧容器配置状态，不列为本次新增程序缺陷或已完成的维护操作。

本版以下实机项目尚未执行或未获独立确认：

- 300 秒停滞后的取消、回收、有限重试和后续任务接续等故障路径；本次正常转存不代替这项验收。
- OpenList、独立 Compose、Linux ARM64、全新安装及可信内网部署。
- 单独的 WebDAV 随机测试文件验收、公网入口、手机 Range、新证书签发和长期续期。
- 数据库 schema 值的单独查询、隔离恢复、故障回退、内容哈希、云盘官方最终入库及长期负载。

## 其他环境的实机验收顺序

1. 核对正式 Release、源码、镜像与两版包已齐，记录原 Bot／CLI 版本、部署方式和下载进度。先暂停新任务放行；已有卡住任务不直接删除、改库或批量重试。
2. 使用原部署方式导出私密一致性备份，包含配置、数据库与旁文件、两个 Session、必要网关／代理配置。旧协程尚活动时，先检查取消与保留范围；需要停 Bot 时先保存现场，不让同 Token 的两个 Bot 同时运行。
3. 公网脚本实例运行 `bash <(curl -fsSL https://raw.githubusercontent.com/LuoPoJunZi/TG2Cloud/main/install.sh) --edition clouddrive2 --release v1.5.9`；OpenList 更换 Edition，可信内网仍用 install-lan.sh，不切换部署模式。
4. 独立 Compose 下载核验完整本版包，用新包的 `bash manage.sh --root /原Compose目录` 进入“安装与升级”，选择 v1.5.9；保留原数据和配置，不用脚本接管。format 2 与早期 format 1 的限制继续见教程。
5. 核对 Bot、宿主 CLI／离线包均为 1.5.9，数据库仍为 schema 3，原授权、监听来源、上传目录和历史任务保留。业务就绪应有 transfers 聚合观察，正常忙时明确等待下载名额。
6. 用自有小文件分别测试原手动转存和管理员监听，等待从下载到完成，核对网关目录、大小及可读性，再到云盘官方客户端确认。两种网关和两种部署各自记录，未测项目继续列为未验收。
7. 正常慢速、明确 Telegram 冷却与播放器访问不应误判。仅在隔离的模拟来源测试无进度：超过阈值后看到回收／有限重试，旧协程退出后后续任务可开始；不主动破坏真实 Telegram 网络或生产网关制造故障。
8. 异常时收集版本、任务状态、聚合 readiness／diagnostics 和脱敏日志；不得公开文件名、地址、凭据、Session 或数据库。新 Bot 可能写库后失败先保留现场，不自动覆盖新库；回退仅用匹配代码和已验证快照。

没有数据库迁移不等于可以省略备份。实际 WebDAV 大小复验不等于内容哈希或云盘官方最终入库；SDK 不响应取消或下游自身卡住也不能宣称由取源保护强制修复。

[一键脚本](../manuals/vps-install.md) · [Compose](../manuals/compose.md) · [Compose 管理](../manuals/compose-management.md) · [任务恢复](../manuals/recovery.md) · [备份恢复](../manuals/backup-restore.md)
