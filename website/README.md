# TGCloud Docs

当前文档为 TGCloud 0.0.1，内容核对日期 2026-11-16，尚未发布。当前仅支持 CloudDrive2，新项目的正式下载、CI、生产网站和实机验收没有由文档整理自动完成。

## 本地使用

Node.js 22+，无第三方 npm 依赖：

```bash
npm run build
npm test
npm run preview
```

预览只监听本机，网站本身不会部署 Bot。`SITE_URL` 未配置时不生成虚构的 canonical 或 sitemap。

## 文件与历史

当前文章在 `docs/`，模板在 `src/`，静态资源在 `public/`，生成物在被忽略的 `dist/`。原站文章、一致性合同和旧验收记录已移入[项目历史档案](../archive/README.md)，不混入当前导航。

项目链接为 [LuoPoJunZi/TGCloud](https://github.com/LuoPoJunZi/TGCloud)，保留 MIT、原版权及 TG115 上游致谢。不直接修改 `dist/` 或提交凭据。
