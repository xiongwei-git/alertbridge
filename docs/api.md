# AlertBridge v1 API 契约

`GET /` 与 `HEAD /` 公开提供服务内嵌的项目介绍 HTML，不创建登录会话、不读取业务数据。其他根路径方法返回 `405`（`Allow: GET, HEAD`），未知路径仍为 `404`。这不改变下面通知 API、MCP 或管理员认证的契约。

本文描述 v1 通知 API；OpenAPI 的 `info.version` 是接口文档版本，独立于应用发布版本 v0.5.0。管理员 OIDC 不改变以下 API 的认证。

除以下通知入站 API 外，可选 `/mcp` 提供独立授权的只读 Agent 查询。它使用 MCP JSON-RPC，而非 REST 入站契约；详见 [MCP 接入指南](mcp.md)。现有两个通知接口的认证和响应不变。

MCP `list_alerts` 支持 `order=asc|desc`，默认保持正序；最近 10 条使用 `{"order":"desc","limit":10}`。按服务接收时间、记录 ID 排序，分页游标绑定方向，旧游标仍兼容正序。仅正序返回增量 `checkpoint`，倒序更新需重新查询第一页。

Dots OAuth 支持预配置公共客户端 `alertbridge-dots`、授权码 + PKCE S256、`alerts:read` 和资源绑定；不支持客户端秘密、DCR 或 CIMD。发现路径为 `/.well-known/oauth-protected-resource/mcp` 与 `/.well-known/oauth-authorization-server`，授权确认页为 `/admin/mcp/authorize`，令牌/轮换端点为 `/oauth/token`，撤销为 `/oauth/revoke`。访问令牌有效 1 小时，授权最长 30 天；刷新令牌每次轮换。所有端点只在配置 OAuth 后注册。

AlertBridge 提供两种兼容并存的入站方式：

| 接口 | 认证 | 适用场景 |
| --- | --- | --- |
| `POST /api/v1/notifications` | Bearer 轻量令牌 | 宝塔自定义消息通道、简单脚本和一次性通知 |
| `POST /api/v1/events` | HMAC + 时间戳 + Nonce | 幂等重试、可选路由、`firing/resolved` 故障生命周期 |

两种接口都复用相同的路由、静默、持久队列、重试和死信机制。轻量接口是对完整事件接口的补充，不改变已有 HMAC 调用。

## `POST /api/v1/notifications`

轻量接口只需要固定的 Bearer 令牌和标题、正文。令牌在管理后台创建，服务端为它固定来源、路由、级别和分钟限额；调用方不能在正文中覆盖这些权限。

```http
Authorization: Bearer abt_...
Content-Type: application/json
```

```json
{
  "title": "宝塔磁盘告警",
  "message": "根分区使用率超过 90%",
  "category": "disk",
  "url": "https://status.example.com/host/1"
}
```

字段约束：

- `title`：必填，1–200 个字符，不能包含换行；
- `message`：必填，1–4000 个字符；
- `category`：可选，最多 64 个字符，不能包含换行；保存为事件的 `category` 标签；
- `url`：可选，只接受不带用户信息的绝对 HTTP/HTTPS URL；
- 正文最大 8 KiB，只接受一个 JSON 对象，未知字段会被拒绝。

服务端生成事件 ID 和 UTC 时间，来源使用令牌名称，状态固定为 `info`，路由和级别使用令牌配置。因此轻量接口不会创建、刷新或关闭“活跃故障”。需要 `firing/resolved`、调用方幂等 ID、防重放或按请求选择路由时，应使用完整事件接口。

成功持久化入队后返回 `200 OK`，以兼容只把 HTTP 200 视为成功的宝塔自定义消息通道。响应结构与完整事件接口相同，例如：

```json
{
  "request_id": "9ac0e2cb4e29da5d92e671d4",
  "event_record_id": "cc9ef9830ac37f7c218cb01c96391dda",
  "event_id": "simple-e9049fb4aa34fd33a0f1ded32d5d11ec",
  "outcome": "queued",
  "deliveries": 1
}
```

这里的 `200 OK` 表示 AlertBridge 已完成认证、校验和持久化入队，不表示外部通知渠道已经收到消息。`outcome=queued` 时应在后台“投递记录”查看最终结果。完整事件接口仍返回 `202 Accepted`。

令牌是高熵凭据，只在创建或轮换时显示一次，数据库只保存摘要。必须通过 HTTPS 放在 `Authorization` 请求头中；不要把令牌写进 URL、Git、日志或前端代码。每个令牌使用独立的 1–60 次/分钟限流桶，认证成功但正文无效的请求也会计数。

## `POST /api/v1/events`

成功接收返回 `202 Accepted`。这里的“接收”表示认证、校验、事件记录和投递任务已持久化，不表示外部通知渠道已经收到消息。

必须提供四个 HMAC 请求头：`X-Notify-Client`、`X-Notify-Timestamp`、`X-Notify-Nonce`、`X-Notify-Signature`。签名算法见下节。请求体最大 32 KiB，只接受 `application/json`，未知字段会被拒绝。

这是 AlertBridge 唯一的完整事件生命周期契约。调用方负责把自身事件映射成下面的统一事件模型，再对实际发送的原始 JSON 字节签名。AlertBridge 不根据第三方产品名称猜测或转换其私有格式。

### HMAC 签名

```text
body_hash = hex(SHA256(raw_request_body))
message   = timestamp + "\n" + nonce + "\n" + body_hash
signature = hex(HMAC-SHA256(client_secret, message))
```

`timestamp` 是请求头中的 Unix 秒字符串，`nonce` 每次请求随机生成；摘要和签名为十六进制。签名后不要重新序列化 JSON。重试保留事件 ID，但使用新的时间戳、Nonce 和对应签名。可执行实现见 [Python 示例](../examples/python/README.md)。

### 请求

```json
{
  "event_id": "proxy-hk-01-20260715-001",
  "source": "monitoring-hk",
  "routing_key": "infrastructure",
  "status": "firing",
  "severity": "critical",
  "title": "香港代理服务不可用",
  "message": "连续三次检测失败，TCP 端口无法连接。",
  "occurred_at": "2026-07-15T12:30:00+08:00",
  "dedupe_key": "proxy-hk-01-connectivity",
  "labels": {
    "service": "proxy",
    "region": "hong-kong",
    "node": "hk-vps-01"
  },
  "url": "https://status.example.com/service/hk-vps-01"
}
```

约束：

- `event_id`：同一客户端内唯一，1–128 字节；相同值用于幂等重试。
- `source`、`routing_key`：1–64 字节标识符。
- `status`：决定事件是否进入故障生命周期，语义见下表。
- `severity`：`critical`、`warning` 或 `info`。
- `title`：1–200 个字符；`message`：1–4000 个字符。
- `occurred_at`：RFC 3339；不能超过服务当前时间 5 分钟。
- `dedupe_key`：`firing` 和 `resolved` 必填，最多 128 字节。
- `labels`：最多 20 项，键最多 64 字节，值最多 256 个字符。
- `url`：可选，只接受不带用户信息的绝对 HTTP/HTTPS URL。

状态语义：

| 值 | 含义 |
| --- | --- |
| `firing` | 创建或刷新由 `client_id + routing_key + dedupe_key` 标识的活跃故障 |
| `resolved` | 关闭同一活跃故障；通知会根据已记录的开始时间显示持续时间 |
| `info` | 普通一次性通知，不创建活跃故障 |
| `test` | 接入或渠道测试，不创建活跃故障 |

`firing` 与 `resolved` 必须使用相同的客户端、`routing_key` 和 `dedupe_key` 才能完成一次故障闭环。测试请求应使用 `test`，不要用 `firing` 代替测试状态。

### 响应

```json
{
  "request_id": "9ac0e2cb4e29da5d92e671d4",
  "event_record_id": "cc9ef9830ac37f7c218cb01c96391dda",
  "event_id": "proxy-hk-01-20260715-001",
  "outcome": "queued",
  "deliveries": 1
}
```

`outcome` 的稳定值：

| 值 | 含义 |
| --- | --- |
| `queued` | 创建了一个或多个持久化投递任务 |
| `suppressed` | 事件已记录，但被去重、孤立恢复或静默窗口抑制 |
| `duplicate` | 同一客户端的 `event_id` 已存在，没有重复投递 |

`suppressed` 还会返回 `reason=dedupe_window`、`reason=orphan_resolved` 或 `reason=silence`。

## 接入原则与迁移

- 只需要发送一次性标题和正文的来源，可调用 `POST /api/v1/notifications`。
- 需要故障生命周期、幂等和防重放的外部监控平台、脚本或中间服务，调用 `POST /api/v1/events`。
- 如果来源不能生成完整事件和四个签名请求头，也不能安全保存 Bearer 请求头，应由来源侧插件或独立的可信适配器完成接入。
- 已移除的 `/hooks/*` 路径不再接收或转换消息，迁移期固定返回 `410 Gone` 和 `endpoint_removed`，并在错误消息中指向正式接口。

## 错误

所有错误使用统一结构：

```json
{
  "error": {
    "code": "invalid_event",
    "message": "title must contain 1 to 200 characters",
    "request_id": "9ac0e2cb4e29da5d92e671d4"
  }
}
```

| HTTP | `code` | 含义 |
| --- | --- | --- |
| 400 | `invalid_json` | JSON 无效、含未知字段或包含多个值 |
| 401 | `authentication_failed` | Bearer 令牌无效，或 HMAC 客户端、签名、时间窗、Nonce 重放无效 |
| 403 | `route_forbidden` | 客户端无权使用该逻辑路由 |
| 413 | `body_too_large` | 正文超过上限 |
| 415 | `unsupported_media_type` | 不是 JSON |
| 422 | `invalid_event` / `invalid_notification` | 字段语义不满足对应契约 |
| 422 | `route_unavailable` | 路由在该级别没有启用渠道 |
| 429 | `rate_limited` | 客户端或令牌分钟限流；响应含 `Retry-After: 60` |
| 503 | `storage_unavailable` / `service_unavailable` | 事件未可靠持久化或服务暂时无法生成安全事件 ID；HMAC 客户端重试时应使用新 Nonce |

## 健康检查

- `GET /healthz`：进程存活，不访问数据库。
- `GET /readyz`：SQLite 可访问时返回 `200`，否则返回 `503`。

健康检查不需要认证，也不会返回内部配置或版本信息。


## 可选 MCP Events

独立启用 `ALERTBRIDGE_MCP_EVENTS=1` 与精确回调主机白名单后，同一认证 `POST /mcp` 提供 `server/discover`（`2026-07-28`）、`events/list`、`events/subscribe`、`events/unsubscribe`。同一 MCP 2.0 实现同时支持查询与订阅，不再使用旧版 SDK 或版本头改写，不新增入站告警接口。每请求必须携带规范 `_meta`、版本和方法镜像头；工具调用还需名称镜像头。成功响应含 `resultType:complete`，发现/工具列表含缓存字段；无 initialize 握手。

事件 `alert.created` 可按授权路由、来源、严重程度和状态过滤，只推送新 accepted/queued 告警的摘要与内部 ID。订阅使用平台提供的 HTTPS Webhook 和 Standard Webhooks 签名密钥，先验证 challenge，再持久化订阅；默认/最长 24 小时，支持续期、取消、五分钟双密钥轮换，无历史回放（`cursor:null`）。权限撤销或过期停止投递；410/413 不重试。回调 2xx 只表示接收。公开入站响应中的 `deliveries` 仍只计算配置的通知渠道，MCP 回调任务不会改变该数量；只读投递详情可见内部 `@mcp-event:` 任务及状态，但不含回调凭据。

详细请求、响应、安全边界与真实账号验收见 [MCP Events](mcp.md#主动触发)。本功能已完成本地独立验证，并部署到隔离测试站 test.tedxiong.com；生产未升级，真实 Dots 订阅和唤醒未验收。
