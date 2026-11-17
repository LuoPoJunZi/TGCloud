# TGCloud 开发与架构

## 目录与职责

| 路径 | 职责 |
| --- | --- |
| `src/tgcloud/cli/`、`deployment/` | 标准库宿主 CLI、安装与维护 |
| `src/tgcloud/telegram/` | Bot 事件、界面、命令与授权 |
| `src/tgcloud/domain/` | 任务规则、分类、命名和规划 |
| `src/tgcloud/transfer/`、`storage/` | 队列、资源窗口、下载、流式与 rclone |
| `src/tgcloud/persistence/` | SQLite、任务与配额、快照和隔离演练 |
| `src/tgcloud/listening/` | 个人账号授权、来源与筛选 |
| `src/tgcloud/compose/`、`addons/` | 独立 Compose、HTTPS 和外部访问 |
| `packaging/` | Dockerfile、CloudDrive2 Compose、受管钩子与源码清单 |
| `scripts/`、`.github/scripts/` | 源码入口、打包和质量工具 |
| `tests/` | 合成回归及按环境显式启用的集成测试 |
| `docs/` | 安装、使用、维护和开发文档 |

Telegram 事件进入持久化任务和计划，经资源窗口调度下载／流式传输，再由 rclone 写入 CloudDrive2 WebDAV。宿主 CLI 延迟导入，帮助和版本检查不要求 Bot 依赖。版本唯一来源是 `src/tgcloud/version.py`，当前为 0.0.2。

## 开发环境

推荐 Python 3.12，与 Bot 镜像一致；宿主 CLI 保持 Python 3.10+。安装锁定的开发依赖后，以可编辑方式安装当前包：

```bash
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -r requirements/dev.lock.txt
python -m pip install --no-deps -e .
```

rclone、Bash、ShellCheck 和 Docker 是相关检查的外部工具。不要将真实配置、Session、数据或下载放入测试夹具。

## 检查与构建

```bash
python -B .github/scripts/check_python.py all
python -B .github/scripts/prepare_bootstrap.py
python -B .github/scripts/maintenance_preflight.py --json
python -B .github/scripts/linux_validation.py shell
```

源码清单变动经过审阅后运行 `python -B .github/scripts/prepare_bootstrap.py --write-manifest`。文档索引和文件链接由仓库检查及现有回归验证。Compose 构建见[部署指南](compose-guide.md)。

Linux CI 覆盖 AMD64／ARM64、Python 3.10／3.13 宿主检查、真实 rclone 和隔离 Docker。仅在所需环境中启用相关集成测试；本机缺少条件时如实报告，不将跳过项目描述为通过。源码检查不等于生产、网盘或长期稳定性验收。

## 命令边界

`python3 -B scripts/run_source.py --help` 列出 `install`、`lan`、`manage`、`compose`、`proxy`、`webdav`、`listen`、`uninstall` 和 `naming-preview`。子命令使用自己的 `--help`；部署变更需要实例归属检查和必要确认。

构建、测试和本地文档操作不会自动创建 Release。提交、推送、标签、镜像发布与部署按维护者明确指令执行。
