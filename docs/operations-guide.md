# TGCloud 日常维护

脚本部署运行 `sudo tgcloud`，独立 Compose 进入部署目录运行 `sudo bash manage.sh`。菜单检查来源与实例归属后操作。

## 管理菜单

| 操作 |
| --- |
| 1. 安装与升级 |
| 2. 查看运行状态 |
| 3. 查看运行日志 |
| 4. 服务启停管理 |
| 5. Bot 用户管理 |
| 6. WebDAV 凭据 |
| 7. HTTPS 与管理 |
| 8. 文件分类设置 |
| 9. 异常通知设置 |
| 10. 业务就绪检查 |
| 11. WebDAV 验收 |
| 12. 故障诊断报告 |
| 13. 数据备份管理 |
| 14. 卸载当前实例 |
| 15. 外部 WebDAV |
| 16. 监听管理 |
| 0. 退出 |

## 服务、日志与诊断

“4. 服务启停管理”分别控制 Bot 和存储网关；停止网关会中断存储和外部读取。脚本部署的底层管理入口包括：

```bash
sudo bash /opt/tgcloud/manage.sh status
sudo bash /opt/tgcloud/manage.sh logs
sudo bash /opt/tgcloud/manage.sh check
```

容器健康不等于业务就绪。先看检查时间、目标目录和资源余量，再执行 WebDAV 验收或诊断报告。导出日志前仍需检查脱敏结果，不公开 Token、Session、授权配置或完整私有文件名。

## 源码升级

获取完整 TGCloud 源码，通过“1. 安装与升级”检查候选版本和源码摘要，核对计划、配置与活动任务后切换。同版本源码变化也需要确认。先创建独立备份，保留失败现场；不要清空数据库来消除报错。

在完整源码目录运行 `sudo bash install.sh` 时自动使用该源码作为候选。已安装的管理工具只携带维护模块；如从管理命令执行升级，使用 `sudo tgcloud manage --source-dir /absolute/TGCloud` 明确指定完整源码，无需在菜单中重复填写。

## 卸载

先预览，再选择保留或清理数据：

```bash
sudo tgcloud uninstall --check
sudo tgcloud uninstall --keep-data
```

`--purge-data` 会清理当前实例本地数据，`--purge-all` 还包含核对后的受管备份，均要求额外确认。云端文件不由项目卸载清理。共享代理与外部访问依赖必须先处理。

自定义安装目录的删除必须通过完整归属检查；主菜单不会猜测该清理哪个目录。独立 Compose 使用其管理菜单卸载，避免使用跨项目 Docker prune 命令。

`prune-backups 1～50` 会直接删除已核对范围内超出保留数量的备份；执行前先保存独立副本。
