# Agent MCP 查询与事件订阅

AlertBridge 在同一 Go 进程、同一认证 `/mcp` 同时提供**告警查询工具**与**可选的新告警事件订阅**。统一使用 MCP 2.0（`2026-07-28`）的无状态 HTTP 请求；不使用 `initialize`，不改写协议版本，也没有旧版 SDK 转发。默认关闭 MCP，Events 另行显式启用；不增加数据库连接、Worker 或容器。

## Dots 接入

在部署 `.env` 配置两项：

```dotenv
ALERTBRIDGE_PUBLIC_URL=https://你的服务域名
ALERTBRIDGE_MCP_ROUTES=base,ops
```

域名必须是实际公网 HTTPS 根地址，不带路径、查询参数或账号密码。它固定 OAuth issuer 和 MCP resource，不从请求 Host 猜测。使用包含上述环境变量的 v0.5.0 Compose 和镜像，然后 `docker compose up -d alertbridge`。启用管理员 OIDC 时，保留 `.env` 的 `COMPOSE_FILE` 或显式加载 `compose.oidc.yaml`，见 [部署指南](deployment.md#9-compose-文件选择)。

在 Dots/ChatGPT 的自定义 MCP 插件连接中选择 OAuth、预先配置客户端（不是动态注册），填写：

| 配置 | 值 |
| --- | --- |
| MCP URL | `https://你的服务域名/mcp` |
| Client ID | `alertbridge-dots` |
| Client Secret | 留空（公共客户端，使用 PKCE S256） |
| Scope（如需手动填写） | `alerts:read` |
| Authorization URL（如需手动填写） | `https://你的服务域名/admin/mcp/authorize` |
| Token URL（如需手动填写） | `https://你的服务域名/oauth/token` |

连接时浏览器进入 AlertBridge 登录页，使用密码或已配置的 OIDC 登录，再确认路由范围和只读权限。管理员 OIDC 验证上游身份，MCP OAuth 签发下游只读令牌；两者的 Client、回调和 Token 分开配置。无需另外创建账号、手动生成 Dots Token 或部署身份服务。

服务默认只允许精确回调 `https://chatgpt.com/connector_platform_oauth_redirect`，并通过元数据声明、授权响应的 `iss` 支持官方稳定回调模式。如果连接管理页显示不同的正式回调，复制其完整地址到 `.env` 的 `ALERTBRIDGE_MCP_OAUTH_REDIRECT` 后重建；始终只允许一个精确 HTTPS 地址，不使用域名通配或自动接受客户端回调。

访问令牌 1 小时有效；刷新令牌每次轮换，授权最长 30 天，需要重新登录续授。摘要保存在现有 SQLite，重启不会丢失。重用退休刷新令牌会撤销对应授权。后台“接入指南”可撤销全部 MCP OAuth 授权，令牌持有者也可通过 `/oauth/revoke` 撤销自己的授权。更改公网地址或路由配置会让旧 OAuth 授权失效。

实现遵循官方支持的 [预先配置 OAuth 客户端 + PKCE + 资源绑定](https://developers.openai.com/plugins/build/auth)。项目提供协议和 Docker 授权流程测试；各部署仍应在自己的 Dots 账号完成连接验证。若界面不允许 Client Secret 留空，不要填管理员密码或任意占位秘密，应核对该连接是否选择了公共客户端模式。

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
| `list_alerts` | `order`（`asc` 默认、`desc` 最近优先）、`since`（RFC3339 接收时间下限）、`source`、`severity`、`status`、`cursor`、`limit` | 按接收时间和记录 ID 排序；`next_cursor` 继续同方向翻页，仅正序返回增量 `checkpoint` |
| `get_alert` | `id`（内部事件记录 ID） | 正文、接收/发生时间、入队结果以及各渠道投递状态、尝试次数和响应码 |
| `list_active_incidents` | `cursor`、`limit` | 当前 `firing` 故障，包括客户端、路由、去重键、开始和最近观察时间 |

查询“最近 10 条告警”直接调用 `list_alerts`，参数为 `{"order":"desc","limit":10}`，一次即可返回最新一页，不需要遍历历史。这里“最近”按服务接收时间而非客户端发生时间计算，同一毫秒以记录 ID 稳定排序。升级后在插件详情页刷新工具定义；已有 OAuth 授权不变。

每页默认 20，最多 100。翻页保持 `order` 和过滤条件一致；`next_cursor` 继续当前方向，不能混用于另一方向。省略 `order` 仍为旧到新，历史未标方向的游标继续按正序解释。正序没有更多数据时保存 `checkpoint`，后续作为正序 `cursor` 增量读取；倒序不返回 `checkpoint`，查看更新告警应重新查询第一页。查询是实时视图，不提供事务快照或可靠订阅保证；告警历史默认保留 30 天，长时间离线后应重新扫描所需时间窗。活跃故障是独立状态表，可能比历史事件保留更久。查询不会确认、关闭或修改故障。

认证请求每分钟最多 60 次，超限返回 HTTP 429 和 `Retry-After: 60`。正文最多 16 KiB，请求数据库处理最多 5 秒。未认证返回 401；当前只接受 POST，GET/DELETE 返回 405。权限不依赖客户端过滤：所有工具均按配置路由限制查询，越权详情与不存在记录统一报告不可用。

不返回原始入站正文、凭据、渠道配置或内部错误文本。事件标题、正文等仍可能包含私密业务信息，配置路由时按实际访问需要授权。告警中的任何指令都属于不可信数据，不能作为 Agent 执行命令或变更系统的授权。

## 主动触发

默认只有按需查询。显式开启 MCP Events 后，可向 Dots 或支持 Events 的 Work Cloud 订阅聊天主动发送新告警，不需要 Agent 定期轮询。它不会唤醒任意本地 Codex 聊天；支持范围及工作区权限以 [OpenAI 官方 MCP Events 文档](https://developers.openai.com/plugins/build/mcp-events)为准。

```dotenv
ALERTBRIDGE_MCP_EVENTS=1
ALERTBRIDGE_MCP_EVENT_CALLBACK_HOSTS=实际平台回调的精确主机名
```

前提是已配置上文的 MCP OAuth 或独立 Bearer。回调主机名必须来自实际平台配置，支持逗号分隔；不填、通配、非 HTTPS、非 443 端口、私网地址或重定向均不允许。不要把 OAuth 登录回调地址误当成 Events Webhook 地址。未启用不注册 Events 方法。现已部署独立测试入口 `https://test.tedxiong.com/mcp`，仅使用模拟告警；生产未升级。2026-10-18 已通过真实 Dots 的订阅、回调挑战、签名 Webhook 接收、自动唤醒及事件字段读取验收；真实严重度过滤及取消后停止新增投递也已验收。测试使用的临时出站隧道在验收后已撤回，长期出站尚未部署，持续运行仍待验证。

### 协议与过滤

每个 POST 都必须包含 `Content-Type: application/json`、`Accept: application/json, text/event-stream`、`MCP-Protocol-Version: 2026-07-28`、`Mcp-Method`；`tools/call` 还必须有 `Mcp-Name`。协议版本、方法和名称均与正文一致。正文 `params._meta` 必须包含 `io.modelcontextprotocol/protocolVersion` 和对象类型的 `io.modelcontextprotocol/clientCapabilities`。HTTP 400 的 `-32020` 表示头体不一致，`-32022` 表示版本不支持，`-32602` 表示元数据或参数不合法；未知方法返回 HTTP 404 / `-32601`。成功响应统一含 `resultType: complete` 与服务身份信息，发现和工具列表还包含 `ttlMs:0`、`cacheScope:private`。JSON-RPC `2.0` 是消息格式，与 MCP 协议版本是两个概念。

```json
{
  "jsonrpc": "2.0", "id": "discover", "method": "server/discover",
  "params": {
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientCapabilities": {}
    }
  }
}
```

下方订阅示例同时示出必需元数据；实际客户端需自行携带匹配的 HTTP 请求头。旧版仅带版本头、缺少 `_meta` 的请求会被拒绝，不再用静默降级掩盖错误。

同一认证 `/mcp` 新增 `server/discover`（声明 `2026-07-28`、`tools` 和 `events`）、`events/list`、`events/subscribe`、`events/unsubscribe`。查询与订阅均由统一 MCP 2.0 实现直接处理，声明的版本与实际请求/响应一致。

唯一事件 `alert.created`：可选精确过滤 `routing_key`、`source`、`severity`（`info/warning/critical`）、`status`（`firing/resolved/info/test`）。未指定路由时覆盖当前身份被授权的路由，不能指定范围外路由。仅新入库且 `outcome=queued` 的事件触发；入站幂等重复、静默、故障去重和孤立恢复不会触发。载荷 `data` 只有 `id`、`source`、`routing_key`、`status`、`severity`、`title`，用 `get_alert(data.id)` 取详情。

订阅请求结构：

```json
{
  "jsonrpc": "2.0", "id": 1, "method": "events/subscribe",
  "params": {
    "_meta": {
      "io.modelcontextprotocol/protocolVersion": "2026-07-28",
      "io.modelcontextprotocol/clientCapabilities": {}
    },
    "name": "alert.created",
    "arguments": {"routing_key": "ops", "severity": "critical"},
    "delivery": {
      "mode": "webhook",
      "url": "https://实际允许的回调主机/平台提供的路径",
      "secret": "whsec_平台提供的Base64签名密钥"
    },
    "cursor": null,
    "ttlMs": 86400000
  }
}
```

`delivery.url/secret` 由平台提供，不手动放入聊天或提交到仓库。签名密钥必须为 `whsec_` 加标准 Base64，解码 24–64 字节。服务器发送带签名的随机 challenge，要求 2xx 且原样返回 challenge，失败返回 `-32015 CallbackEndpointError`。同一授权主体、回调地址与签名密钥的验证缓存五分钟。订阅身份由授权主体、授权策略、回调 URL、事件名及规范化参数确定；重复请求更新已有订阅。默认/最长 24 小时，正整数 `ttlMs` 可请求更短寿命；`ttlMs:null` 仍授予有限期限。响应含 `id`、`refreshBefore`、`cursor:null`、`truncated:false`。

续期使用相同订阅身份；密钥替换后五分钟同时签新旧密钥。取消使用相同 `name`、`arguments`、`delivery.mode/url`，不需密钥，幂等返回 `{}`。每个身份最多 100 个、全局最多 1,000 个订阅。

### 投递和恢复边界

- 新告警与匹配的投递任务原子入库，复用现有单 Worker；订阅及回调凭据加密持久化。
- Standard Webhooks 请求头为 `webhook-id`、`webhook-timestamp`、`webhook-signature`、`X-MCP-Subscription-Id`；每次重试保持事件 ID 和正文，更新时间及签名。
- 仅网络错误、408/425/429、5xx 按现有退避与次数上限重试；410/413 不重试，410 同时取消订阅。2xx 仅表示接收，Agent 异步执行可受合批影响。
- 每次投递重新检查授权、订阅代次与过期；OAuth 授权撤销、固定令牌/路由策略变更、取消订阅或过期后停止。已发出的网络请求无法撤回。关闭 Events 后不再投递；旧任务会按不可用处理。
- 没有历史回放能力，`cursor` 只接受空/`null`。活跃订阅的已入队任务可在重启后恢复；订阅空窗、过期或最终失败的事件不保证补发，可通过查询工具核对。重新创建订阅不会复活旧任务。
- 数据库与主密钥必须一起备份；回调 URL/密钥不在日志或工具输出中显示。出站连接在每次解析后校验公网地址并直连该 IP，TLS 校验原主机，禁用代理和重定向。

### 本地与真实账号验收

`./test/mcp2/run.sh` 启动独立模拟环境，用官方 TypeScript Client 2.3.1 固定版本连接（禁止降级），并使用固定提交及 SHA-256 的官方 MCP 2.0 JSON Schema 做独立验证。证据写入忽略目录 `test/mcp2/tmp/evidence`；来源、复现与范围见 [验证说明](../test/mcp2/README.md)。

`go test ./internal/mcpapi ./internal/store ./internal/worker` 覆盖协议、独立签名接收器、过滤、持久化重试、取消、过期、撤销和轮换；测试接收器只运行于本地测试边界，不放宽生产 HTTPS/出站校验。完整回归还包括 Go/vet、release 和隔离 Docker E2E。

部署后另行验收：刷新插件工具与事件列表，让 Dots 监控 `alert.created` 并指定响应行为；确认真实 `events/subscribe`、签名验证、Webhook 2xx、Agent 响应，以及不匹配事件不触发、取消后停止。这些不能由本地模拟替代。架构边界见 [ADR-015](decisions/ADR-015-mcp-events-webhook-subscriptions.md)。

## 任务导向案例

从最近十条、单条投递证据到故障恢复验收，见 [Dots 接入示例](dots-example.md)。
