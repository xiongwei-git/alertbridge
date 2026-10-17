# ADR-011: Dots 的最小 OAuth 适配

## Status

Accepted — 2026-10-17

## Context

Dots 的云端连接不能直接沿用本地客户端的固定 Bearer 输入方式。需要兼容 OpenAI 官方支持的 OAuth 授权码流程，同时保留轻量单实例部署。已有管理员会话足以确认谁有权向 Agent 授予读取权限。

## Decision

- 在现有 Go 进程实现范围明确的预配置公共 OAuth 客户端 `alertbridge-dots`，仅支持授权码 + PKCE S256、`alerts:read`，不新增账号系统或外部身份服务。
- 不实现 DCR、任意 CIMD URL 抓取、第三方 IdP、多租户、JWT、客户端秘密或通配回调。官方元数据公布 `none` 客户端认证；客户端秘密必须留空。
- `ALERTBRIDGE_PUBLIC_URL` 固定 HTTPS issuer/resource；与明确的 `ALERTBRIDGE_MCP_ROUTES` 一起启用 OAuth。回调地址严格匹配，默认 OpenAI 稳定回调，通过 RFC 9207 `iss` 响应保护，必要时由部署者复制官方连接页的精确地址覆盖。
- 授权确认页在 `/admin/mcp/authorize`，复用原 `/admin` Cookie 和 CSRF；只允许登录页返回这一个本地路径。GET 不签发授权码，POST 明确同意才签发。
- 授权页 CSP 的 `form-action` 只允许本站和配置的精确回调地址；Chromium 也会检查表单提交后的重定向链，仅允许 `'self'` 会让授权按钮看似无反应。保留默认资源禁止、禁止嵌入和 `base-uri 'none'`，不放行通配域名或脚本。
- 授权码有效 5 分钟、单次使用，绑定 PKCE、回调和资源。SQLite 事务原子消费授权码与创建授权。
- 不透明访问令牌有效 1 小时，刷新令牌轮换、授权最长 30 天。重用历史刷新令牌撤销整个授权。所有令牌和授权码只存 SHA-256 摘要。
- 每次查询核对当前路由/资源策略。改变该配置会使已有 OAuth 授权失效。访问限流按授权独立计数，令牌端点全局 60 次/分钟。
- 后台提供带现有 CSRF 保护的全部授权撤销；RFC 7009 风格撤销端点只撤销令牌所属授权。原固定 Bearer 模式仍可选。

## Alternatives Considered

外部身份服务或完整 OAuth 框架会带来额外部署、配置和存储成本；当前单管理员、固定一个客户端的需求不需要它们。匿名 MCP 无法保护告警内容。动态注册和 CIMD 可以后续按真实连接需求增加，本次固定客户端避免网络抓取和注册管理。

## Consequences

没有新增运行进程、数据库连接或 Go 依赖；增加三张小表保存授权状态。数据库与密钥备份边界不变。已通过本地协议和 Docker 集成测试；真实 Dots 账号安装要在发布部署后确认，不能用模拟测试代替。主动触发 MCP Events 仍不在本次范围内。
