# AlertBridge v1 API 契约

## `POST /api/v1/events`

成功接收返回 `202 Accepted`。这里的“接收”表示认证、校验、事件记录和投递任务已持久化，不表示外部通知渠道已经收到消息。

必须提供四个 HMAC 请求头：`X-Notify-Client`、`X-Notify-Timestamp`、`X-Notify-Nonce`、`X-Notify-Signature`。签名算法见项目 README。请求体最大 32 KiB，只接受 `application/json`，未知字段会被拒绝。

这是 AlertBridge 唯一的正式入站契约。调用方负责把自身事件映射成下面的统一事件模型，再对实际发送的原始 JSON 字节签名。AlertBridge 不根据第三方产品名称猜测或转换其私有格式。

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

- 外部监控平台、脚本或中间服务统一调用 `POST /api/v1/events`。
- 如果来源不能生成统一正文和四个签名请求头，应由来源侧插件或独立的可信适配器完成映射与签名。
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
| 401 | `authentication_failed` | 客户端、签名、时间窗或 Nonce 重放无效 |
| 403 | `route_forbidden` | 客户端无权使用该逻辑路由 |
| 413 | `body_too_large` | 正文超过上限 |
| 415 | `unsupported_media_type` | 不是 JSON |
| 422 | `invalid_event` | 字段语义不满足契约 |
| 422 | `route_unavailable` | 路由在该级别没有启用渠道 |
| 429 | `rate_limited` | 客户端分钟限流；响应含 `Retry-After: 60` |
| 503 | `storage_unavailable` | 事件未可靠持久化，客户端应使用新 Nonce 重试 |

## 健康检查

- `GET /healthz`：进程存活，不访问数据库。
- `GET /readyz`：SQLite 可访问时返回 `200`，否则返回 `503`。

健康检查不需要认证，也不会返回内部配置或版本信息。
