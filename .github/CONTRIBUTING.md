# 参与 TGCloud 开发

先阅读[架构与开发说明](../docs/development.md)，保持 CloudDrive2 单网关范围、Python 标准库宿主入口和现有模块边界。对功能、路径或配置的变更，应同时更新对应文档与有实际意义的回归。

使用合成数据或隔离资源测试，不提交真实凭据、Session、数据库、下载、备份或构建产物。Python 检查使用 `python -B .github/scripts/check_python.py all`；Shell 使用 Bash／ShellCheck；文档检查覆盖文件引用、索引和 Release Notes 格式。

Ruff 由 `pyproject.toml` 和 `requirements/dev.lock.txt` 共同约束，CI 从锁文件读取工具版本。报告真实失败和环境未执行项，不用跳过、宽松断言或删除有效测试掩盖问题。

提交说明应包含问题、修改后的行为、验证和限制。版本发布、镜像发布、推送及部署需要维护者明确授权。
