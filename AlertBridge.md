# AlertBridge 架构与实现范围

适用版本：v0.5.0。安装见 [部署指南](docs/deployment.md)，接口字段见 [API 契约](docs/api.md)。本文说明已实现的数据流和限制，不作为后续功能承诺。

## 运行结构

```text
事件调用方 ── HMAC / Bearer ──► HTTP API
                                  │
                                  ▼
                           认证、校验、路由
                                  │
                                  ▼
                              SQLite
                         事件 / 故障 / 投递任务
                                  │
                                  ▼
                            单投递 Worker
                                  │
                                  ▼
                    飞书 / Telegram / ntfy / TLS SMTP

管理员 ── 密码或可选 OIDC ──► 服务端渲染后台 ──► 加密动态配置
Agent  ── MCP OAuth/Bearer ──► 只读查询 ─────────► 授权路由的数据
```

一个 Go HTTP 进程、一个 SQLite 连接、一个持久投递 Worker。SQLite 使用 WAL 和事务保存事件与投递任务；Worker 从队列领取任务，记录尝试结果，失败后退避重试，达到限制后转为死信。具体默认值与配置约束以代码和管理后台为准。

后台通过 `html/template` 渲染表单，不需要客户端 JavaScript、Node.js 或 CDN。动态修改写入 SQLite 后构建不可变运行快照并原子切换；无需为渠道、路由变更重启应用。

## 两种事件入口

| 契约 | 来源能力 | 约束 |
| --- | --- | --- |
| `/api/v1/events` | 能生成统一 JSON 和 HMAC 签名的脚本、监控系统 | 每客户端独立密钥、路由授权、时间窗、Nonce、幂等事件 ID |
| `/api/v1/notifications` | 只能填写固定 URL、请求头与正文的系统 | Bearer 请求头；来源、路由、严重程度和限额由服务端固定，状态为 `info` |

网关不根据第三方产品名称或模糊字段猜测事件格式。需要格式转换的系统应在来源侧转换。轻量令牌不能设置生命周期字段，也不能替代 HMAC 客户端。

## 故障与投递状态

这两个状态维度分别维护：

- 事件 `firing` 创建或刷新活跃故障；`resolved` 关闭相同客户端、路由和 `dedupe_key` 对应的故障。去重窗口内重复故障仍会记录，但不重复投递；孤立恢复被抑制。
- `info` 和 `test` 不创建活跃故障。
- `queued` 表示已创建投递任务，`suppressed` 表示被静默、去重或孤立恢复规则抑制，`duplicate` 表示事件 ID 已存在。
- 渠道投递的成功、重试和死信不等同于故障发生或恢复。API 返回 HTTP 200/202 不代表接收人已收到或阅读消息。

路由按 `routing_key + severity` 选择一个或多个渠道。静默窗口按路由、严重程度和绝对时间范围设置，最长 30 天；没有周期性窗口、维护结束汇总或通知升级。

## 凭据与管理登录

客户端密钥和渠道凭据用独立主密钥进行 AES-256-GCM 加密。管理员密码保存 Argon2id 哈希，随机会话和轻量令牌保存摘要。数据库和主密钥分卷保存，恢复必须配套。

OIDC 是可选管理员登录方式，当前按 Tinyauth 验证。AlertBridge 使用 Authorization Code + PKCE S256，验证 RS256 ID Token、issuer、audience、nonce 和明确配置的 subject。每个 Client 的 subject 必须单独核实；用户名或邮箱不授予权限。

登录事务在进程内保存，最多 256 项、有效期五分钟；重启使未完成事务失效。身份提供方 Token 验证后丢弃，不写入 SQLite 或 Cookie。成功登录建立本地随机会话，应用退出不执行身份提供方全局退出。可通过配置保留备用密码登录。

这不要求对整个域名增加 `auth_request`；通知 API、健康检查和 MCP 仍使用各自的认证规则。OIDC 的外部配置文件也不是业务配置种子，客户端、渠道和路由仍从空后台配置。

## MCP 查询

`/mcp` 使用无状态 Streamable HTTP 和 JSON 响应，默认关闭。提供 `list_alerts`、`get_alert`、`list_active_incidents`，服务端限制可读路由。

Dots 使用 AlertBridge 签发的 OAuth 令牌；支持自定义请求头的客户端可使用独立固定 Bearer。管理员 OIDC 是上游登录，MCP OAuth 是下游只读授权，两者不交换身份提供方 Token。

历史告警按接收时间与记录 ID 分页；倒序用于最近记录，正序的 checkpoint 用于增量读取。查询不提供事务快照或可靠订阅，不实现主动唤醒、确认、恢复或执行命令。正文来自外部调用方，必须作为不可信数据处理。

## 部署与恢复

生产容器基于 `scratch`，以 UID 10001 运行，根文件系统只读，去除 capabilities，应用端口仅绑定 loopback。Nginx 管理公网 HTTPS。

基础密码登录需要一个 Compose 文件、`.env` 和文件型引导密码。OIDC 额外需要 overlay 和受限 JSON Secret，身份提供方是可选外部依赖；启用时其发现接口不可用会阻止应用启动。

数据库与主密钥卷成对备份；Compose、环境文件、引导密码以及 OIDC 配置文件分别保留。服务无法在自身主机故障时发送通知，因此自身存活检查和兜底发送应由独立外部系统承担。

官网和浏览器 Demo 是单独的静态文件，部署在 `opensource.tedxiong.com`。不复制生产数据库、不调用通知 API、不提供公开管理后台。

## 未实现能力

多实例共享存储、高可用、告警聚合、值班排班、超时升级、告警确认、周期性静默、自动修复、MCP Events 和其他通知渠道均未实现。新需求应先明确负载、协议和权限约束，再通过 ADR 决定是否改变当前结构。历史决策见 [docs/decisions](docs/decisions/)。
