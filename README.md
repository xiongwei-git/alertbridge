# AlertBridge

[![CI](https://github.com/xiongwei-git/alertbridge/actions/workflows/ci.yml/badge.svg)](https://github.com/xiongwei-git/alertbridge/actions/workflows/ci.yml)

AlertBridge 是用 Go 和 SQLite 实现的单实例通知网关。它接收 HTTP 事件，按路由和严重程度投递到飞书、Telegram、ntfy 或 SMTP，并保存事件、故障状态和每次投递记录。

[项目网站与模拟 Demo](https://opensource.tedxiong.com/alertbridge/) · [English](README.en.md) · [部署](docs/deployment.md) · [API](docs/api.md) · [MCP / Dots](docs/mcp.md) · [发布记录](CHANGELOG.md)

当前发布版本：**v0.5.1**。项目介绍页已内嵌于服务并随镜像部署，保留可选管理员 OIDC 登录；通知 API、MCP 授权和业务数据格式保持兼容。

运行服务的域名根路径 `/` 自动提供无需登录的项目介绍页，不展示业务数据，无需额外配置 Nginx 静态页面。API、MCP 和后台认证不变。见[介绍页说明及旧配置迁移](docs/showcase-deployment.md#运行服务的公开根路径介绍页)。

## 功能

| 模块 | 实现 |
| --- | --- |
| 完整事件 | `POST /api/v1/events`；HMAC-SHA256、时间窗、Nonce 防重放、事件 ID 幂等 |
| 一次性通知 | `POST /api/v1/notifications`；请求头 Bearer 令牌，服务端固定来源、路由和级别 |
| 故障状态 | `firing` / `resolved`，按客户端、路由和去重键关联；支持去重和静默窗口 |
| 出站投递 | 飞书、Telegram、ntfy、TLS SMTP；SQLite 持久队列、退避重试、死信及人工重新入队 |
| 管理后台 | 服务端渲染；配置客户端、渠道、路由、静默窗口，查询投递记录 |
| 管理员登录 | 本地密码；可选 OIDC Authorization Code + PKCE S256，按 issuer + subject 绑定管理员 |
| Agent 查询 | 可选只读 MCP；查询历史、详情与活跃故障，支持 Dots OAuth 或独立固定 Bearer |

运行时为一个 HTTP 进程、一个 SQLite 连接和一个投递 Worker，不需要 Redis、PostgreSQL 或 Node.js。后台不依赖客户端 JavaScript、外部字体或 CDN。Docker 使用 `scratch`、UID 10001、只读根文件系统、无 capabilities；默认限制为 128 MiB 内存和 0.5 CPU。

适用于个人基础设施、小团队和单节点运维。当前不支持多实例高可用、告警聚合、值班排班、超时升级、告警确认、自动修复或第三方监控格式解析。调用方负责转换为统一事件格式。

## 试用

[浏览器 Demo](https://opensource.tedxiong.com/alertbridge/#demo) 使用虚构数据，模拟故障、重复事件、恢复和 MCP 查询；不调用网关、不发送通知，也不演示真实 OIDC 登录。

真实后台和投递测试使用本地 Docker 环境，需要 Docker Compose、OpenSSL 和 curl：

```sh
git clone https://github.com/xiongwei-git/alertbridge.git
cd alertbridge
./scripts/local-demo.sh up
```

打开 `http://127.0.0.1:18081/admin/`，使用脚本显示的临时管理员密码。该环境使用独立数据卷并关闭 Secure Cookie，仅供本机测试。按“渠道 → 路由 → 客户端”的顺序配置，再通过后台接入指南发送测试事件。

```sh
./scripts/local-demo.sh status
./scripts/local-demo.sh down   # 保留本地数据
./scripts/local-demo.sh reset  # 删除本地演示数据，执行前确认不再需要
```

## 生产部署

服务器需要 Docker Engine、Compose v2、HTTPS 反向代理、curl 和 OpenSSL。使用与镜像同版本的 Compose：

```sh
mkdir -p /www/wwwroot/alertbridge
cd /www/wwwroot/alertbridge
curl -fsSLo compose.yaml \
  https://raw.githubusercontent.com/xiongwei-git/alertbridge/v0.5.1/compose.yaml
```

按 [部署指南](docs/deployment.md) 创建 `.env` 和文件型管理员 Secret，将 `ALERTBRIDGE_IMAGE_TAG` 固定为 `v0.5.1`，再启动服务。基础部署使用密码登录；OIDC 另需同版本 `compose.oidc.yaml`、身份提供方和受限配置文件。

应用只绑定 `127.0.0.1:18080`，由 Nginx 提供 HTTPS。唯一镜像发布源改为阿里云 ACR，支持 `linux/amd64` 与 `linux/arm64`：

```text
crpi-2q1p9uwje81xbddu.cn-shanghai.personal.cr.aliyuncs.com/ted_docker_mirror/ted
```

当前仓库拉取需要授权账号及 `docker login`，不支持匿名部署。现有 `v0.5.1` 镜像已在 ACR；旧版本 Compose 不会重写，请按[部署指南](docs/deployment.md)在 `.env` 明确设置 `ALERTBRIDGE_IMAGE`。后续版本直接构建并发布到 ACR，不再发布 GHCR 或执行跨仓库复制；历史 GHCR 镜像保留。公网地址用于 CI 和通用部署，阿里云同地域 ECS 可显式选择 VPC 地址。SBOM 与来源证明保留并签证为 GitHub 构建产物，见 [ADR-014](docs/decisions/ADR-014-acr-only-image-publishing.md)。此次流程调整不代表发布了新版本。

## 接入接口

| 需求 | 接口与凭据 | 成功响应 |
| --- | --- | --- |
| 只发送标题和正文 | `/api/v1/notifications`；后台创建的轻量 Bearer 令牌 | HTTP 200 |
| 幂等重试、动态路由、故障发生和恢复 | `/api/v1/events`；HMAC 客户端 | HTTP 202 |
| Agent 读取指定路由的告警 | `/mcp`；独立 OAuth 或固定 Bearer | MCP JSON-RPC |

通知 API 的 HTTP 成功响应不代表外部渠道送达。检查响应中的 `outcome` 和后台投递记录：`queued` 表示创建了投递任务，`suppressed` 表示事件被抑制，`duplicate` 表示事件 ID 已存在。

轻量通知示例（请替换为自己的部署与专用测试令牌）：

```sh
curl --fail-with-body --silent --show-error \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer 替换为轻量接入令牌' \
  --data-binary '{"title":"磁盘空间不足","message":"根分区使用率超过 90%","category":"disk"}' \
  'https://notify.example.com/api/v1/notifications'
```

轻量入口状态固定为 `info`，不能创建或关闭故障。完整事件字段、签名和响应见 [API 契约](docs/api.md)、[OpenAPI](docs/openapi.yaml) 与 [Python 示例](examples/python/README.md)。

## 管理员 OIDC 与 MCP OAuth

这是两条独立认证流程：

- **管理员 OIDC**：AlertBridge 是身份提供方的客户端，验证 ID Token 后创建本地管理员会话。只接受配置的 issuer + subject；用户名和邮箱不授予权限。退出仅清除本应用会话。当前以 Tinyauth 验证，配置和限制见 [OIDC 部署](docs/deployment.md#可选-tinyauth-oidc-统一登录)。
- **MCP OAuth**：AlertBridge 为 Dots 的只读查询签发令牌。管理员先用密码或 OIDC 登录，再批准指定路由的读取权限；不向 Dots 提供身份提供方的 Token。

MCP 提供 `list_alerts`、`get_alert`、`list_active_incidents`。最近十条使用 `{"order":"desc","limit":10}`。它不确认、恢复或修改故障，也不主动唤醒 Agent。配置、分页和撤销见 [MCP 指南](docs/mcp.md)，任务示例见 [Dots 接入](docs/dots-example.md)。

## 数据与恢复

- `alertbridge-data` 保存 SQLite，`alertbridge-secrets` 保存配置加密主密钥，两者必须成对备份和恢复。
- HMAC 密钥和渠道凭据使用 AES-256-GCM 加密；管理员密码保存 Argon2id 哈希；轻量令牌及 MCP 令牌只保存摘要。
- 启用 OIDC 时，另外备份宿主机 OIDC 配置文件、Compose overlay 和身份提供方配置；数据库卷不包含这些文件。
- 内部时间与 API 使用 UTC，后台和通知默认按 `Asia/Shanghai` 展示，可用 `ALERTBRIDGE_DISPLAY_TIMEZONE` 修改。
- 网关失效通知应由外部监控直接发送。单机网关不能在自身主机失效时继续投递。

不要在生产栈执行 `docker compose down -v`。具体备份、升级和回滚步骤见 [运维指南](docs/deployment.md)。

## 开发与文档

```sh
go test ./...
go vet ./...
./test/release/run.sh
./test/e2e/run.sh
./scripts/build-site.sh
python3 test/site/test_example.py
```

| 文档 | 内容 |
| --- | --- |
| [部署与运维](docs/deployment.md) | Compose、Nginx、OIDC、升级、备份恢复和排障 |
| [管理后台](docs/admin.md) | 登录、配置顺序、路由、渠道与投递记录 |
| [API](docs/api.md) / [OpenAPI](docs/openapi.yaml) | 请求、认证、结果语义和错误码 |
| [MCP](docs/mcp.md) / [Dots 示例](docs/dots-example.md) | Agent 查询、OAuth、分页和权限 |
| [架构与实现范围](AlertBridge.md) | 数据流、状态处理、部署及限制 |
| [架构决策](docs/decisions/) | 设计依据和历史变更 |
| [静态网站部署](docs/showcase-deployment.md) | 官网构建、校验、发布和回滚 |
| [文档维护记录](docs/opensource-review.md) | 本轮核对项与未实现能力 |

贡献流程见 [CONTRIBUTING](CONTRIBUTING.md)，漏洞报告见 [SECURITY](SECURITY.md)，交流遵守 [行为准则](CODE_OF_CONDUCT.md)。项目由个人维护，不提供可用性或支持 SLA。

## 许可证

[Apache License 2.0](LICENSE)。AlertBridge 是独立开源项目。
