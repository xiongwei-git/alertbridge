# AlertBridge Docker 部署与运维指南

本文覆盖无仓库 Docker Compose 部署、宝塔 Nginx、首次配置、升级回滚、备份恢复和排障。AlertBridge 是单实例服务，不允许多个容器同时写入同一个 SQLite 数据卷。

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

不需要克隆仓库，不需要 Go、Git、Redis 或 PostgreSQL。

## 3. 创建部署目录

```sh
mkdir -p /www/wwwroot/alertbridge
cd /www/wwwroot/alertbridge
curl -fsSLo compose.yaml \
  https://raw.githubusercontent.com/xiongwei-git/alertbridge/v0.5.0/compose.yaml
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
  'ALERTBRIDGE_IMAGE_TAG=v0.5.0' \
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

### 中国内地服务器使用 ACR

默认镜像源是 GHCR。若维护者提供了同版本 ACR 镜像，中国内地的阿里云 ECS 可以在 `.env` 增加完整的 VPC 镜像路径：

```sh
printf '%s\n' \
  'ALERTBRIDGE_IMAGE=<ACR VPC 域名>/<命名空间>/<仓库名>' >> .env
```

首次使用私有 ACR 仓库时，登录一次对应的 VPC Registry。命令会交互式询问密码，不要把密码直接写在命令参数或 `.env` 中：

```sh
docker login <ACR VPC 域名> -u '<Registry 登录名>'
docker compose config
docker compose pull
docker compose up -d
```

`ALERTBRIDGE_IMAGE` 只改变镜像仓库，升级与回滚仍然只修改 `ALERTBRIDGE_IMAGE_TAG`。ACR 是国内部署镜像，GHCR 仍是官方版本和发布来源。ACR 复制并核验 GHCR 的原始 AMD64/ARM64 平台摘要；因 ACR 不支持新版证明文件格式，来源证明保留在 GHCR，镜像索引摘要可以不同。

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
| 镜像拉取失败 | 已发布完整版本、GHCR 连通性；ACR 另查登录与仓库权限 |
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
  https://raw.githubusercontent.com/xiongwei-git/alertbridge/v0.5.0/compose.oidc.yaml
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
