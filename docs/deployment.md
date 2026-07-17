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
  https://raw.githubusercontent.com/xiongwei-git/alertbridge/v0.3.0/compose.yaml
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
  'ALERTBRIDGE_IMAGE_TAG=v0.3.0' \
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

`ALERTBRIDGE_IMAGE` 只改变镜像仓库，升级与回滚仍然只修改 `ALERTBRIDGE_IMAGE_TAG`。ACR 是国内部署镜像，GHCR 仍是官方版本和发布来源。

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

## 7. 首次后台配置

打开：

```text
https://你的域名/admin/
```

使用初始化时显示的账号密码登录，然后：

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

宝塔变量只填充标题、正文和可选分类。来源、路由、级别由令牌在服务端固定，状态始终是 `info`，因此不会产生无法关闭的活跃故障。令牌不要拼进 URL；泄露时在后台停用或轮换即可。需要故障发生/恢复闭环时仍使用 HMAC 完整事件接口。

上述字段来自[宝塔自定义消息通道指南](https://www.bt.cn/bbs/thread-143326-1-1.html)中的 `$title`、`$msg`、`$type`。原帖界面以 Linux 面板 9.5.0 为例，其他版本的菜单名称可能不同，但请求 URL、请求头和自定义正文的配置原则相同。

## 8. 上线验收

1. HTTPS 域名的 `/healthz` 和 `/readyz` 正常；
2. 管理员可以登录，Cookie 通过 HTTPS 正常保存；
3. 每个渠道的后台测试消息到达；
4. 每个 HMAC 客户端至少允许一个已配置路由，每个轻量令牌固定一条已配置规则；
5. 轻量测试通知和 HMAC 签名测试事件都得到 `202 Accepted`；
6. 原样重放同一 HMAC Nonce 得到 `401`；
7. 新 Nonce 配相同 `event_id` 得到 `outcome=duplicate`，渠道不重复发送；
8. 日志中没有 Webhook、Token、密码、签名或完整请求正文。

## 9. 日常运维

```sh
docker compose ps
docker compose logs --tail=100 alertbridge
docker compose logs -f --tail=100 alertbridge
docker compose up -d
docker compose down  # 停止并保留两个数据卷
```

不要在生产环境执行 `docker compose down -v`。它会删除 `alertbridge_alertbridge-data` 和 `alertbridge_alertbridge-secrets`。

## 10. 升级与回滚

升级前先执行第 11 节备份。记录当前版本后编辑 `.env`：

```sh
grep '^ALERTBRIDGE_IMAGE_TAG=' .env
```

把 `ALERTBRIDGE_IMAGE_TAG` 改为已发布的新版本，然后：

```sh
docker compose pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:18080/readyz
docker compose logs --tail=100 alertbridge
```

回滚时把镜像标签改回旧版本，重新执行以上命令。若 Release 说明包含不可逆数据库迁移，还必须恢复与旧镜像匹配的两个卷，不能只回滚镜像。

## 11. 备份

数据库卷和密钥卷是不可拆分的恢复对。短暂停止服务后同时打包：

```sh
cd /www/wwwroot/alertbridge
umask 077
backup_dir="$PWD/backups/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup_dir"

docker compose stop alertbridge
docker run --rm \
  -e HOST_UID="$(id -u)" \
  -e HOST_GID="$(id -g)" \
  -v alertbridge_alertbridge-data:/data:ro \
  -v alertbridge_alertbridge-secrets:/secrets:ro \
  -v "$backup_dir:/backup" \
  alpine:3.23 \
  sh -c 'tar -czf /backup/alertbridge-data.tar.gz -C /data . &&
         tar -czf /backup/alertbridge-secrets.tar.gz -C /secrets . &&
         chown "$HOST_UID:$HOST_GID" /backup/*.tar.gz'

mkdir -p "$backup_dir/secrets"
cp compose.yaml .env "$backup_dir/"
cp secrets/admin_password "$backup_dir/secrets/"
chmod -R go-rwx "$backup_dir"
docker compose start alertbridge
printf 'Backup created: %s\n' "$backup_dir"
```

备份同时包含密码哈希和可解密动态凭据的主密钥，只能进入加密存储，不要上传 Git、公开对象存储或普通共享网盘。

## 12. 恢复

以下操作会替换目标数据卷：

```sh
backup_dir="$PWD/backups/YYYYMMDD-HHMMSS"
test -s "$backup_dir/alertbridge-data.tar.gz"
test -s "$backup_dir/alertbridge-secrets.tar.gz"

docker compose down
docker volume rm alertbridge_alertbridge-data alertbridge_alertbridge-secrets 2>/dev/null || true
docker volume create alertbridge_alertbridge-data >/dev/null
docker volume create alertbridge_alertbridge-secrets >/dev/null

docker run --rm \
  -v alertbridge_alertbridge-data:/data \
  -v alertbridge_alertbridge-secrets:/secrets \
  -v "$backup_dir:/backup:ro" \
  alpine:3.23 \
  sh -c 'tar -xzf /backup/alertbridge-data.tar.gz -C /data &&
         tar -xzf /backup/alertbridge-secrets.tar.gz -C /secrets &&
         chown -R 10001:0 /data /secrets && chmod 700 /data /secrets && chmod 600 /secrets/master.key'

docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:18080/readyz
```

恢复后重新验证管理员登录、渠道测试和签名事件。只看到容器 `running` 不等于恢复完整。

## 13. 常见故障

| 现象 | 主要检查项 |
| --- | --- |
| 首次启动提示找不到管理员密码 | `secrets/admin_password` 是否存在且至少 16 字节；父目录是否为 `0700`、文件是否为 `0644`；`docker compose config` 是否通过 |
| `container rootfs is marked read-only` | 使用了 v0.2.0 的环境来源 Secret；下载 v0.2.1 或更高版本的 `compose.yaml`，按第 4 节创建文件型 Secret |
| 修改引导密码文件后登录密码没变化 | 这是安全设计：引导密码只在数据库为空时使用，不会覆盖现有管理员 |
| 主密钥无法创建或读取 | `alertbridge-secrets` 卷是否可写；密钥是否被损坏；不要手工替换正在使用的主密钥 |
| 后台登录后又回到登录页 | 必须通过 HTTPS；检查 Nginx 的 `Host`、`X-Forwarded-Proto` 和浏览器 Secure Cookie |
| `/readyz` 返回 503 | 检查两个卷、磁盘空间、SQLite 日志和容器启动日志 |
| 轻量 API 返回 401 | 检查 `Authorization: Bearer ...` 是否完整、令牌是否启用或已经轮换；不要把令牌放在 URL |
| HMAC API 返回 401 | 检查 Client ID、服务器时间、Nonce 和原始正文签名 |
| API 返回 429 | 对应客户端或轻量令牌已达到当前分钟限额，等待 `Retry-After` 后再试或核对异常调用来源 |
| API 返回 `403 route_forbidden` | 客户端没有允许该 `routing_key` |
| API 返回 `422 route_unavailable` | 对应路由和严重程度没有绑定已启用渠道 |
| “活跃故障”测试后一直为 1 | 测试事件误用了 `status=firing`；发送相同客户端、路由和 `dedupe_key` 的 `resolved` 关闭故障，后续测试使用 `status=test` |
| 飞书返回 `19024` | 后台“安全关键词”必须命中机器人配置的任一关键词 |
| GHCR 拉取失败 | 使用已发布完整版本；确认 VPS 可访问 `ghcr.io`；中国内地阿里云 ECS 可按第 5 节配置维护者提供的 ACR VPC 镜像 |
| ACR 返回未授权 | 重新对 `.env` 中镜像地址的 VPC Registry 执行 `docker login`；确认登录名、固定密码和仓库权限 |
| 18080 被占用 | 修改 `.env` 的 `ALERTBRIDGE_PORT` 后重新创建容器 |
| 通知时间比北京时间少 8 小时 | 使用 v0.2.2 或更高版本；确认 `ALERTBRIDGE_DISPLAY_TIMEZONE=Asia/Shanghai`，不要修改服务器系统时钟 |

不要通过关闭既有认证、把令牌放进 URL、使用固定弱密码、把密码放入容器环境变量、放宽出站地址校验或开放应用端口来绕过故障。
