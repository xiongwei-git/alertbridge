# ADR-008: 增加受约束的 Bearer 轻量通知 API

## Status

Accepted — 2026-07-17; amended by ADR-009 on 2026-07-17

## Context

`POST /api/v1/events` 提供完整事件模型、HMAC 签名、防重放、调用方路由授权、幂等和 `firing/resolved` 故障生命周期。这些能力适合可编程客户端，但[宝塔自定义消息通道](https://www.bt.cn/bbs/thread-143326-1-1.html)等来源通常只能配置 URL、固定请求头和带变量的 JSON 正文，无法计算基于原始正文的 HMAC。

要求所有此类来源部署签名适配器，会让一次性通知的接入成本高于实际风险。另一方面，把完整事件接口降级为固定 API Key，会允许泄露的凭据选择路由、伪造级别或关闭活跃故障，不能接受。

## Decision

- 保留 `POST /api/v1/events` 及其 HMAC、Nonce、时间窗、路由授权和幂等语义，不改变已有客户端。
- 新增 `POST /api/v1/notifications`，只接受 `Authorization: Bearer <token>` 和严格 JSON：

  ```json
  {
    "title": "磁盘空间不足",
    "message": "根分区使用率超过 90%",
    "category": "disk",
    "url": "https://status.example.com/host/1"
  }
  ```

- `title`、`message` 必填；`category`、`url` 可选；正文上限 8 KiB，未知字段被拒绝。
- 每个令牌在管理后台固定一个来源名称、`routing_key`、`severity` 和 1–60 次/分钟的独立限额。调用正文不能覆盖这些值。
- 轻量通知的状态固定为 `info`，事件 ID 和时间由服务生成。它不创建、刷新或关闭活跃故障。
- 令牌由 256 位随机秘密和非秘密公开标识组成，只在创建或轮换时显示一次。SQLite 只保存秘密的 SHA-256 摘要，不保存可恢复明文。
- 凭据只允许放在 `Authorization` 请求头，不支持查询参数或 URL 路径中的令牌。
- 认证成功的请求在解析正文前计入令牌自己的持久化分钟限流桶；无效正文同样消耗限额。
- 认证和约束完成后，轻量通知复用现有路由解析、静默、SQLite 持久队列、重试和死信流程。

## Alternatives considered

- **降低完整事件接口的认证要求**：会削弱已有 HMAC、防重放和故障生命周期边界，影响所有调用方。
- **允许令牌在正文中指定路由、级别或状态**：配置更少，但凭据泄露后的权限过大，也会重新引入调用方字段映射。
- **在 URL 中携带令牌**：部分工具配置更方便，但 URL 更容易进入代理访问日志、浏览历史和诊断记录。
- **内置宝塔或其他产品专用解析器**：会再次扩大第三方格式兼容面；轻量接口只提供稳定的通用通知契约。
- **继续要求外置签名适配器**：安全边界最强，但对普通一次性通知不够轻量。

## Consequences

- 只会发送一次性通知的来源可直接接入，完整事件客户端无需迁移。
- Bearer 令牌泄露后，攻击者仍可能在限额内制造通知；管理员应立即停用或轮换令牌，并可在宝塔 Nginx 叠加来源限速。
- 固定路由、级别和 `info` 状态把泄露影响限制在一个预先授权的通知通道内，不能伪造故障恢复。
- API、管理后台、OpenAPI 和 Docker E2E 增加一条需要长期维护的认证路径，但不增加新进程、数据库连接、Worker 或外部依赖。

## References

- [RFC 6750: OAuth 2.0 Bearer Token Usage](https://www.rfc-editor.org/rfc/rfc6750.html)：Bearer 凭据通过 `Authorization` 请求头和 TLS 传输，避免进入 URL。
- [OWASP API4:2023 Unrestricted Resource Consumption](https://owasp.org/API-Security/editions/2023/en/0xa4-unrestricted-resource-consumption/)：对请求大小和调用频率设置服务端边界。
