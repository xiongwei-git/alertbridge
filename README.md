# AlertBridge

AlertBridge 是一个面向个人、小团队和单节点运维环境的轻量通知网关。外部系统只向统一事件 API 提交消息；AlertBridge 负责认证、路由、静默、去重、持久化排队、失败重试和多渠道投递。

它采用 Go 单进程与 SQLite，不需要 Redis、PostgreSQL、Node.js 或独立前端服务。生产镜像基于 `scratch`，Docker E2E 环境中的参考体积约 5.6 MB、空闲内存约 8–10 MiB；实际资源使用会随消息量和配置变化。

## 适用场景

- 将多个监控脚本或内部服务的告警统一发送到飞书、Telegram、ntfy 或邮件；
- 集中保管通知渠道凭证，不把飞书 Webhook、Bot Token 或邮箱密码分散到每台服务器；
- 在一台小型 VPS 上运行带管理后台、持久队列和失败重试的通知服务；
- 通过宝塔 Nginx 终止 HTTPS，AlertBridge 只监听主机回环端口。

AlertBridge 只提供一个正式入站契约：`POST /api/v1/events`。调用方负责把自身数据映射成统一事件格式；服务不会猜测或转换以第三方产品命名的私有消息格式。

## 核心能力

- 每个客户端独立的 HMAC-SHA256 密钥、路由权限和持久化分钟限流；
- 5 分钟时间窗、Nonce 防重放、严格 JSON 校验和 32 KiB 请求上限；
- 按 `routing_key + severity` 将事件映射到一个或多个已启用渠道；
- `event_id` 幂等、`dedupe_key` 去重、恢复关联、故障时长和静默窗口；
- SQLite WAL 持久队列、指数退避重试、死信记录和人工重新入队；
- 飞书文本/卡片、安全关键词和机器人签名；Telegram、ntfy、TLS/STARTTLS SMTP；
- 服务端渲染管理后台：客户端、密钥轮换、渠道、路由、静默和投递记录；
- AES-256-GCM 加密动态凭证，安全会话、CSRF、严格 CSP 和登录限流；
- Docker 非 root、只读根文件系统、无 capabilities、资源限制和日志轮转。

```text
监控平台 / 脚本 / 内部服务
          │ HTTPS + HMAC
          ▼
     AlertBridge API
          │
   SQLite 事件与持久队列
          │
          ├── 飞书
          ├── Telegram
          ├── ntfy
          └── SMTP 邮件
```

## 本地快速体验

克隆仓库并进入项目目录：

```sh
git clone https://github.com/xiongwei-git/alertbridge.git
cd alertbridge
```

准备 Docker Compose、OpenSSL 和 `curl`，然后执行：

```sh
./scripts/local-demo.sh up
```

脚本会构建本地镜像、启动隔离的模拟通知渠道，并显示管理后台地址和临时管理员密码。打开：

```text
http://127.0.0.1:18081/admin/
```

发送一条本地测试事件：

```sh
./scripts/send-local-test.sh
```

查看状态或停止演示环境：

```sh
./scripts/local-demo.sh status
./scripts/local-demo.sh down
```

需要删除演示数据库和临时凭证时才执行：

```sh
./scripts/local-demo.sh reset
```

本地演示使用 `compose.e2e.yaml` 和模拟渠道，不应作为生产配置。

## Docker 生产部署

### 1. 初始化配置

```sh
./scripts/init.sh
```

初始化会生成：

- `config/config.json`：生产配置副本；
- `secrets/client-monitoring`：默认客户端 HMAC 密钥；
- `secrets/admin-password`：后台管理员密码；
- `secrets/master-key`：动态配置加密主密钥；
- `.env`：容器读取本机 Secret 文件所需的组 ID。

随后填写：

- `secrets/feishu-ops-webhook`：完整飞书机器人 Webhook；
- `secrets/feishu-ops-signing-secret`：飞书机器人签名密钥；
- `config/config.json` 中与实际环境有关的客户端、渠道、关键词和路由。

Secret 文件只保存原始值，不要加引号。`config/config.json`、`.env`、`secrets/*`、`backups/` 和运行数据库都已被 `.gitignore` 排除；不要使用 `git add -f` 强制提交它们。

### 2. 构建并启动

```sh
docker compose config
ALERTBRIDGE_VERSION="$(git rev-parse --short HEAD 2>/dev/null || printf local)" docker compose build --pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:18080/readyz
```

服务默认只绑定 `127.0.0.1:18080`，不要在安全组或主机防火墙中开放该端口。生产环境应通过 HTTPS 反向代理访问：

```text
https://你的域名/admin/
```

后台默认用户名为 `admin`，密码保存在 `secrets/admin-password`。首次启动时，JSON 中的客户端、渠道和路由会写入 SQLite；数据库已有动态配置后，请通过管理后台修改，重启容器不会覆盖这些设置。

完整的 Docker、宝塔、升级、回滚、备份、恢复和排障步骤见[部署与运维指南](docs/deployment.md)。

## 客户端接入

每个请求必须包含：

```text
X-Notify-Client: monitoring-client
X-Notify-Timestamp: <当前 Unix 秒级时间戳>
X-Notify-Nonce: <每次请求唯一的随机值>
X-Notify-Signature: <64 位十六进制 HMAC-SHA256>
Content-Type: application/json
```

签名消息使用实际发送的原始请求字节：

```text
body_hash = hex(SHA256(raw_request_body))
message   = timestamp + "\n" + nonce + "\n" + body_hash
signature = hex(HMAC-SHA256(client_secret, message))
```

登录后台后打开“接入指南”，页面会自动代入当前服务地址、客户端以及可用的路由和严重程度，并生成 Shell/Python 示例。完整字段、结果语义和错误码见 [API 契约](docs/api.md) 与 [OpenAPI](docs/openapi.yaml)。

## 安全边界

- 公网只暴露宝塔 Nginx 的 HTTPS 端口，应用端口保持回环绑定；
- 生产配置保持 `secure_cookie: true`，不要启用 E2E 环境中的不安全开关；
- `master-key` 必须与 SQLite 数据卷配套加密备份，丢失后动态凭证无法恢复；
- 反向代理或 WAF 不得改写 JSON 正文，否则 HMAC 校验会失败；
- AlertBridge 自身失效告警必须由独立外部监控发送，不能依赖 AlertBridge 自己；
- 当前版本面向单实例运行，不支持多个实例同时写入同一个 SQLite 数据库。

## 常用运维命令

```sh
# 状态与健康
docker compose ps
curl -fsS http://127.0.0.1:18080/healthz
curl -fsS http://127.0.0.1:18080/readyz

# 日志
docker compose logs -f --tail=100 alertbridge

# 应用 Compose 变更
docker compose up -d --force-recreate

# 停止但保留数据库卷
docker compose down
```

生产环境不要执行 `docker compose down -v`，`-v` 会删除 AlertBridge 的 SQLite 数据卷。

## 开发与测试

本机已安装 Go 时：

```sh
go test ./...
go vet ./...
```

也可以完全在 Docker 中验证：

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.26.5-alpine3.24 \
  sh -c 'go test ./... && go vet ./...'
./test/e2e/run.sh
```

E2E 脚本会创建并清理自己的 Compose 栈；不要把生产配置或生产端口传给它。

## 文档

| 文档 | 内容 |
| --- | --- |
| [部署与运维指南](docs/deployment.md) | Docker、宝塔 HTTPS、升级、备份恢复和排障 |
| [管理后台指南](docs/admin.md) | 动态配置、密钥和页面操作 |
| [API 契约](docs/api.md) | 事件模型、认证、响应和错误码 |
| [OpenAPI](docs/openapi.yaml) | 可机器读取的 v1 API 定义 |
| [产品设计大纲](AlertBridge.md) | 产品定位、设计原则和阶段边界 |
| [架构决策](docs/decisions/) | Go/SQLite、动态控制面和统一入站 API 的决策记录 |

## 项目结构

```text
cmd/alertbridge       进程入口和优雅退出
internal/auth         HMAC 客户端认证
internal/domain       统一事件模型与边界校验
internal/httpapi      HTTP 契约和安全响应
internal/admin        无前端运行时的管理后台
internal/runtimecfg   加密动态配置和原子运行快照
internal/securestore  AES-256-GCM 配置加密
internal/store        SQLite、幂等、去重和持久队列
internal/channel      飞书、Telegram、ntfy、SMTP
internal/worker       投递、重试和死信
config                配置模板
docs                  API、部署和架构决策
test/e2e              Docker 端到端测试
```

## 许可证

本项目采用 [Apache License 2.0](LICENSE) 开源许可证。
