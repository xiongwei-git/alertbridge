# AlertBridge

[![CI](https://github.com/xiongwei-git/alertbridge/actions/workflows/ci.yml/badge.svg)](https://github.com/xiongwei-git/alertbridge/actions/workflows/ci.yml)

AlertBridge 是面向个人、小团队和单节点运维环境的轻量通知网关。外部服务只向统一事件 API 提交消息；AlertBridge 负责认证、路由、静默、去重、持久化排队、失败重试和多渠道投递。

它采用 Go 单进程、SQLite 和服务端渲染后台，不依赖 Redis、PostgreSQL、Node.js 或 CDN。生产镜像基于 `scratch`，参考镜像约 5.5 MB；Compose 默认限制为 128 MiB 内存和 0.5 CPU。

## 核心能力

- 每个客户端独立 HMAC-SHA256 密钥、路由权限和持久化分钟限流；
- Nonce 防重放、严格 JSON 校验、`event_id` 幂等和 `dedupe_key` 去重；
- 按 `routing_key + severity` 投递到飞书、Telegram、ntfy 或 TLS SMTP；
- SQLite WAL 持久队列、退避重试、死信记录和人工重新入队；
- 管理后台配置客户端、通知渠道、路由、静默和密钥轮换；
- AES-256-GCM 加密动态凭据，Argon2id 管理员密码哈希；
- Docker 非 root、只读根文件系统、无 capabilities、资源限制和日志轮转。
- 内部时间统一使用 UTC，后台与通知默认按 `Asia/Shanghai` 展示，可通过 IANA 时区名覆盖。

AlertBridge 只支持一个正式入站契约：`POST /api/v1/events`。调用方负责把自身数据映射为统一事件格式，服务不会维护以第三方产品命名的私有解析入口。

## 最短生产部署

部署不需要克隆 Git 仓库，也不需要 Go 环境。服务器上只需要：

```text
compose.yaml
.env
secrets/admin_password
```

从对应 Release 下载 Compose 文件，例如：

```sh
mkdir -p /www/wwwroot/alertbridge
cd /www/wwwroot/alertbridge
curl -fsSLo compose.yaml \
  https://raw.githubusercontent.com/xiongwei-git/alertbridge/v0.2.2/compose.yaml
```

创建仅当前账户可进入的 Secret 目录和部署参数。管理员密码至少 16 字节，不要使用项目内置的通用密码：

```sh
umask 077
mkdir -p secrets
chmod 700 secrets
printf '%s\n' \
  'ALERTBRIDGE_IMAGE_TAG=v0.2.2' \
  'ALERTBRIDGE_PORT=18080' \
  'ALERTBRIDGE_ADMIN_USERNAME=admin' \
  'ALERTBRIDGE_DISPLAY_TIMEZONE=Asia/Shanghai' > .env

ADMIN_PASSWORD=$(openssl rand -base64 24 | tr -d '\n')
printf '%s\n' "$ADMIN_PASSWORD" > secrets/admin_password
chmod 644 secrets/admin_password
printf '请立即保存管理员密码：%s\n' "$ADMIN_PASSWORD"
unset ADMIN_PASSWORD
chmod 600 .env
```

`secrets` 目录的 `0700` 权限阻止其他宿主机用户访问；密码文件使用 `0644`，是为了让不同宿主机文件组下的 UID 10001 非 root 容器都能通过只读 bind mount 读取。不要把 Secret 文件放进可被其他用户进入的目录。

检查、拉取并启动：

```sh
docker compose config
docker compose pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:18080/readyz
```

镜像直接来自 `ghcr.io/xiongwei-git/alertbridge`。Compose 把宿主机密码文件只读挂载为 `/run/secrets/admin_password`，不会把密码放进容器环境变量或写入只读根文件系统。首次启动时：

- 用户名和 Argon2id 密码哈希写入 SQLite；
- 32 字节动态配置主密钥自动生成到独立的 `alertbridge-secrets` 卷；
- 客户端、通知渠道和路由保持为空，登录后台后再配置；
- 数据库已初始化后，修改 `.env` 的引导密码不会覆盖现有登录密码。

API、SQLite 和重试计算始终使用 UTC。管理后台和通知消息只在展示时转换为 `ALERTBRIDGE_DISPLAY_TIMEZONE`，默认是 `Asia/Shanghai`；如需其他地区，填写标准 IANA 时区名，例如 `UTC` 或 `America/Los_Angeles`。

服务只绑定 `127.0.0.1:18080`。生产环境应由宝塔 Nginx 终止 HTTPS，再反向代理到该地址；不要把应用端口直接开放到公网。完整步骤见[部署与运维指南](docs/deployment.md)。

## 首次进入后台

通过宝塔域名打开：

```text
https://你的域名/admin/
```

使用初始化时显示的用户名和密码登录，然后按顺序完成：

1. 在“通知渠道”创建并测试飞书、Telegram、ntfy 或 SMTP；
2. 在“路由规则”建立 `routing_key + severity` 到渠道的映射；
3. 在“客户端”创建调用方并立即保存只显示一次的 HMAC 密钥；
4. 打开“接入指南”，复制已经代入当前域名、客户端和可用路由的调用示例。

## 客户端签名

每个请求必须包含：

```text
X-Notify-Client: <客户端 ID>
X-Notify-Timestamp: <Unix 秒级时间戳>
X-Notify-Nonce: <每次请求唯一的随机值>
X-Notify-Signature: <64 位十六进制 HMAC-SHA256>
Content-Type: application/json
```

签名覆盖实际发送的原始 JSON 字节：

```text
body_hash = hex(SHA256(raw_request_body))
message   = timestamp + "\n" + nonce + "\n" + body_hash
signature = hex(HMAC-SHA256(client_secret, message))
```

完整字段、结果语义和错误码见 [API 契约](docs/api.md) 与 [OpenAPI](docs/openapi.yaml)。

## 本地开发体验

源码开发需要 Go、Docker Compose、OpenSSL 和 `curl`：

```sh
git clone https://github.com/xiongwei-git/alertbridge.git
cd alertbridge
./scripts/local-demo.sh up
```

脚本会构建本地镜像、创建隔离数据卷，并显示临时管理员密码。打开 `http://127.0.0.1:18081/admin/` 后，从空后台开始配置。该环境显式关闭 Secure Cookie，仅用于本机测试。

```sh
./scripts/local-demo.sh status
./scripts/local-demo.sh down
./scripts/local-demo.sh reset  # 删除本地演示数据卷
```

## 安全与恢复边界

- 公网只暴露宝塔 Nginx 的 HTTPS 端口；
- `secrets` 目录必须为 `0700`，其中的引导密码文件不得提交到 Git；
- `alertbridge-data` 与 `alertbridge-secrets` 是恢复对，必须同时加密备份；
- 只恢复 SQLite、不恢复原主密钥，已保存的渠道和客户端凭据无法解密；
- 反向代理或 WAF 不得改写 JSON 正文，否则 HMAC 校验失败；
- 当前版本只支持单实例，不能让多个容器写同一 SQLite 卷；
- AlertBridge 自身失效告警必须来自独立外部监控。

## 常用运维命令

```sh
docker compose ps
docker compose logs -f --tail=100 alertbridge
curl -fsS http://127.0.0.1:18080/healthz
curl -fsS http://127.0.0.1:18080/readyz
docker compose down                 # 保留数据卷
```

生产环境不要执行 `docker compose down -v`，它会同时删除数据库卷和主密钥卷。

## 开发与测试

```sh
go test ./...
go vet ./...
./test/release/run.sh
./test/e2e/run.sh
```

需要验证本地源码镜像和生产 Compose 组合时：

```sh
./test/e2e/run.sh
```

## 文档

| 文档 | 内容 |
| --- | --- |
| [部署与运维指南](docs/deployment.md) | 无仓库 Docker 部署、宝塔、升级、备份恢复和排障 |
| [管理后台指南](docs/admin.md) | 首次配置、客户端、渠道、路由和密钥 |
| [API 契约](docs/api.md) | 事件模型、认证、响应和错误码 |
| [OpenAPI](docs/openapi.yaml) | 可机器读取的 v1 API 定义 |
| [产品设计大纲](AlertBridge.md) | 产品定位、设计原则和阶段边界 |
| [架构决策](docs/decisions/) | 存储、控制面、入站 API、镜像和安全引导决策 |

## 项目结构

```text
cmd/alertbridge        进程入口和优雅退出
internal/bootstrap     首次管理员与主密钥初始化
internal/passwordhash  Argon2id 密码哈希
internal/auth          HMAC 客户端认证
internal/httpapi       HTTP 契约和安全响应
internal/admin         无前端运行时的管理后台
internal/runtimecfg    加密动态配置和原子运行快照
internal/securestore   AES-256-GCM 配置加密
internal/store         SQLite、幂等、去重和持久队列
internal/channel       飞书、Telegram、ntfy、SMTP
internal/worker        投递、重试和死信
docs                   API、部署和架构决策
test/e2e               Docker 端到端测试
test/release           GHCR 发布配置契约检查
```

## 许可证

本项目采用 [Apache License 2.0](LICENSE)。
