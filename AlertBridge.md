# AlertBridge 产品设计与阶段边界

> 文档状态：截至 2026-07-17，第一、第二阶段已经实现，轻量接入扩展已完成；第三阶段仍是候选规划。本文保留产品设计背景，但当前可用能力以 [README](README.md)、[API 契约](docs/api.md)、[管理后台指南](docs/admin.md)和[部署指南](docs/deployment.md)为准。

## 当前实现快照

- 已实现：受约束的 Bearer 轻量通知 API、统一事件 API、客户端 HMAC、路由授权、`firing/resolved` 故障生命周期、去重、静默窗口、持久队列、重试与死信、管理后台、飞书/Telegram/ntfy/SMTP，以及 Docker + 宝塔部署；
- 已实现：GHCR 双架构正式镜像，以及面向中国内地阿里云 ECS 的可选 ACR VPC 部署镜像；
- 尚未实现：多事件聚合、超时升级、告警确认、Prometheus 指标、多实例存储，以及钉钉/企业微信/Slack/Teams/Discord/Gotify 适配器；
- 设计边界：`POST /api/v1/notifications` 只接收标题和正文的一次性通知，`POST /api/v1/events` 接收完整事件；两者都不解析第三方监控产品的私有格式。

## 一、需求定位

这不是一个简单的“飞书 Webhook 转发器”，更适合定位为：

> **统一通知网关（Notification Gateway）**

所有监控服务只需要知道：

- 通知网关地址，例如 `https://notify.tedxiong.com`
- 自己的轻量令牌或 HMAC 客户端凭据
- 简单通知内容，或完整事件、严重程度和逻辑路由

飞书、Telegram、邮件等渠道的 Webhook、Token、签名密钥和 IP 白名单，全部集中在通知网关管理。

飞书侧只需要信任通知网关的固定出口 IP。飞书自定义机器人支持关键词、IP 白名单和签名校验；AlertBridge 同时限制自身出站正文大小，并通过单 Worker、持久队列和退避重试控制发送压力。具体平台限制应以[飞书开放平台](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot)当前文档为准。

------

## 二、整体架构

```text
┌──────────────────────────────────────────────┐
│                  监控服务                     │
│ 监控平台 / 自定义脚本 / 外部服务              │
│ VPS巡检 / 代理检测 / SSL检测 / Docker监控      │
└──────────────────────┬───────────────────────┘
                       │ HTTPS + Bearer / HMAC
                       ▼
┌──────────────────────────────────────────────┐
│           Notification Gateway              │
│                                              │
│  ① 接入认证      ② 消息标准化                 │
│  ③ 路由规则      ④ 去重、恢复、静默           │
│  ⑤ 队列与重试    ⑥ 告警状态管理               │
│  ⑦ 渠道消息渲染  ⑧ 发送记录与审计             │
└──────────────┬──────────┬──────────┬─────────┘
               │          │          │
        ┌──────▼───┐ ┌────▼────┐ ┌──▼─────────┐
        │ 飞书适配器 │ │Telegram │ │ 邮件/ntfy  │
        └──────────┘ └─────────┘ └────────────┘
```

钉钉、企业微信、Slack、Teams、Discord 和 Gotify 仅是候选出站渠道，不属于当前架构的已实现部分。

### 核心原则

**接收和发送必须解耦。**

监控服务调用网关后，网关完成认证和数据校验，立即返回：

```http
HTTP/1.1 202 Accepted
```

然后由后台任务发送飞书消息。这样即使飞书暂时超时，监控服务也不会被阻塞。

------

## 三、轻量通知与统一事件模型

对于宝塔自定义消息通道等只能发送标题和正文的来源，轻量接口允许：

```json
{
  "title": "服务器告警",
  "message": "磁盘使用率超过 90%",
  "category": "disk"
}
```

路由、严重程度、来源和分钟限额固定在令牌上，状态固定为 `info`。它不会创建或关闭活跃故障。

需要故障生命周期、幂等、防重放或按请求选择路由时，完整事件 API 使用结构化模型：

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

当前支持以下状态：

| 状态       | 含义                   |
| ---------- | ---------------------- |
| `firing`   | 故障首次发生或持续存在 |
| `resolved` | 故障已经恢复           |
| `info`     | 普通通知               |
| `test`     | 测试消息               |

严重程度：

```text
critical > warning > info
```

结构化模型目前支持：

- 严重故障同时通知飞书和 Telegram
- 普通告警只发飞书
- 恢复通知自动关联原故障
- 重复故障不反复刷屏
- 按地区、服务、监控类型路由

------

## 四、认证设计

### 1. 每个调用方使用独立凭证

当前为每个调用方分配独立凭证：

| Client ID        | 使用方              | 允许路由         |
| ---------------- | ------------------- | ---------------- |
| `monitor-us`     | 美国 VPS 上的监控服务 | `infrastructure` |
| `proxy-check-hk` | 香港代理检测脚本    | `proxy`          |
| `docker-home`    | 家庭服务器          | `homelab`        |

某台 VPS 密钥泄露后，只需要撤销这一台，不影响其他服务。

### 2. HMAC 签名

请求头：

```http
X-Notify-Client: monitoring-client
X-Notify-Timestamp: 1784090000
X-Notify-Nonce: 7f12c93a
X-Notify-Signature: 3b825...
Content-Type: application/json
```

签名内容：

```text
HMAC-SHA256(
    client_secret,
    timestamp + "\n" +
    nonce + "\n" +
    SHA256(request_body)
)
```

网关验证：

- 时间戳不能超过 5 分钟
- Nonce 不能重复使用
- Client 是否启用
- Client 是否有权调用该 `routing_key`
- 签名是否正确
- 请求频率是否超限

比单纯 API Key 更适合暴露在公网，可以防止请求被截获后直接重放。

### 3. 受约束的 Bearer 轻量令牌

简单 Webhook 使用 `Authorization: Bearer ...`。令牌在后台固定来源、路由、严重程度和 1–60 次/分钟限额，调用方只能提交标题、正文、可选分类和链接。

令牌使用 256 位随机秘密，只显示一次，数据库只保存摘要。它不能提交 `firing/resolved`、不能改变路由或严重程度，也不与 HMAC 客户端共享限流桶。这是在不削弱完整事件认证的前提下，为固定 Webhook 提供的最小能力。

### 4. 可选的进一步保护

对于全部由自己管理的 VPS，可以再增加：

- Tailscale 或 WireGuard 私网访问
- 客户端 IP 白名单
- mTLS 客户端证书
- Cloudflare Access 或反向代理限流

完整事件仍应保留 HMAC；轻量入口也不能完全依赖 IP 或代理限流。

------

## 五、路由规则

不要让调用方直接指定具体 Webhook，例如：

```json
{
  "feishu_webhook": "https://open.feishu.cn/..."
}
```

调用方只传逻辑路由：

```json
{
  "routing_key": "proxy"
}
```

网关内部配置：

```yaml
routes:
  proxy:
    info:
      - feishu.proxy_group

    warning:
      - feishu.proxy_group

    critical:
      - feishu.proxy_group
      - telegram.personal
      - email.backup

  infrastructure:
    warning:
      - feishu.ops_group

    critical:
      - feishu.ops_group
      - ntfy.phone
```

这样以后更换飞书群、Webhook 或 IM 工具，不需要修改任何监控服务。

------

## 六、告警治理能力

| 能力 | 当前状态 |
| --- | --- |
| 去重、恢复通知 | 已实现 |
| 一次性静默窗口 | 已实现 |
| 重试、死信、人工重新入队 | 已实现 |
| 多事件聚合 | 规划中 |
| 超时升级 | 规划中 |
| 告警确认 | 规划中 |

### 1. 去重（已实现）

相同 `dedupe_key` 在一定时间内只发送一次：

```text
proxy-hk-01-connectivity
```

持续故障在去重窗口结束后可以再次通知，而不是每分钟刷屏。当前不会把多个事件自动合并为摘要。

### 2. 聚合（规划中）

当多台 VPS 同时异常时，合并成一条：

```text
【严重告警】3 个节点无法访问

- hk-vps-01
- us-vps-02
- sg-vps-01
```

### 3. 恢复通知（已实现）

收到：

```json
{
  "dedupe_key": "proxy-hk-01-connectivity",
  "status": "resolved"
}
```

网关可以计算故障持续时间：

```text
✅ 香港代理服务已恢复
故障持续：12 分 36 秒
```

### 4. 静默和维护窗口（已实现一次性窗口）

当前后台支持限定路由、严重程度、开始时间和结束时间的一次性静默窗口，最长 30 天。例如：

```text
2026-07-19 02:00–04:00
```

这一时间段内：

- 暂不发送普通异常
- 严重异常仍可发送
- 维护结束后恢复正常投递

周期性窗口和维护结束汇总尚未实现。

### 5. 重试与死信（已实现）

建议发送失败后：

```text
1 秒 → 5 秒 → 30 秒 → 2 分钟 → 10 分钟
```

仍然失败后进入 Dead Letter Queue，可在后台人工重新入队。同一路由配置多个渠道时，各渠道拥有独立投递任务；当前不会在失败后动态选择一个未配置的备用渠道。

### 6. 升级通知（规划中）

例如：

```text
故障发生
  ↓
立即发送飞书
  ↓
5 分钟仍未恢复
  ↓
发送 Telegram / ntfy
  ↓
30 分钟仍未恢复
  ↓
发送邮件或更高等级提醒
```

### 7. 确认告警（规划中）

这是后期可以扩展的功能：

```text
确认告警
静默 30 分钟
静默 2 小时
查看详情
```

需要注意，飞书自定义机器人发送的卡片按钮只能跳转 URL，不能直接把按钮操作回调给你的服务。因此有两种实现方式：

1. 按钮跳转到通知网关的告警详情页，在网页中确认；
2. 升级为飞书应用机器人，通过回调处理交互。

如进入第三阶段，应重新依据[飞书开放平台](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot)当时的交互能力选择自定义机器人或应用机器人方案。

------

## 七、通知渠道

| 渠道     | 当前状态 | 适合场景               |
| -------- | -------- | ---------------------- |
| 飞书     | 已实现   | 日常主通知、运维群     |
| Telegram | 已实现   | 个人紧急通知、境外环境 |
| ntfy     | 已实现   | 自建手机推送           |
| 邮件     | 已实现   | 长期留档、最终兜底     |
| 钉钉     | 候选     | 国内企业群             |
| 企业微信 | 候选     | 国内企业群             |
| Slack    | 候选     | 海外团队               |
| Teams    | 候选     | Microsoft 365 团队     |
| Discord  | 候选     | 个人或技术社区         |
| Gotify   | 候选     | 自建手机推送           |

候选渠道不是当前版本承诺。只有在出现明确使用需求后，才新增独立的出站适配器，并继续复用统一事件、路由和投递队列。

Slack Incoming Webhook 通过唯一 URL 接收 JSON，并支持 Slack 的消息布局组件。([Slack 开发者文档](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/))

钉钉自定义机器人同样采用 Webhook，并支持加签安全方式，适合作为一个标准发送适配器。([钉钉开放平台](https://open.dingtalk.com/document/robots/custom-robot-access))

Telegram Bot API 是 HTTP 接口，可以向私人会话或群组发送消息，比较适合作为飞书之外的独立备用路径。([Telegram](https://core.telegram.org/bots/api))

Teams 建议优先接入新的 Workflows Webhook。Teams Workflow 与具体所有者关联，生产环境应设置共同所有者，避免账号离职或停用后工作流失效。([Microsoft Learn](https://learn.microsoft.com/en-us/microsoftteams/platform/webhooks-and-connectors/how-to/add-incoming-webhook))

ntfy 支持通过简单的 HTTP POST/PUT 推送消息，并支持自托管，适合作为手机端紧急推送通道。([docs.ntfy.sh](https://docs.ntfy.sh/publish/))

------

## 八、接入层只提供两种稳定契约

```http
POST /api/v1/notifications
POST /api/v1/events
```

轻量接口只创建由令牌约束的 `info` 一次性通知；完整事件接口承载 HMAC、防重放、幂等和 `firing/resolved` 生命周期。已有 HMAC 客户端不需要迁移。

AlertBridge 不维护以第三方产品命名的输入解析器，也不通过猜测字段完成转换。固定 Webhook 可直接适配轻量契约；需要完整生命周期的平台负责映射统一事件和签名。这样避免上游格式变化导致静默误判，并让权限边界和版本演进保持一致。

------

## 九、管理后台

当前后台包括以下能力：

### 渠道管理

- 飞书 Webhook 和签名密钥
- Telegram Bot Token 和 Chat ID
- SMTP 配置
- ntfy 地址和访问凭据
- 测试发送
- 启用、禁用

### 客户端管理

- 创建 Client ID
- 生成和轮换 Secret
- 限定可使用的路由
- 限定调用频率
- 撤销凭证
- 创建、启停和轮换轻量令牌
- 为轻量令牌固定路由、级别和独立限额

### 路由管理

- 严重程度与渠道映射
- 静默时间
- 客户端可用路由授权

消息模板和故障升级规则属于第三阶段规划，不在当前后台中。

### 活跃故障

- 仪表盘显示尚未收到匹配 `resolved` 的 `firing` 事件；
- 事件按照客户端、路由键和 `dedupe_key` 关联；
- 测试调用应使用 `status=test`，不会增加活跃故障数。

### 发送记录

```text
时间
来源
事件标题
目标渠道
发送状态
响应码
重试次数
失败原因
```

后台不会回显已保存的敏感凭据；编辑渠道时敏感字段留空表示保留原值。渠道列表只显示飞书安全关键词是否已配置，编辑时将关键词留空会明确清除它。

------

## 十、技术方案

结合个人 VPS、Docker、轻量和安全要求，当前采用：

```text
Go 单进程
  + SQLite
  + 原生通知渠道适配器
  + 服务端渲染管理后台
  + 持久化投递 Worker
  + 宝塔 Nginx HTTPS
```

### 为什么这样选择

- Go 单二进制：镜像小、启动快，不需要 Python 或 Node.js 运行时；
- SQLite：个人和小团队单实例规模足够，减少外部依赖和故障面；
- 原生适配器：明确控制飞书签名、安全关键词、Telegram、ntfy 和 SMTP 的安全边界；
- 服务端渲染：后台不依赖外部脚本、字体或 CDN，弱网和离线环境更稳定；
- Docker Compose：迁移、资源限制、版本升级和卷备份边界清晰；
- 宝塔 Nginx：复用现有域名、证书和 HTTPS 管理，不重复增加 Caddy TLS 层。

------

## 十一、部署可靠性

这是整个方案最容易忽略的地方。

### 不要部署在被监控的 VPS 上

假设通知网关和代理服务部署在同一台香港 VPS：

```text
香港 VPS 故障
  ├─ 代理服务掉线
  └─ 通知网关也掉线
```

结果就是最需要告警的时候无法告警。

建议：

- 通知网关部署在独立 VPS
- 最好与主要被监控机器不同服务商、不同地区
- 配置固定 IPv4，供飞书配置 IP 白名单
- 数据库和配置每天异地备份
- 网关自身由外部监控检查
- 网关失效通知必须绕过网关，例如外部监控直接发邮件或 ntfy

这叫做避免“告警系统监控自己，但自己故障后无法告警”的闭环问题。

------

## 十二、实施阶段

### 第一阶段：可用版本（已完成）

实现：

- 统一 `/api/v1/events`
- 客户端 HMAC 认证
- 飞书发送
- 固定格式的文本和卡片消息
- SQLite 发送记录
- 异步队列和失败重试
- Docker Compose 部署
- `/healthz` 和 `/readyz` 健康检查

### 第二阶段：真正的通知网关（已完成）

增加：

- Telegram、邮件、ntfy
- 客户端独立密钥
- 路由规则
- 去重和恢复通知
- 稳定、严格、可版本化的统一事件 API
- 管理后台
- 静默和维护窗口

第二阶段轻量接入扩展：

- `POST /api/v1/notifications`
- 高熵、只保存摘要的 Bearer 令牌
- 服务端固定路由、级别和 `info` 状态
- 独立持久化分钟限流
- 宝塔自定义消息通道直接接入

### 第三阶段：告警中心（规划中）

增加：

- 告警确认
- 超时升级
- 告警详情页
- 统计看板
- Prometheus Metrics
- 多实例部署
- Redis/PostgreSQL
- 飞书应用机器人交互

------

## 最终建议方案

当前阶段的 **AlertBridge** 使用：

```text
域名：notify.tedxiong.com

入口：
POST /api/v1/notifications
POST /api/v1/events

认证：
Bearer 轻量令牌
或 Client ID + HMAC Secret

主渠道：
飞书

紧急备用：
Telegram 或 ntfy

最终兜底：
邮件

部署：
独立 VPS + Docker Compose + 宝塔 Nginx + SQLite
```

最重要的不是支持多少 IM，而是先把以下五个基础能力做好：

```text
统一入口
独立认证
逻辑路由
可靠重试
故障去重
```

一旦这层稳定，后续增加任何 IM 工具都只是新增一个适配器。
