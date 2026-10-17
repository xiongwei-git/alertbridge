# 静态网站构建与部署

正式地址：

- 项目集合：<https://opensource.tedxiong.com/>
- AlertBridge：<https://opensource.tedxiong.com/alertbridge/>

网站由宝塔 Nginx 直接提供静态文件，不运行 Go 服务、不反向代理通知 API。GitHub 保存源码、执行 CI 并构建镜像，镜像仅发布到 ACR；GitHub Pages 已停止使用。

## 构建与本地预览

```sh
./scripts/build-site.sh
python3 -m http.server 18083 --bind 127.0.0.1 --directory dist-site
```

`site/collection.html` 生成根目录首页，`site/index.html` 与 CSS/JS/SVG 放入 `/alertbridge/`。构建脚本只复制白名单文件并生成 `SHA256SUMS`，没有 Node.js、外部字体或 CDN 依赖。

浏览器 Demo 的状态仅在内存中维护，使用虚构事件，不请求生产 API、不保存凭据、不演示真实 OIDC 或通知投递。实际功能测试使用隔离 Docker E2E 或自己的本地 Demo。

## 发布

1. 执行网站检查和构建，审阅集合首页与项目页。
2. 在静态站专用目录创建新的 `releases/<标识>` 目录，将 `dist-site/` 内部文件复制进去，不能把整个仓库上传到网站根目录。
3. 在新目录执行 `sha256sum -c SHA256SUMS`，确认每个文件完整。
4. 记录站点当前根目录，备份当前 Nginx/rewrite 配置。通过宝塔 HTML 项目的“网站根目录”切换到新 release，保留之前目录。
5. 执行 `nginx -t`，通过后检查公网首页、项目页及全部资产。确认返回内容的 SHA-256 与本次产物一致。

切换已有站点根目录时保留域名、证书和现有 HTTPS 设置，不重建站点。该操作不需要重新部署通知容器、身份提供方、数据库或主密钥。

## Nginx 规则

下面的规则放在此 HTML 站点的 rewrite 配置入口，修改前先备份。证书仍由宝塔管理：

```nginx
location / {
    try_files $uri $uri/ =404;
}
add_header X-Content-Type-Options nosniff always;
add_header Referrer-Policy strict-origin-when-cross-origin always;
add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'" always;
```

这是普通多文件静态站，不使用 SPA fallback。`/admin/`、`/api/v1/events`、`/mcp` 在此域名应返回 404；网站链接到代码和文档，不代理这些接口。不要将通知服务的 OIDC callback 或 OAuth 发现代理配置复制到这里。保留宝塔的 ACME 验证路径。

HTML 不应长时间缓存。修改固定文件名的 CSS/JS 后，核对浏览器是否仍使用旧缓存；版本更新时可以使用构建版本作为资源 URL 的查询参数。

## 验收与回滚

检查桌面和窄窗口下的导航、接口表格与代码块；模拟故障 → 重复事件 → 恢复 → 查询 → 重置。缺失文件应为 404，页面不应向通知服务发请求。HTTPS 应命中此站点，不能仅以 HTTP 200 作为成功依据。

如果产物或页面验证失败，通过宝塔将根目录切回上一 release，再复查页面与资源；不要删除旧目录或更改生产数据卷。原站点配置只在本次确有配置改动且回滚需要时恢复。

## 集合扩展

目前只收录 AlertBridge。新增项目时单独核对公开仓库、版本与功能，再更新 `site/collection.html`。集合站暂与 AlertBridge 源码一同维护；其他项目增加后可迁移到独立仓库，不在本仓库引入其他项目运行数据。

## 运行服务的公开根路径介绍页

从 v0.5.1 起，Go 服务使用 `go:embed` 内嵌 `internal/httpapi/static/service-home.html`，Docker 镜像自动携带该页面。匿名 `GET /`、`HEAD /` 返回介绍页，无需登录、静态文件挂载或额外 Nginx 规则。页面不创建会话、不读取业务数据，只链接官网、文档及原后台入口。

只注册精确根路径，不提供目录文件服务或 SPA fallback。未知路径仍为 404；原 API、MCP、OAuth/OIDC 与后台认证保持不变。样式 SHA-256 CSP 由服务根据内嵌内容计算，仅作用于根页，不允许脚本或放宽全站策略。修改页面源码后重新构建镜像即可，不需要手动维护样式摘要。

### 旧独立 Nginx 根页迁移

1. 先升级到 v0.5.1 或更新的兼容镜像，并直接通过本机 loopback 根地址确认已返回介绍页。
2. 仅将之前新增的根页扩展文件（例如 `extension/站点名/public-home-20261017.conf`）改为不以 `.conf` 结尾的归档名。不要删除原主反代配置、OAuth 发现或 OIDC 回调规则。
3. 执行 `nginx -t`，成功后平滑重载。现有根路径代理会自动把 `/` 转发给服务。
4. 对比公网 HTML 与服务内嵌产物，复查后台登录、OAuth 发现、通知 API 与 MCP 认证。旧静态 HTML 可留存作为回退副本，不再参与服务。

该变更没有数据库迁移，不修改身份提供方、认证文件或业务配置。回滚旧镜像时，可重新启用归档的独立根页扩展，再验证语法与重载。独立官网和模拟 Demo 仍按前面的静态站流程部署，与服务根页是两个不同产物。
