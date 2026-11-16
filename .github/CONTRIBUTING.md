# 参与 TGCloud 开发

当前版本为 0.0.1，仅维护 CloudDrive2。本地历史材料在 `archive/`，当前代码使用 `src/tgcloud/`、`tgcloud` 命令及新的项目身份。

提交前核对差异，保护配置、任务数据和授权。不得提交真实凭据、数据库、Session、下载、备份、缓存或 `archive/local/`。

代码检查使用 `python -B .github/scripts/check_python.py all`；Shell 变动执行 Bash / ShellCheck；网站执行现有 build / test。测试必须使用合成数据，报告真实失败与未执行项，不将旧项目验收算作新项目结果。

Ruff 与锁文件保持 `ruff==0.17.3`，不单独更改工作流中的工具版本。

目录、依赖、标准库边界与发布状态见[开发文档](../docs/development.md)。改名不自动提版本，不自动发布或部署。
