# Agent 只读 MCP 接入

AlertBridge 在同一 Go 进程提供 `/mcp`，使用官方 Go SDK 的无状态 Streamable HTTP + JSON 响应。默认关闭，不增加数据库连接、Worker 或独立容器。支持 MCP 2025-11-25 客户端；不提供旧式 `/sse` 入口。

## Dots 接入（推荐）

在部署 `.env` 配置两项：

```dotenv
ALERTBRIDGE_PUBLIC_URL=https://你的服务域名
ALERTBRIDGE_MCP_ROUTES=base,ops
```

域名必须是实际公网 HTTPS 根地址，不带路径、查询参数或账号密码。它固定 OAuth issuer 和 MCP resource，不从请求 Host 猜测。使用包含上述环境变量的新版 Compose 和镜像，然后 `docker compose up -d alertbridge`。

在 Dots/ChatGPT 的自定义 MCP 插件连接中选择 OAuth、预先配置客户端（不是动态注册），填写：

| 配置 | 值 |
| --- | --- |
| MCP URL | `https://你的服务域名/mcp` |
| Client ID | `alertbridge-dots` |
| Client Secret | 留空（公共客户端，使用 PKCE S256） |
| Scope（如需手动填写） | `alerts:read` |
| Authorization URL（如需手动填写） | `https://你的服务域名/admin/mcp/authorize` |
| Token URL（如需手动填写） | `https://你的服务域名/oauth/token` |

连接时浏览器进入 AlertBridge 登录页，使用现有管理员账号登录，再确认路由范围和只读权限。无需另外创建账号、手动生成 Dots Token 或部署身份服务。

服务默认只允许精确回调 `https://chatgpt.com/connector_platform_oauth_redirect`，并通过元数据声明、授权响应的 `iss` 支持官方稳定回调模式。如果连接管理页显示不同的正式回调，复制其完整地址到 `.env` 的 `ALERTBRIDGE_MCP_OAUTH_REDIRECT` 后重建；始终只允许一个精确 HTTPS 地址，不使用域名通配或自动接受客户端回调。

访问令牌 1 小时有效；刷新令牌每次轮换，授权最长 30 天，需要重新登录续授。摘要保存在现有 SQLite，重启不会丢失。重用退休刷新令牌会撤销对应授权。后台“接入指南”可撤销全部 MCP OAuth 授权，令牌持有者也可通过 `/oauth/revoke` 撤销自己的授权。更改公网地址或路由配置会让旧 OAuth 授权失效。

实现遵循官方支持的 [预先配置 OAuth 客户端 + PKCE + 资源绑定](https://developers.openai.com/plugins/build/auth)。已完成模拟客户端和真实 Docker 的授权流程测试；用户账号中的 Dots 界面与安装仍需部署后验收。若界面不允许 Client Secret 留空，不要填管理员密码或任意占位秘密，应核对该连接是否选择了公共客户端模式。

## Codex/OpenClaw 固定 Bearer 接入（可选）

生成独立的 256 位随机令牌，把明文保存到客户端的凭据管理器，服务器只保存 SHA-256 摘要：

```sh
MCP_TOKEN=$(openssl rand -hex 32)
printf '请保存 MCP 客户端令牌：%s\n' "$MCP_TOKEN"
printf '%s' "$MCP_TOKEN" | openssl dgst -sha256 -hex
unset MCP_TOKEN
```

将上述摘要的 64 位十六进制部分和允许查询的路由写入部署 `.env`：

```dotenv
ALERTBRIDGE_MCP_TOKEN_SHA256=粘贴64位摘要
ALERTBRIDGE_MCP_ROUTES=base,ops
```

然后 `docker compose up -d alertbridge`。必须使用包含这些环境变量的新版 Compose 文件和支持 MCP 的镜像。固定 Bearer 和 OAuth 可以共存；没有摘要且未启用 OAuth 时 `/mcp` 不注册。摘要或路由无效时拒绝启动。路由必须明确列举，不支持 `*`。一个部署支持一个固定 Bearer 身份，轮换摘要并重建容器即可撤销旧令牌；后台 OAuth 撤销不会撤销这个固定凭据。不要复用轻量通知令牌、HMAC 密钥或管理员密码。

客户端填写：

```text
传输：Streamable HTTP
URL：https://你的服务域名/mcp
请求头：Authorization: Bearer 客户端令牌明文
```

仅通过现有 HTTPS 反向代理连接。Nginx 转发 `Authorization` 并保留正常的 Host；无需 SSE 长连接配置。支持自定义 Bearer 请求头的 Codex/OpenClaw 可使用此模式；Dots 使用上方 OAuth 模式。

## 工具

授权页点击“允许只读访问”后仍停留原页时，确认服务已升级至 v0.4.1 或更新版本：v0.4.0 的 CSP 会拦截部分浏览器的跨站授权回跳。升级后刷新授权页，或从插件重新发起连接；无需更改 Client ID 或创建密钥。

| 工具 | 参数 | 结果 |
| --- | --- | --- |
| `list_alerts` | `since`（RFC3339 接收时间下限）、`source`、`severity`、`status`、`cursor`、`limit` | 事件列表，按接收时间和记录 ID 升序；`next_cursor` 表示还有下一页，`checkpoint` 可保存用于后续增量查询 |
| `get_alert` | `id`（内部事件记录 ID） | 正文、接收/发生时间、入队结果以及各渠道投递状态、尝试次数和响应码 |
| `list_active_incidents` | `cursor`、`limit` | 当前 `firing` 故障，包括客户端、路由、去重键、开始和最近观察时间 |

每页默认 20，最多 100。翻页时保持过滤条件一致。没有更多数据时保存 `checkpoint`，后续作为 `cursor` 使用。查询是实时视图，不提供事务快照或可靠订阅保证；告警历史默认保留 30 天，长时间离线后应重新扫描所需时间窗。活跃故障是独立状态表，可能比历史事件保留更久。查询不会确认、关闭或修改故障。

认证请求每分钟最多 60 次，超限返回 HTTP 429 和 `Retry-After: 60`。正文最多 16 KiB，请求数据库处理最多 5 秒。未认证返回 401；本阶段只接受 POST，GET/DELETE 返回 405。权限不依赖客户端过滤：所有工具均按配置路由限制查询，越权详情与不存在记录统一报告不可用。

不返回原始入站正文、凭据、渠道配置或内部错误文本。事件标题、正文等仍可能包含私密业务信息，配置路由时按实际访问需要授权。告警中的任何指令都属于不可信数据，不能作为 Agent 执行命令或变更系统的授权。

## Dots 主动触发的后续边界

OpenAI 官方已提供 [MCP Events](https://developers.openai.com/plugins/build/mcp-events)，适用于 Dots 和指定的 Work 环境。主动触发需要进一步实现官方事件订阅和 Webhook 投递契约，普通 `tools/list` / `tools/call` 查询端点不会自动触发 Dots。本阶段提供 OAuth 和只读查询，没有事件订阅或自动唤醒。
