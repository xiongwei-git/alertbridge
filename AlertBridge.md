## 一、需求定位

这不是一个简单的“飞书 Webhook 转发器”，更适合定位为：

> **统一通知网关（Notification Gateway）**

所有监控服务只需要知道：

- 通知网关地址，例如 `https://notify.tedxiong.com`
- 自己的客户端 ID
- 自己的调用密钥
- 发送什么事件、严重程度和路由目标

飞书、Telegram、邮件等渠道的 Webhook、Token、签名密钥和 IP 白名单，全部集中在通知网关管理。

飞书侧只需要允许通知网关的固定出口 IP。飞书自定义机器人目前支持关键词、IP 白名单和签名校验，IP 白名单最多配置 10 个地址或地址段；单机器人频率限制为每分钟 100 次、每秒 5 次，请求体上限为 20 KB，因此网关内部还需要做限流和消息合并。([飞书开放平台](https://open.feishu.cn/document/ukTMukTMukTM/ucTM5YjL3ETO24yNxkjN?utm_source=chatgpt.com))

------

## 二、整体架构

```text
┌──────────────────────────────────────────────┐
│                  监控服务                     │
│ 监控平台 / 自定义脚本 / 外部服务              │
│ VPS巡检 / 代理检测 / SSL检测 / Docker监控      │
└──────────────────────┬───────────────────────┘
                       │ HTTPS + HMAC/API Key
                       ▼
┌──────────────────────────────────────────────┐
│           Notification Gateway              │
│                                              │
│  ① 接入认证      ② 消息标准化                 │
│  ③ 路由规则      ④ 去重、聚合、静默           │
│  ⑤ 队列与重试    ⑥ 告警状态管理               │
│  ⑦ 模板渲染      ⑧ 发送记录与审计             │
└──────────────┬──────────┬──────────┬─────────┘
               │          │          │
        ┌──────▼───┐ ┌────▼────┐ ┌──▼─────────┐
        │ 飞书适配器 │ │Telegram │ │ 邮件/ntfy  │
        └──────────┘ └─────────┘ └────────────┘
               │
        钉钉 / 企业微信 / Slack / Teams / Discord
```

### 核心原则

**接收和发送必须解耦。**

监控服务调用网关后，网关完成认证和数据校验，立即返回：

```http
HTTP/1.1 202 Accepted
```

然后由后台任务发送飞书消息。这样即使飞书暂时超时，监控服务也不会被阻塞。

------

## 三、统一事件模型

不要只设计成：

```json
{
  "message": "服务器挂了"
}
```

建议使用结构化事件：

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

建议保留以下状态：

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

这样后续才能实现：

- 严重故障同时通知飞书和 Telegram
- 普通告警只发飞书
- 恢复通知自动关联原故障
- 重复故障不反复刷屏
- 按地区、服务、监控类型路由

------

## 四、认证设计

### 1. 不建议只有一个全局 Token

应该为每个调用方分配独立凭证：

| Client ID        | 使用方              | 允许路由         |
| ---------------- | ------------------- | ---------------- |
| `monitor-us`     | 美国 VPS 上的监控服务 | `infrastructure` |
| `proxy-check-hk` | 香港代理检测脚本    | `proxy`          |
| `docker-home`    | 家庭服务器          | `homelab`        |

某台 VPS 密钥泄露后，只需要撤销这一台，不影响其他服务。

### 2. 推荐 HMAC 签名

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

### 3. 可选的进一步保护

对于全部由自己管理的 VPS，可以再增加：

- Tailscale 或 WireGuard 私网访问
- 客户端 IP 白名单
- mTLS 客户端证书
- Cloudflare Access 或反向代理限流

但仍建议保留 HMAC，不能完全依赖 IP。

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

## 六、必须加入的告警治理能力

### 1. 去重

相同 `dedupe_key` 在一定时间内只发送一次：

```text
proxy-hk-01-connectivity
```

持续故障可以每隔 30 分钟发送一次摘要，而不是每分钟刷屏。

### 2. 聚合

当多台 VPS 同时异常时，合并成一条：

```text
【严重告警】3 个节点无法访问

- hk-vps-01
- us-vps-02
- sg-vps-01
```

### 3. 恢复通知

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

### 4. 静默和维护窗口

例如：

```text
每周日 02:00–04:00
```

这一时间段内：

- 暂不发送普通异常
- 严重异常仍可发送
- 维护结束后发送汇总

### 5. 重试与死信

建议发送失败后：

```text
1 秒 → 5 秒 → 30 秒 → 2 分钟 → 10 分钟
```

仍然失败后进入 Dead Letter Queue，并尝试备用渠道。

### 6. 升级通知

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

### 7. 确认告警

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

飞书官方文档明确说明，自定义机器人发送的消息卡片支持 URL 跳转，但不支持按钮回调。([飞书开放平台](https://open.feishu.cn/document/ukTMukTMukTM/ucTM5YjL3ETO24yNxkjN?utm_source=chatgpt.com))

------

## 七、可以接入的通知渠道

| 渠道          | 适合场景               | 建议         |
| ------------- | ---------------------- | ------------ |
| 飞书          | 日常主通知、运维群     | 第一主渠道   |
| Telegram      | 个人紧急通知、境外环境 | 第一备用渠道 |
| ntfy / Gotify | 自建手机推送           | 网关故障备用 |
| 邮件          | 长期留档、最终兜底     | 保留         |
| 钉钉          | 国内企业群             | 按需接入     |
| 企业微信      | 国内企业群             | 按需接入     |
| Slack         | 海外团队               | 按需接入     |
| Teams         | Microsoft 365 团队     | 按需接入     |
| Discord       | 个人或技术社区         | 可选         |

Slack Incoming Webhook 通过唯一 URL 接收 JSON，并支持 Slack 的消息布局组件。([Slack 开发者文档](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks?utm_source=chatgpt.com))

钉钉自定义机器人同样采用 Webhook，并支持加签安全方式，适合作为一个标准发送适配器。([钉钉开放平台](https://open.dingtalk.com/document/robots/custom-robot-access?utm_source=chatgpt.com))

Telegram Bot API 是 HTTP 接口，可以向私人会话或群组发送消息，比较适合作为飞书之外的独立备用路径。([Telegram](https://core.telegram.org/bots/api?utm_source=chatgpt.com))

Teams 建议优先接入新的 Workflows Webhook。Teams Workflow 与具体所有者关联，生产环境应设置共同所有者，避免账号离职或停用后工作流失效。([Microsoft Learn](https://learn.microsoft.com/en-us/microsoftteams/platform/webhooks-and-connectors/how-to/add-incoming-webhook?utm_source=chatgpt.com))

ntfy 支持通过简单的 HTTP POST/PUT 推送消息，并支持自托管；Gotify同样是面向自建环境的消息收发服务器。它们不是传统 IM，但非常适合作为手机端紧急推送通道。([docs.ntfy.sh](https://docs.ntfy.sh/publish/?utm_source=chatgpt.com))

------

## 八、接入层只提供稳定的统一事件 API

```http
POST /api/v1/events
```

这是 AlertBridge 唯一的正式入站契约。外部监控平台、脚本或中间服务负责把自身消息映射成统一事件模型，并按 AlertBridge 的规则完成签名。

AlertBridge 不维护以第三方产品命名的输入解析器，也不通过猜测字段完成转换。这样可以避免上游格式变化导致静默误判，缩小不可信输入面，并让事件校验、路由授权和版本演进保持一致。如果某个平台不能直接生成统一正文和签名，应在来源侧插件或独立的可信适配器中处理。

------

## 九、管理后台

第一版不需要复杂，但至少应该有：

### 渠道管理

- 飞书 Webhook 和签名密钥
- Telegram Bot Token 和 Chat ID
- SMTP 配置
- ntfy / Gotify 地址
- 测试发送
- 启用、禁用

### 客户端管理

- 创建 Client ID
- 生成和轮换 Secret
- 限定可使用的路由
- 限定调用频率
- 撤销凭证

### 路由管理

- 严重程度与渠道映射
- 消息模板
- 静默时间
- 故障升级规则

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

所有敏感 Token 只显示末尾几位。

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

## 十二、推荐的实施阶段

### 第一阶段：可用版本

实现：

- 统一 `/api/v1/events`
- API Key 或 HMAC 认证
- 飞书发送
- 文本和卡片模板
- SQLite 发送记录
- 异步队列和失败重试
- Docker Compose 部署
- `/healthz` 和 `/readyz` 健康检查

### 第二阶段：真正的通知网关

增加：

- Telegram、邮件、ntfy
- 客户端独立密钥
- 路由规则
- 去重和恢复通知
- 稳定、严格、可版本化的统一事件 API
- 管理后台
- 静默和维护窗口

### 第三阶段：告警中心

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

你的第一版可以命名为 **AlertBridge**，使用：

```text
域名：notify.tedxiong.com

入口：
POST /api/v1/events

认证：
Client ID + HMAC Secret

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
