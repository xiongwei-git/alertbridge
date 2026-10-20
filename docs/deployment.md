# AlertBridge Docker 部署与运维指南

本文覆盖无仓库 Docker Compose 部署、宝塔 Nginx、首次配置、升级回滚、备份恢复和排障。AlertBridge 是单实例服务，不允许多个容器同时写入同一个 SQLite 数据卷。

v0.5.1 起域名根路径 `/` 自动提供内嵌项目介绍页，无需登录、不读取业务数据，也无需单独托管静态文件。其他路径继续走原代理与认证。旧静态根页扩展需停用，见[介绍页及迁移说明](showcase-deployment.md#运行服务的公开根路径介绍页)。

## 1. 推荐拓扑

```text
公网客户端
    │ HTTPS :443
    ▼
宝塔 Nginx（TLS、域名、可选限流）
    │ HTTP 127.0.0.1:18080
    ▼
AlertBridge 容器
    ├── alertbridge-data     SQLite 与持久队列
    └── alertbridge-secrets  自动生成的配置加密主密钥
```

网关最好部署在独立于关键被监控服务的 VPS 上。它自身的存活检测应由外部系统直接检查 `/healthz`，失效通知不能再经过 AlertBridge。

## 2. 前置条件

- Linux VPS、Docker Engine 和 Docker Compose v2；
- 宝塔 Nginx 或等价 HTTPS 反向代理；
- 一个解析到 VPS 的域名和有效 TLS 证书；
- `curl` 和 OpenSSL，用于下载 Compose、生成初始密码和健康检查；
- 服务器通过 NTP 对时，签名请求默认只接受前后 5 分钟。
- 已获授权的 ACR 拉取账号与 Registry 密码：当前仓库匿名拉取返回 401，不能仅凭公开源码匿名拉镜像。

不需要克隆仓库，不需要 Go、Git、Redis 或 PostgreSQL。

## 3. 创建部署目录

```sh
mkdir -p /www/wwwroot/alertbridge
cd /www/wwwroot/alertbridge
curl -fsSLo compose.yaml \
  https://raw.githubusercontent.com/xiongwei-git/alertbridge/v0.6.0/compose.yaml
```

也可以在宝塔文件管理器中新建 `compose.yaml`，从对应 GitHub Release 复制同版本文件。生产环境应锁定完整版本号，不要长期使用 `latest`。

## 4. 设置管理员引导凭据

创建私有 Secret 目录和 `.env`：

```sh
cd /www/wwwroot/alertbridge
umask 077
mkdir -p secrets
chmod 700 secrets
printf '%s\n' \
  'ALERTBRIDGE_IMAGE=crpi-2q1p9uwje81xbddu.cn-shanghai.personal.cr.aliyuncs.com/ted_docker_mirror/ted' \
  'ALERTBRIDGE_IMAGE_TAG=v0.6.0' \
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

约束：

- 密码必须为 16–1024 字节，不能包含换行或 NUL；
- 不存在项目内置的 `admin/admin` 通用密码；
- `secrets` 目录必须为 `0700`，其他宿主机用户无法进入；
- 密码文件为 `0644`：owner 可读写，容器 UID 10001 可在不同宿主机文件组下通过只读 bind mount 读取；仅当父目录保持 `0700` 时这个权限组合才安全；
- Compose 把密码文件挂载为 `/run/secrets/admin_password`，密码不会进入容器环境变量或只读根文件系统；
- 首次启动保存 Argon2id 哈希，不保存管理员明文密码；
- 数据库已有管理员后，引导用户名和密码不再覆盖现有凭据。

`.env` 和 `secrets/admin_password` 都需保留，以便后续 `docker compose up` 重新创建容器。数据库已有管理员后，修改密码文件不会修改后台登录密码。

## 5. 拉取并启动

先登录公网 Registry（交互式输入密码，不要写进命令参数、`.env` 或容器环境）：

```sh
docker login crpi-2q1p9uwje81xbddu.cn-shanghai.personal.cr.aliyuncs.com -u '<获授权的 Registry 登录名>'
```

当前主分支 Compose 默认使用公网 ACR；已发布的 `v0.5.1` 等历史 Compose 文件不会重写，所以上文 `.env` 显式设置 `ALERTBRIDGE_IMAGE`，使旧文件也从 ACR 拉取。当前发布为 `v0.6.0`，历史镜像仍保留。镜像仓库需授权账号访问；公开 GitHub 仓库不等于公开 ACR 拉取权限。

```sh
docker compose config
docker compose pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:18080/healthz
curl -fsS http://127.0.0.1:18080/readyz
```

预期健康响应：

```json
{"status":"ok"}
{"status":"ready"}
```

首次启动会：

1. 创建 SQLite 表和管理员密码哈希；
2. 使用系统安全随机源生成 32 字节主密钥；
3. 以 `0600` 写入独立的 `alertbridge-secrets` 命名卷；
4. 以空客户端、空渠道、空路由进入管理后台。

Compose 默认启用：回环端口、非 root UID 10001、只读根文件系统、无 capabilities、`no-new-privileges`、128 MiB 内存、0.5 CPU、进程限制和日志轮转。

服务内部、SQLite 和 API 时间语义保持 UTC。管理后台与通知内容在输出时转换为 `.env` 中的 `ALERTBRIDGE_DISPLAY_TIMEZONE`，默认是 `Asia/Shanghai`。该值必须是 IANA 时区名，例如 `UTC`、`Asia/Shanghai` 或 `America/Los_Angeles`；非法值会阻止服务启动，避免静默显示错误时间。

### 阿里云同地域 ECS 使用 VPC 地址（可选）

CI 推送和通用部署使用公网地址。只有能够访问该实例内网端点的阿里云同地域 ECS 才应将 `.env` 中已有的 `ALERTBRIDGE_IMAGE` 一行替换为：不要重复追加同名变量，不要将 VPC 地址用于普通 VPS、本机或 GitHub 托管 Runner。

```sh
ALERTBRIDGE_IMAGE=crpi-2q1p9uwje81xbddu-vpc.cn-shanghai.personal.cr.aliyuncs.com/ted_docker_mirror/ted
```

首次改用 VPC 地址时，需另行登录对应的 VPC Registry（公网登录不会自动替代 VPC 登录）：

```sh
docker login crpi-2q1p9uwje81xbddu-vpc.cn-shanghai.personal.cr.aliyuncs.com -u '<获授权的 Registry 登录名>'
docker compose config
docker compose pull
docker compose up -d
```

`ALERTBRIDGE_IMAGE` 仍可覆盖镜像地址，升级与回滚仍然只修改 `ALERTBRIDGE_IMAGE_TAG`。ACR 是唯一镜像发布源，未来版本不再发布 GHCR、也不再从 GHCR 复制；旧 GHCR 镜像不删除。GitHub Actions 一次构建两种架构并生成 SBOM/来源证明，验证每个平台及证明的摘要绑定后，直接将本地构建的运行镜像上传 ACR，逐个标签核验索引摘要。当前个人版仓库实际拒绝了证明格式，因此证明文件保存为 GitHub Actions `release-evidence-vX.Y.Z` 产物（90 天），由 GitHub artifact attestation 签证。长期审计应及时下载归档，见 [ADR-014](decisions/ADR-014-acr-only-image-publishing.md)。

## 6. 宝塔 HTTPS 反向代理

在宝塔“网站”中添加正式域名、申请证书并强制 HTTPS，反向代理目标为：

```text
http://127.0.0.1:18080
```

推荐 Nginx 位置配置：

```nginx
location / {
    client_max_body_size 32k;
    proxy_connect_timeout 5s;
    proxy_read_timeout 15s;
    proxy_send_timeout 15s;

    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_pass http://127.0.0.1:18080;
}
```

`Host` 和 `X-Forwarded-Proto` 不能省略，后台“接入指南”使用它们生成当前服务地址。公网只开放 443 和按需用于跳转的 80；不要开放 18080。WAF 不得改写 API 原始 JSON，否则 HMAC 会失效。

启用 Dots OAuth 时，宝塔默认的 `location /.well-known` 可能让发现接口返回 Nginx 404。在站点 `server` 内为以下三个路径分别添加精确匹配的反向代理（保留原有 ACME 验证规则）：

- `/.well-known/oauth-protected-resource`
- `/.well-known/oauth-protected-resource/mcp`
- `/.well-known/oauth-authorization-server`

每个路径使用下面的配置，将第一行路径替换为对应值：

```nginx
location = /.well-known/oauth-protected-resource/mcp {
    proxy_pass http://127.0.0.1:18080;
    proxy_set_header Host $http_host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

执行 `nginx -t` 成功后平滑重载，确认上述三个 OAuth 发现路径返回 JSON；未认证访问 `/mcp` 返回带 `WWW-Authenticate` 的 401 是正常行为。

## 7. 首次后台配置

打开：

```text
https://你的域名/admin/
```

基础部署使用初始化时保存的账号密码登录；已启用 OIDC 的部署使用登录页按钮，然后：

1. 在“通知渠道”保存渠道凭据并测试发送；
2. 在“路由规则”建立每个 `routing_key + severity` 的目标渠道；
3. 在“客户端”按来源能力创建轻量令牌或 HMAC 调用身份，立即保存只显示一次的凭据；
4. 在“接入指南”复制对应示例并提交测试通知或事件；
5. 在“投递记录”确认最终发送结果。

所有业务配置都写入 SQLite。需要再次使用的敏感字段由独立主密钥进行 AES-256-GCM 加密；轻量令牌只保存不可恢复的摘要。修改成功后立即切换运行快照，无需重启。

### 7.1 宝塔自定义消息通道接入

宝塔只能配置固定 URL、请求头和带变量的 JSON 正文时，不需要额外部署签名适配器：

1. 先创建并启用通知渠道；
2. 建立目标 `routing_key + severity` 路由规则；
3. 在“客户端”创建轻量接入令牌，选择这条规则，限额建议从 10 次/分钟开始；
4. 立即保存只显示一次的令牌。

在宝塔自定义消息通道中填写：

```text
请求方式：POST
URL：https://你的域名/api/v1/notifications
```

请求头：

```json
{
  "Authorization": "Bearer 粘贴轻量接入令牌",
  "Content-Type": "application/json"
}
```

自定义正文：

```json
{
  "title": "$title",
  "message": "$msg",
  "category": "$type"
}
```

宝塔变量只填充标题、正文和可选分类。来源、路由、级别由令牌在服务端固定，状态始终是 `info`，因此不会产生无法关闭的活跃故障。AlertBridge 持久化入队后为该轻量接口返回 `200 OK`，兼容宝塔只接受 HTTP 200 的测试逻辑；返回体中的 `outcome=queued` 仍表示异步投递，最终结果在后台“投递记录”确认。令牌不要拼进 URL；泄露时在后台停用或轮换即可。需要故障发生/恢复闭环时仍使用 HMAC 完整事件接口。

上述字段来自[宝塔自定义消息通道指南](https://www.bt.cn/bbs/thread-143326-1-1.html)中的 `$title`、`$msg`、`$type`。原帖界面以 Linux 面板 9.5.0 为例，其他版本的菜单名称可能不同，但请求 URL、请求头和自定义正文的配置原则相同。

## 8. 上线验收

可选 Dots 查询：在 `.env` 配置 `ALERTBRIDGE_PUBLIC_URL=https://实际服务域名` 和 `ALERTBRIDGE_MCP_ROUTES=base`，使用新版 Compose 和镜像重新创建服务。Dots 选择 OAuth、Client ID 填 `alertbridge-dots`、Client Secret 留空，登录现有管理员账号并确认只读授权。Codex/OpenClaw 可另配 `ALERTBRIDGE_MCP_TOKEN_SHA256` 使用固定 Bearer。默认关闭；完整回调、授权撤销与配置步骤见 [MCP 接入指南](mcp.md)。修改公网地址或读取路由会使旧 OAuth 授权失效，固定 Bearer 需另行轮换摘要。

1. HTTPS 域名的 `/healthz` 和 `/readyz` 正常；
2. 管理员可以登录，Cookie 通过 HTTPS 正常保存；
3. 每个渠道的后台测试消息到达；
4. 每个 HMAC 客户端至少允许一个已配置路由，每个轻量令牌固定一条已配置规则；
5. 轻量测试通知得到 `200 OK`，HMAC 签名测试事件得到 `202 Accepted`；
6. 原样重放同一 HMAC Nonce 得到 `401`；
7. 新 Nonce 配相同 `event_id` 得到 `outcome=duplicate`，渠道不重复发送；
8. 日志中没有 Webhook、Token、密码、签名或完整请求正文。

## 9. Compose 文件选择

基础部署使用 `docker compose`。若启用本文末尾的 OIDC overlay，将以下配置加入 `.env`：

```dotenv
COMPOSE_FILE=compose.yaml:compose.oidc.yaml
ALERTBRIDGE_OIDC_CONFIG_FILE=/www/wwwroot/alertbridge/secrets/oidc.json
```

Linux Compose 会从 `.env` 的 `COMPOSE_FILE` 选择两个文件，后续 `config`、`pull`、`up`、`stop` 和 `start` 都沿用 OIDC Secret 挂载。首次启用和排错时也可显式使用 `docker compose -f compose.yaml -f compose.oidc.yaml ...`。不要在只加载基础 Compose 的情况下重建已启用 OIDC 的服务。

如果部署已将等价 OIDC 设置合入自己的 Compose，就保留原文件和管理方式，不要直接覆盖为仓库的基础文件。

## 10. 日常运维与升级

```sh
docker compose ps
docker compose logs --tail=100 alertbridge
docker compose pull
docker compose up -d alertbridge
curl -fsS http://127.0.0.1:18080/readyz
```

升级前备份，记录旧镜像标签、镜像 ID、Compose 文件和代理配置。修改 `.env` 的 `ALERTBRIDGE_IMAGE_TAG` 为已发布版本，再执行以上检查。不要将 `main` 的 Compose 与旧镜像混用；启用 OIDC 时同时保留匹配版本的 overlay。

v0.5.0 增加管理员 OIDC，不修改通知 API、MCP 授权和业务数据格式。未启用 OIDC 的部署可以只升级镜像；启用 OIDC 的部署还依赖受限配置文件和身份提供方发现接口。MCP 工具 Schema 变化后，在客户端刷新工具定义。

回滚镜像前核对 Release 的数据兼容说明。v0.5.0 如需退回 v0.4.x，须同时撤销 OIDC overlay/配置并恢复密码登录路径；不要保留新配置却假定旧镜像支持 OIDC。若版本包含不兼容数据迁移，先在隔离环境验证恢复，再决定切换。正常回滚不恢复数据库或身份提供方账号，避免覆盖期间新增的数据。

停止服务并保留数据使用 `docker compose down`。不要对生产栈执行 `docker compose down -v`。

## 11. 备份

数据库和主密钥卷是一组恢复资料，不能只备份 SQLite。以下示例会短暂停止应用，使用默认 Compose 项目名 `alertbridge`；自定义项目名时先通过 `docker volume ls` 核对实际卷名，不要照搬。

```sh
cd /www/wwwroot/alertbridge
umask 077
backup_dir="$PWD/backups/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup_dir"

docker compose stop alertbridge
# 即使后续备份失败，也重新启动原服务。
trap 'docker compose start alertbridge' EXIT HUP INT TERM

docker run --rm \
  -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
  -v alertbridge_alertbridge-data:/data:ro \
  -v alertbridge_alertbridge-secrets:/keys:ro \
  -v "$backup_dir:/backup" alpine:3.23 \
  sh -ec 'tar -czf /backup/alertbridge-data.tar.gz -C /data .
          tar -czf /backup/alertbridge-secrets.tar.gz -C /keys .
          chown "$HOST_UID:$HOST_GID" /backup/*.tar.gz'

cp -p compose.yaml .env "$backup_dir/"
cp -Rp secrets "$backup_dir/"
if [ -f compose.oidc.yaml ]; then cp -p compose.oidc.yaml "$backup_dir/"; fi
chmod -R go-rwx "$backup_dir"
docker compose start alertbridge
trap - EXIT HUP INT TERM
printf 'Backup directory: %s\n' "$backup_dir"
```

将脚本保存为文件并以 `sh -e` 执行，任一步失败就停止，不把部分文件当作完整备份。验证两个归档可读后，再写入自己的备份完成标记。备份不包含 Nginx 配置，需要另外保存。

OIDC 示例将配置保存在 `secrets/oidc.json`，会随目录备份。如果 `.env` 指向其他位置，还要单独复制该文件并记录原路径、属主和权限。身份提供方的 Client、账号、签名密钥和数据库按其自身方法备份；不要把这些资料混入网站静态产物。

备份含有密码哈希、可解密凭据的主密钥及可能存在的 OIDC client secret，必须进入加密存储。保留最近一次完整且验证通过的恢复集；不得提交 Git 或放入公开网站目录。

## 12. 恢复演练与切换

优先恢复到独立 Compose 项目和数据卷，不删除原卷。新建私有工作目录，复制备份的 Compose、`.env` 和 `secrets/`，并完成以下调整：

1. 将 `.env` 的 `ALERTBRIDGE_PORT` 改为未占用的 loopback 端口，例如 `18084`。
2. OIDC 配置文件路径若已改变，修正 `.env`；恢复文件推荐 UID 10001、0400，父目录 0700。OIDC callback 仍须与身份提供方登记地址一致。
3. 确认 `ALERTBRIDGE_IMAGE_TAG` 与备份版本一致，并保留 `.env` 的 overlay 选择。
4. 创建隔离项目的容器与卷但不启动：

```sh
docker compose -p alertbridge-restore create alertbridge
```

确认 `alertbridge-restore_alertbridge-data` 与 `alertbridge-restore_alertbridge-secrets` 是本次新建空卷后，将归档解压到它们：

```sh
backup_dir=/绝对路径/完整备份目录
docker run --rm \
  -v alertbridge-restore_alertbridge-data:/data \
  -v alertbridge-restore_alertbridge-secrets:/keys \
  -v "$backup_dir:/backup:ro" alpine:3.23 \
  sh -ec 'tar -xzf /backup/alertbridge-data.tar.gz -C /data
          tar -xzf /backup/alertbridge-secrets.tar.gz -C /keys
          chown -R 10001:0 /data /keys
          chmod 700 /data /keys
          chmod 600 /keys/master.key'
```

不要直接启动恢复副本：备份中的待投递任务可能向真实渠道重新发送消息。先将副本置于禁止出站的隔离网络，再启动 `docker compose -p alertbridge-restore up -d alertbridge`；或者只检查归档与数据库完整性，待正式切换前再启动。OIDC 在线发现和渠道测试需要受控出站，应在演练计划中单独放行，不能把恢复副本当成无副作用的只读服务。

完成数据、登录和配置核对后，停止原实例，按确认过的发布步骤切换 Nginx 上游或正式端口；保留原卷与配置用于回退。不要同时让原实例与恢复副本向真实渠道投递。仅容器显示 `running` 不能证明恢复完整。

## 13. 常见故障

| 现象 | 检查项 |
| --- | --- |
| 找不到引导密码 | 文件存在且为 16–1024 字节；父目录 0700、文件 0644；Compose 配置通过 |
| 修改引导密码后旧密码仍有效 | 引导值仅用于空数据库，不会覆盖已有管理员密码 |
| 主密钥读取失败 | 核对主密钥卷与数据库恢复集，不能重新生成密钥绕过 |
| 登录后返回登录页 | 使用 HTTPS；检查 Secure Cookie、代理 Host 和 X-Forwarded-Proto |
| OIDC 启用后重启变回密码登录 | 检查是否遗漏 overlay，或 `.env` 中 COMPOSE_FILE 未被加载 |
| OIDC 启动失败 | 文件路径、UID/权限、issuer 发现、HTTPS 同主机端点和 RS256 支持 |
| OIDC 回调拒绝 | 精确 callback、Client ID、独立 subject、state/nonce、服务器对时；不要记录完整回调 URL |
| `/readyz` 返回 503 | 卷、磁盘空间、SQLite 和启动日志 |
| 轻量 API 401 | 令牌请求头、启用状态、轮换；不要使用 HMAC 密钥或 OIDC Token |
| HMAC API 401 | Client ID、时间窗、Nonce 重放和原始字节签名 |
| API 429 | 等待 Retry-After，核对来源调用频率和限额 |
| 403 route_forbidden | 客户端的路由授权 |
| 422 route_unavailable | 该路由与级别是否绑定已启用渠道 |
| 测试后活跃故障未关闭 | 用相同客户端、路由和去重键发送 resolved；普通测试使用 test |
| 飞书 19024 | 后台关键词需匹配机器人要求 |
| 镜像拉取失败 | ACR 对应端点登录、仓库拉取权限、已发布完整版本与网络；VPC 仅适用于可达该内网的 ECS |
| 时间显示错误 | IANA 时区配置；不修改服务器时钟补偿展示时差 |

## 可选 Tinyauth OIDC 统一登录

### 配置文件

需要 v0.5.0 或更新镜像，以及一个符合以下约束的身份提供方。当前实现以 Tinyauth 验证，不代表任意 OIDC 服务即插即用：

- 固定 HTTPS issuer，发现、授权、Token、JWKS 端点都在该 issuer 的主机。
- 支持 Authorization Code、PKCE S256、RS256 ID Token 和 Client Secret Basic。
- 每个应用单独注册 Client，精确登记 `https://notify.example.com/auth/oidc/callback`。
- 管理员由 issuer + subject 固定绑定；不要以用户名、邮箱或首次登录授予管理员。

在宿主机 `secrets/` 私有目录内创建 `oidc.json`（以下只是字段示例，不可原样使用）：

```json
{
  "issuer": "https://auth.example.com",
  "client_id": "alertbridge-admin",
  "client_secret": "<至少32字节的独立高熵秘密>",
  "redirect_url": "https://notify.example.com/auth/oidc/callback",
  "admin_subject": "<此Client下明确登记的管理员subject>",
  "password_fallback": true
}
```

```sh
chmod 700 secrets
chown 10001:0 secrets/oidc.json
chmod 400 secrets/oidc.json
curl -fsSLo compose.oidc.yaml \
  https://raw.githubusercontent.com/xiongwei-git/alertbridge/v0.6.0/compose.oidc.yaml
```

将第 9 节的 `COMPOSE_FILE` 和绝对文件路径加入 `.env`，确认镜像标签为 `v0.5.0` 或更高的兼容正式版本，然后：

```sh
docker compose config
docker compose pull
docker compose up -d alertbridge
```

`password_fallback=true` 保留折叠的本地密码入口；false 时 OIDC 启用后的登录页不提供密码入口。引导密码文件仍是基础 Compose 的必需 Secret。变更身份或停用备用入口前保留可执行的恢复方案。

Tinyauth 的 subject 与 Client 相关，必须核对部署版本和该账号在这个 Client 下的精确值，不得照搬其他应用的 sub。现有登录按钮文字固定为“使用 Ted 个人认证服务登录”，当前不能通过配置改名；这是 UI 限制，不表示部署必须使用维护者的认证服务。

### 回调代理与日志

在通知服务的 Nginx 站点增加以下精确 location；不是在官网静态站中添加。保留原 OAuth 发现代理和 ACME 规则：

```nginx
location = /auth/oidc/callback {
    proxy_pass http://127.0.0.1:18080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    access_log off;
    error_log /dev/null crit;
}
```

用宝塔的反代配置入口保存并同步面板配置，先备份，再执行 `nginx -t` 和平滑重载。不要在应用、反代或身份提供方日志中保存包含 code/state 的完整 callback URL。不要对整个域名添加 `auth_request`，以免阻断健康检查、通知调用和 MCP。

### 登录验证和回退

验证登录页等待点击、身份提供方认证与授权、callback 返回后台、应用退出，以及再次登录时身份提供方会话复用。TOTP 是否出现取决于提供方配置和现有会话，不是 AlertBridge 提供的能力。应用退出只清除本地会话，不宣称身份提供方全局退出。

错误签名、issuer、audience、state、nonce、过期、重复回调和未登记 subject 由隔离测试覆盖；真实部署仍需目标账号完成浏览器登录。后台 OIDC 登录不替代 Dots 的 OAuth 授权；管理员可用 OIDC 登录后再确认 MCP 只读授权。

回退时恢复旧 Compose/.env 和代理配置、原镜像标签，保留现有业务卷。正常回退不恢复身份提供方数据库、账号、TOTP 或签名密钥。完整数据备份只用于明确的数据故障恢复。

## 官网静态站

[项目网站](https://opensource.tedxiong.com/alertbridge/) 与通知运行服务分别部署。网站构建、发布及回滚见 [静态站部署](showcase-deployment.md)。网站不需要通知镜像、数据库或认证配置。


## 可选 MCP Events

现有 MCP 认证之外，Compose 增加 `ALERTBRIDGE_MCP_EVENTS=1` 和 `ALERTBRIDGE_MCP_EVENT_CALLBACK_HOSTS`。后者只填实际 Events 平台回调的精确 DNS 主机名（逗号分隔），不填 URL、通配或 OAuth 登录回调。默认关闭；配置错误拒绝启动。出站仅允许 HTTPS/443 公网地址，不需要开放新入站端口或 SSE 配置。

新订阅与回调任务保存在现有 SQLite；URL/签名密钥由现有主密钥加密，仍必须成对备份和恢复 `alertbridge-data`、`alertbridge-secrets`。升级自动创建订阅及任务代次表，不改变旧表字段或增加连接/Worker。回滚旧镜像前应先在平台取消监控；旧版不投递内部回调任务。关闭 Events 会停止发送，未处理内部任务按不可用终止，重新开启不承诺补发。每小时清理过期/撤销订阅。

启用后需重新扫描插件，明确建立监控任务并进行真实 Dots 全链路验收；本地测试和 Webhook 2xx 都不代表 Agent 已完成任务。协议、安全与验收步骤见 [MCP 指南](mcp.md#主动触发)。2026-10-19 已按用户授权将生产升级到私有 SDK 候选并启用此能力；此次轻量验收不包含生产真实 Dots 自动执行，详见文末迁移记录。


### 2026-10-18 独立 Dots 验收环境

测试入口为 `https://test.tedxiong.com/mcp`；宝塔反代项目名为 `test.tedxiong.com`，不与生产 `notify.tedxiong.com` 共用应用配置。测试 Compose 项目 `alertbridge-mcp-test`，应用仅监听 `127.0.0.1:18089`；目录 `/www/wwwroot/test.tedxiong.com/app`。独立 `alertbridge-mcp-test_alertbridge-data`、`alertbridge-mcp-test_alertbridge-secrets` 卷，私有配置/基线在该目录 `.private/`。管理员口令通过独立 file-backed Secret 初始化，浏览器使用 Secure Cookie；仅模拟通知渠道允许容器内 HTTP，MCP 回调仍严格 HTTPS/公网/域名白名单。

镜像是本地交叉构建并校验传输摘要的 `alertbridge:mcp-test` 与模拟接收器，未发布到镜像仓库，不是正式发布版本。生产容器 ID 与 Compose/.env/Nginx/反代 JSON 四份摘要在部署前后相同，生产 readyz=200。测试独立 HTTPS、OAuth discovery、MCP discovery/queries/events catalog 及真实 Dots 订阅、签名回调、自动唤醒和事件字段读取已通过。当前精确回调白名单为 `connectors.api.openai.com`；初始 `callback.example` 占位值已替换。订阅切换为仅 `sandbox` 路由且 `critical` 严重度后，真实 `info` 告警未生成 MCP 投递任务，`critical` 告警首次投递返回 200；用户随后确认收到匹配 `critical` 告警的自动回复。取消订阅后服务端订阅表为零，新 `critical` 模拟告警只有模拟渠道投递，没有 MCP 投递任务。个别任务回复延迟的具体平台原因未确定，不能将回调 200 等同于立即回复。

停止/回滚测试仅在测试目录、带 `--project-name alertbridge-mcp-test --env-file .private/compose.env -f compose.yaml -f compose.test.yaml` 的命令中进行；`down --remove-orphans` 保留两份恢复卷，禁止误操作生产项目或使用 `down -v`。本节是2026-10-18测试环境记录；2026-10-19生产升级见文末。


### 测试出站网络：当前限制与长期候选

验收时临时通路仅改变测试容器的回调解析及其网络命名空间，原始 TLS 经 SSH 字节隧道出站，应用仍验证 OpenAI 主机证书。宿主无对应测试 DNAT 规则；应用保持 UID 10001、只读文件系统和 `cap_drop: ALL`。该通路依赖本机在线，设置约两小时自动回滚，不是长期部署。过滤与取消验收后已主动回滚并停止隧道；测试实例仍可查询，但恢复真实 Events 订阅前需先配置可用出站通路。测试私有目录 `.private/egress-test/rollback.py` 保存带配置摘要保护的回滚入口；测试数据和 Secret 恢复卷保留。

长期候选为测试专用出站网络命名空间，经独立加密隧道使用现有新加坡 VPS 出站。2026-10-18 的只读探测已验证该 VPS 可与 OpenAI 回调主机完成 TLS 1.3；未安装 WireGuard 或 Docker，也未修改其配置。可考虑 WireGuard 网关，或仅允许固定 OpenAI 回调目的主机的服务器间 SSH 隧道；二者均未部署。

实施前应明确新加坡端的软件、端口、独立密钥及服务管理范围。应用本身不增加网络管理能力，不读取通用代理环境变量；保留 HTTPS、精确主机白名单、DNS 公网地址校验、连接地址绑定和无重定向策略。长期配置需覆盖可信 DNS、容器重建后的网络恢复、断线重连、重启启动顺序和通路中断时禁止回落到不可用直连，并完成关闭本机后、测试容器重建后及隧道恢复后的真实 Dots 回调验收。服务器全局 DNS、默认路由和生产容器均不在变更范围内。

### Isolated MCP Events egress (test opt-in)

`ALERTBRIDGE_MCP_EVENT_EGRESS_FILE` selects a private JSON file with `address`
(public literal IP and port), `server_name`, `ca_file`, `cert_file`, `key_file`, and explicit `destinations` (exact host:443 list).
Mount certificates/config read-only; config and key must have no group/other
permissions and be readable by UID 10001. A configured gateway failure never falls
back to direct access. Target DNS/public-IP validation and address binding occur
at the authenticated EgressHub gateway; original-host business TLS, callback
allowlists, signatures, redirect rejection and delivery queue remain enforced.
Do not enable this setting in production as part of test deployment. Keep the
original test image, Compose files, environment and data/key volumes for rollback.

MCP Events测试的自动证书轮换使用专用凭据目录只读挂载：证书/key路径指向同一个
`current`代际目录，适配器每条新连接先解析该目录再加载配对文件。禁止用单文件bind
mount期待原子替换后自动更新inode。当前轮换仅测试实例启用，不修改生产配置或镜像。


### 公共 SDK 接入与生产迁移（2026-10-19）

生产已获明确授权启用 MCP Events，只有 OpenAI 事件回调走 EgressHub，原通知渠道不变。
回调适配器导入 `egresshub/client`，不再维护一份独立 CONNECT/TLS 实现。
`third_party/egresshub` 是私有公共模块的明确源码快照，由相对 Go replace 引用；
`scripts/sync-egresshub-sdk.py /path/to/EgressHub` 同步六个源码/测试文件和逐文件 SHA256 清单。
Dockerfile 先复制该模块再下载依赖，构建不要求本机相邻 EgressHub 仓库。尚未正式发布远端模块或镜像。

配置必须包含 `destinations: ["connectors.api.openai.com:443"]`，且是应用回调白名单的子集。
配置、私钥600，UID10001可读；只读挂载整个凭据目录，使用同一current代际路径。
凭据挂载根目录须允许容器UID遍历（755），代际目录700且归属UID10001；不要因宿主umask留下root700挂载根。

生产目录 `/www/wwwroot/notify.tedxiong.com` 新增 `compose.egress.yaml`，.env 的绝对 COMPOSE_FILE 同时选择基础和egress overlay。
保留原OIDC、OAuth、base路由、Secret和卷；单独只用基础compose重建会丢失Events开关和挂载。
部署候选为 `alertbridge:egress-sdk-candidate-20261019`（基于实际v0.5.1生产镜像，仅替换已验证二进制）。
生产独立身份 `alertbridge-production`，凭据 `/etc/egresshub-agent-production/credentials`；
`egresshub-cert-client-production.timer` 每日UTC03:50加0–10分钟执行，失败有独立OnFailure提醒。
续期使用已有30天阈值/90天叶证书/24小时重叠/失败回滚方案；本次新生产证书未强制提前轮换。

完整test/vet/release/Docker E2E和mcpapi race通过；测试迁移后新事件mock投递200，SDK双TLS通过。
生产实际UID10001和网络中SDK双TLS通过；健康、OAuth discovery及迁移前后两条普通通知sent200/attempts1通过。
原客户端/渠道/路由记录、密钥和其他容器保持。生产订阅0，未创建生产自动回复任务；不将TLS成功当作Dots执行成功。
首次生产挂载权限被umask收紧，修复后恢复，约54秒后确认healthy；期间存在短暂不可用，未覆盖恢复数据库或丢弃队列。

受限配对备份 `/www/backups/alertbridge-egress-sdk-20261019`，SQLite完整性ok。
紧急回滚 `sudo python3 /www/backups/alertbridge-egress-sdk-20261019/rollback.py`：恢复原Compose/.env及精确旧镜像、归档overlay，保留在线数据与key；
有新生产Events订阅时先停用它们，避免旧版无法处理内部回调。脚本已语法验证，未再次切换生产做演练；回滚也有短暂重建窗口。
测试回滚位于 `/www/wwwroot/test.tedxiong.com/app/.private/sdk-migration-20261019/rollback.py`。

## v0.6.0 渠道编辑与现有生产基线正式发布

v0.6.0 增加后台渠道编辑，固定 ID 与类型，非敏感配置回填，URL 和密钥留空保留。保存立即更新运行快照，不需要额外数据库迁移。此版本同时正式收录已经部署的 MCP 2.0 / Events / EgressHub 源码；旧正式版升级须注意 MCP 仅支持 `2026-07-28`，可选 Events 默认关闭。

现有候选版部署升级时，保留 OIDC 和 EgressHub overlay、Secret 与挂载，只将最终生效的 image 改为 ACR 正式镜像，去掉候选版的 `pull_policy: never`。不能只改 `.env` 标签而忽略 overlay 中的硬编码镜像。上线前校验完整 Compose、备份 SQLite 与主密钥，记录旧镜像 ID；回退只恢复原配置并重建 AlertBridge，保留在线业务卷。
