# TGCloud 开发与文件组织

版本唯一来源为 `src/tgcloud/version.py`，当前 0.0.1。宿主 CLI 保持 Python 3.10+ 标准库边界，Bot 与开发依赖在 `pyproject.toml` 和 `requirements/` 中定义。

## 目录

| 路径 | 用途 |
| --- | --- |
| `src/tgcloud/` | Bot、CLI、部署、传输、存储和持久化 |
| `install.sh`、`install-lan.sh` | 本地源码的轻量 Linux 入口 |
| `packaging/` | 镜像、CloudDrive2 Compose、钩子与清单 |
| `scripts/` | 受控源码入口和本地包生成 |
| `tests/` | 当前回归、合成夹具与按环境启用的集成检查 |
| `docs/` | TGCloud 当前文档 |
| `website/` | TGCloud 0.0.1 静态文档站源码 |
| `assets/brand/` | 当前图标与 TGCloud 字标 |
| `archive/project-history/` | 原项目公开记录、教程及历史验收 |
| `archive/local/` | 私有维护记录和历史产物，Git 忽略 |
| `build/` | 当前生成物，Git 忽略 |

## 验证

```bash
python -B .github/scripts/check_python.py all
python -B .github/scripts/prepare_bootstrap.py
bash -n install.sh
bash -n install-lan.sh
```

在 `website/` 执行 `npm run build` 和 `npm test`。测试只使用合成数据或明确隔离的资源。成功场景使用 CloudDrive2；OpenList 和其他项目场景验证明确拒绝、凭据不读、旧数据与资源不动。

2026-11-16 的完整检查 `check_python.py all` 执行了 1,660 项测试，失败和错误均为 0，随后 Ruff 与编译通过。该次运行的 35 项原有环境条件项中，另有 4 项随后迁移为本机可执行的 Bash 入口、退休网关拒绝和宿主前置检查并补跑通过；当前还需特定环境的为 31 项，没有新增跳过规则。测试方法数量从 1,659 增加到 1,660。

完整原始输出、逐文件迁移台账、失败对照与补充验证在被忽略的 `archive/local/regression-migration/` 中保存。此前改名阶段的失败记录仍保留在 `archive/local/maintenance/`，未覆盖或删去。

Windows 本机没有 Linux、Docker 和 rclone 验收环境。已有 POSIX、真实 PTY、Docker、FUSE、Nginx 和真实 rclone 等条件限制继续如实保留；本地回归不代表真实 CloudDrive2、Telegram、网盘、公网 HTTPS 或长期稳定性验收。

## 来源与发布

当前仓库名称为 TGCloud；仓库链接使用 `https://github.com/LuoPoJunZi/TGCloud`。保留 [MIT 许可证](https://github.com/LuoPoJunZi/TGCloud/blob/main/LICENSE)、原版权和上游致谢。历史提交、发布 SHA 与原项目事实留在档案中，不重新标成新项目的发布成果。

未建立并验证新项目不可变来源、CI、镜像和发布资产前，只使用本地源码。未经明确授权不提交、推送、发布或部署。
