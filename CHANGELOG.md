# Changelog

## Unreleased

- 按 v0.5.0 更新中英文介绍、架构说明、后台指南、安全政策和部署示例。
- 补充 OIDC 与 MCP OAuth 的职责区分，以及 OIDC overlay 的日常管理、备份和回滚步骤。
- 重写官网技术说明，统一使用 `opensource.tedxiong.com`，停止 GitHub Pages 发布。
- 本轮仅修改文档、静态网站和网站校验；应用版本、API 行为和数据库结构不变。

## v0.5.1 — 2026-10-17

- 公开项目介绍页通过 Go embed 内嵌，Docker 镜像直接提供 `GET /` 和 `HEAD /`，不再需要独立 Nginx 页面配置。
- 根页不创建会话、不读取业务数据；采用独立 CSP 样式哈希，未知路径、通知 API、MCP、OAuth/OIDC 与后台认证保持原契约。
- 增加匿名根页、方法/路径隔离、CSP 和真实 Docker 镜像回归测试；无需数据库迁移或新增依赖。

## v0.5.0 — 2026-10-17

- 新增可选管理员 OIDC 登录，使用 Authorization Code、PKCE S256、state、nonce 和 RS256 ID Token 验证。
- 按受限服务器配置中的 issuer + subject 授权管理员；不按用户名、邮箱或首次登录自动授权。
- 新增文件型 Secret overlay `compose.oidc.yaml`；基础部署继续使用本地密码。
- 登录页等待显式点击，可保留折叠的备用密码入口；退出仅清除本应用会话。
- 不持久化身份提供方的 access/refresh token。通知接口、MCP OAuth 和原业务配置保持兼容。

## v0.4.2 — 2026-10-17

- MCP `list_alerts` 支持 `order=desc`；默认正序和历史游标保持兼容。

## v0.4.1 — 2026-10-17

- 修复 Dots OAuth 授权页 CSP 拦截配置回调的问题。

## v0.4.0 — 2026-10-17

- 新增按路由授权的只读 MCP 工具和 Dots OAuth + PKCE。

更早版本见 [GitHub Releases](https://github.com/xiongwei-git/alertbridge/releases)。
