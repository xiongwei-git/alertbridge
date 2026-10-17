# 个人开源集合与 AlertBridge 官网部署

目标域名：`opensource.tedxiong.com`。集合首页 `/`，AlertBridge 项目页及模拟 Demo `/alertbridge/`。源码 `site/`，生成目录 `dist-site/`（含 `SHA256SUMS` 逐文件校验清单），无 Node、构建依赖、CDN、外部字体或数据服务。

## 本地预览与打包

```sh
./scripts/build-site.sh
python3 -m http.server 18083 --bind 127.0.0.1 --directory dist-site
```

访问 `http://127.0.0.1:18083/` 与 `/alertbridge/`。打包时只复制白名单文件，不包含仓库其他内容、环境文件、数据库或凭据。不要把整个仓库作为网站根目录。

## GitHub Pages

GitHub Pages 托管 HTML/CSS/JavaScript，不能运行 Go/SQLite。本站的交互仅为浏览器模拟。依据：[GitHub Pages 官方说明](https://docs.github.com/en/pages/getting-started-with-github-pages/what-is-github-pages)，核对日期 2026-10-17。

1. 将网站和 `.github/workflows/pages.yml` 合并到仓库默认分支。
2. 仓库 Settings → Pages → Build and deployment，Source 选择 GitHub Actions。
3. Actions → Publish open source showcase → Run workflow。工作流只手动触发，和镜像发布、生产部署无关联。
4. 打开工作流输出的 `page_url`；项目页是此地址再加 `alertbridge/`。例如标准项目 Pages 路径为 `https://xiongwei-git.github.io/alertbridge/alertbridge/`，集合页为 `https://xiongwei-git.github.io/alertbridge/`。
5. 验证集合链接、Demo 故障/重复/恢复/重置、移动端和页面资源路径。

本轮不设置 Pages 自定义域名，避免与宝塔的 `opensource.tedxiong.com` DNS 冲突。GitHub Pages 作为可独立访问的静态演示镜像；正式个人开源集合以宝塔域名为入口。

## 宝塔独立静态站

域名尚未解析时，先准备产物，不修改现有通知站点。

1. 在 DNS 服务商为 `opensource.tedxiong.com` 设置指向目标宝塔服务器的 A/AAAA 记录，只有服务器支持 IPv6 时才添加 AAAA。
2. 宝塔新建独立 HTML 静态网站，单独根目录，例如 `/www/wwwroot/opensource.tedxiong.com`，不要复用通知服务目录或反向代理。
3. 备份该静态站旧产物（若存在），将 `dist-site/` **内部文件** 上传到新 release 目录，再切换该站点根目录。保留上一 release 用于回滚。
4. 为新域名申请证书、启用 HTTPS；以下配置仅应用于新静态站，不改通知服务 Nginx 配置。

```nginx
location / {
    try_files $uri $uri/ =404;
}
add_header X-Content-Type-Options nosniff always;
add_header Referrer-Policy strict-origin-when-cross-origin always;
add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'" always;
```

这是静态站，不需要 SPA fallback 或 API 反向代理。确保静态站不代理 `/admin`、`/api`、`/mcp`。`nginx -t` 通过后平滑重载。验收根路径、`/alertbridge/`、CSS/JS/SVG 资源、缺失路径 404、HTTPS 和浏览器交互，再以文件 SHA-256 对照产物。回退只切回旧静态目录，不触碰运行服务和数据卷。

## 扩展集合

目前只收录 AlertBridge。其他项目核实公开状态与介绍后再新增条目。未来应将集合站移到独立仓库；本轮保留在 AlertBridge 内以便审阅和发布同一产物，且不引入其他项目的源码或私密资料。

## 本次部署状态（2026-10-17）

- [集合首页](https://xiongwei-git.github.io/alertbridge/) 与 [AlertBridge Demo](https://xiongwei-git.github.io/alertbridge/alertbridge/) 已通过 Pages 工作流发布。
- 宝塔站点 `opensource.tedxiong.com`，根目录 `/www/wwwroot/opensource.tedxiong.com/releases/20261017-showcase`。服务端 Host 路由验收：五个公开静态文件 HTTP 200、逐文件 SHA-256 匹配，CSP 与缺失路径 404 通过。
- 初始默认首页与新站配置备份在 `/www/wwwroot/opensource.tedxiong.com/backups/20261017-initial`。
- 公共 DNS 查询当前返回 NXDOMAIN；需要维护者添加指向宝塔服务器的 A 记录，再申请证书并验收公网 HTTPS。不能将 Host 路由验收称为公开域名上线。
- 原通知服务仅做就绪检查，未修改运行镜像、Compose、数据卷或凭据。
