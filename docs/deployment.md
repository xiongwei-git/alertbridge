# AlertBridge Docker 部署与运维指南

本文覆盖单机 Docker Compose、宝塔 Nginx HTTPS 反向代理、上线验收、日常运维、升级回滚、备份恢复和常见故障。AlertBridge 当前是单实例服务；不要让多个容器同时写入同一个 SQLite 数据卷。

## 1. 推荐拓扑

```text
公网客户端
    │ HTTPS :443
    ▼
宝塔 Nginx（TLS、域名、可选限流）
    │ HTTP 127.0.0.1:18080
    ▼
AlertBridge 容器
    │
    ├── SQLite 命名卷
    └── 外部通知渠道（HTTPS / TLS SMTP）
```

AlertBridge 最好部署在独立于关键被监控服务的 VPS 上。否则这台 VPS 故障时，网关和被监控服务可能同时离线。网关自身的存活检测应由外部系统直接检查 `/healthz`，失效通知不能再经过 AlertBridge。

## 2. 前置条件

- Linux VPS，已安装 Docker Engine 和 Docker Compose v2；
- 宝塔面板及 Nginx，或者功能等价的 HTTPS 反向代理；
- 一个解析到该 VPS 的域名和有效 TLS 证书；
- OpenSSL 与 `curl`，用于初始化凭证和验收；
- 服务器时间已通过 NTP 同步，HMAC 请求默认只接受前后 5 分钟时间窗。

项目建议放在：

```text
/www/wwwroot/AlertBridge
```

所有后续命令都从项目根目录执行。

## 3. 初始化

```sh
cd /www/wwwroot/AlertBridge
./scripts/init.sh
```

初始化脚本使用 `umask 077`，创建生产配置、客户端密钥、管理员密码、动态配置主密钥和 `.env`。默认目录权限为 `0750`，配置与 Secret 文件权限为 `0640`。

### 必须保管的文件

| 文件 | 用途 | 丢失后的影响 |
| --- | --- | --- |
| `config/config.json` | 首次启动种子和基础运行参数 | 新环境无法按原配置启动 |
| `secrets/client-monitoring` | 默认客户端 HMAC 密钥 | 对应调用方需要轮换密钥 |
| `secrets/admin-password` | 后台管理员密码 | 需要停机修改配置或重新初始化 |
| `secrets/master-key` | 加密 SQLite 中的动态凭证 | 已保存的客户端和渠道凭证无法解密 |
| `.env` | 容器读取 Secret 的宿主机组 ID | 文件挂载可能因权限失败 |

`master-key` 必须和 SQLite 数据卷配套备份。只恢复数据库、不恢复原主密钥，服务无法读取后台保存的动态配置。

## 4. 配置渠道和路由

初始化后至少完成以下操作：

1. 将完整飞书机器人 Webhook 写入 `secrets/feishu-ops-webhook`；
2. 将飞书机器人“签名校验”密钥写入 `secrets/feishu-ops-signing-secret`；
3. 如果飞书机器人启用了自定义关键词，在 `config/config.json` 的渠道 `keyword` 中填写任一匹配关键词；
4. 检查默认客户端 `monitoring-client` 的 `allowed_routes`；
5. 确认 `routes` 为实际使用的每种严重程度配置了目标渠道。

Secret 文件只保存值本身，不要加引号、变量名或额外说明。可以使用编辑器填写，完成后重新收紧权限：

```sh
chmod 750 config secrets
chmod 640 config/config.json secrets/*
```

飞书中国版默认允许 `open.feishu.cn`，国际版 Lark 应同时修改 Webhook 和 `allowed_hosts`。生产环境会拒绝 HTTP Webhook、出站重定向和不在白名单内的目标主机。

### 配置生效规则

- 第一次启动时，`config/config.json` 中的客户端、渠道和路由会写入 SQLite；
- SQLite 已有动态配置后，JSON 不会覆盖后台修改；
- 首次启动后应优先通过 `/admin/` 修改客户端、渠道、路由和静默窗口；
- JSON 引用的 Secret 文件仍必须保留并满足权限要求，因为每次启动都会先校验基础配置。

## 5. Compose 启动

先检查渲染后的 Compose 配置：

```sh
docker compose config
```

构建并写入当前 Git 版本：

```sh
ALERTBRIDGE_VERSION="$(git rev-parse --short HEAD 2>/dev/null || printf local)" \
  docker compose build --pull
docker compose up -d
```

确认容器和数据库就绪：

```sh
docker compose ps
curl -fsS http://127.0.0.1:18080/healthz
curl -fsS http://127.0.0.1:18080/readyz
```

预期分别返回：

```json
{"status":"ok"}
{"status":"ready"}
```

Compose 默认约束：

- 仅绑定 `127.0.0.1:18080`；
- 非 root UID `10001`；
- 只读根文件系统和 8 MiB `tmpfs`；
- 删除全部 Linux capabilities，启用 `no-new-privileges`；
- 128 MiB 内存、0.5 CPU、128 个进程；
- JSON 日志单文件 10 MiB，最多保留 3 个。

需要修改宿主机端口时，在 `.env` 增加：

```text
ALERTBRIDGE_PORT=18090
```

不要把容器端口改成 `0.0.0.0`，也不要在云安全组或主机防火墙中开放它。

## 6. 宝塔 HTTPS 反向代理

在宝塔“网站”中添加正式域名、申请证书并强制 HTTPS。反向代理目标为：

```text
http://127.0.0.1:18080
```

推荐的 Nginx 位置配置：

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

其中 `Host` 和 `X-Forwarded-Proto` 不能省略：管理后台的接入指南使用它们生成当前服务的完整 HTTPS 地址。应用会严格校验 Host，避免将恶意值写入可复制的 Shell 示例。

同时确认：

- 公网只开放 443，以及按需开放用于证书跳转的 80；
- `config/config.json` 保持 `secure_cookie: true`；
- `/admin/` 只通过 HTTPS 域名访问；
- WAF 和反向代理不改写 JSON 的空格、换行或字段顺序，HMAC 覆盖原始请求字节；
- 可在 Nginx 层对 `/admin/login` 额外实施来源限速，但不要缓存后台和 API 响应。

## 7. 上线验收

按顺序完成：

1. `https://正式域名/healthz` 返回 `{"status":"ok"}`；
2. `https://正式域名/readyz` 返回 `{"status":"ready"}`；
3. 打开 `https://正式域名/admin/`，使用 `admin` 和 `secrets/admin-password` 登录；
4. 在“通知渠道”发送测试消息并确认到达；
5. 检查“路由规则”中每个实际使用的 `routing_key + severity` 都绑定了已启用渠道；
6. 从后台“接入指南”复制签名示例，提交 `status=test` 事件并得到 `202`；
7. 原样重放同一 Nonce，应得到 `401`；
8. 使用新 Nonce 和相同 `event_id`，应得到 `202` 和 `outcome=duplicate`，渠道不重复发送；
9. 创建短时静默，确认事件被记录为 `suppressed/silence`；
10. 检查日志中没有 Webhook、密钥、签名和完整请求正文。

## 8. 日常运维

```sh
# 容器状态
docker compose ps

# 最近日志
docker compose logs --tail=100 alertbridge

# 持续跟踪日志
docker compose logs -f --tail=100 alertbridge

# 应用 Compose 或环境变量变更
docker compose up -d --force-recreate

# 停止并保留数据卷
docker compose down
```

生产环境不要执行：

```sh
docker compose down -v
```

`-v` 会删除名为 `alertbridge_alertbridge-data` 的 SQLite 数据卷。

## 9. 升级与回滚

升级前先完成第 10 节的数据库与密钥备份，并给当前镜像保留回滚标签：

```sh
rollback_tag="alertbridge:rollback-$(date +%Y%m%d-%H%M%S)"
docker image tag alertbridge:local "$rollback_tag"
printf 'Rollback image: %s\n' "$rollback_tag"
```

拉取代码、重新构建并观察健康状态：

```sh
git pull --ff-only
ALERTBRIDGE_VERSION="$(git rev-parse --short HEAD)" docker compose build --pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:18080/readyz
docker compose logs --tail=100 alertbridge
```

如果新容器无法启动，将上一步打印的标签替换到下面命令：

```sh
docker image tag alertbridge:rollback-YYYYMMDD-HHMMSS alertbridge:local
docker compose up -d --no-build --force-recreate
```

若未来版本说明包含不可逆数据库迁移，只回滚镜像可能不够，应同时恢复与旧镜像匹配的数据卷和 `master-key` 备份。

## 10. 备份

SQLite 使用 WAL。个人规模最稳妥的方式是短暂停止服务，再复制整个数据卷。下面命令同时备份数据库、配置和 Secret：

```sh
umask 077
backup_dir="$PWD/backups/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup_dir"

docker compose stop alertbridge
docker run --rm \
  -e HOST_UID="$(id -u)" \
  -e HOST_GID="$(id -g)" \
  -v alertbridge_alertbridge-data:/data:ro \
  -v "$backup_dir:/backup" \
  alpine:3.24 \
  sh -c 'tar -czf /backup/alertbridge-data.tar.gz -C /data . && chown "$HOST_UID:$HOST_GID" /backup/alertbridge-data.tar.gz'

tar -czf "$backup_dir/config-and-secrets.tar.gz" \
  config/config.json .env secrets
docker compose start alertbridge
chmod -R go-rwx "$backup_dir"
printf 'Backup created: %s\n' "$backup_dir"
```

如果备份过程中任何命令失败，也应先执行 `docker compose start alertbridge` 恢复服务。备份目录包含可解密生产凭证的材料，只能放在加密存储中，不要上传 Git、普通网盘或公开对象存储。

## 11. 恢复

以下操作会清空目标数据卷，只能在确认备份文件和目标服务器后执行。先设置需要恢复的备份目录：

```sh
backup_dir="$PWD/backups/YYYYMMDD-HHMMSS"
test -s "$backup_dir/alertbridge-data.tar.gz"
test -s "$backup_dir/config-and-secrets.tar.gz"
```

停止服务并恢复配置：

```sh
docker compose down
tar -xzf "$backup_dir/config-and-secrets.tar.gz" -C "$PWD"
chmod 750 config secrets
chmod 640 config/config.json secrets/*
```

如果恢复到了另一台服务器，将 `.env` 中的 `ALERTBRIDGE_GID` 改为当前 `id -g` 的输出。随后恢复数据卷：

```sh
docker volume create alertbridge_alertbridge-data >/dev/null
docker run --rm \
  -e RUNTIME_GID="$(id -g)" \
  -v alertbridge_alertbridge-data:/data \
  -v "$backup_dir:/backup:ro" \
  alpine:3.24 \
  sh -c 'find /data -mindepth 1 -maxdepth 1 -exec rm -rf {} + && tar -xzf /backup/alertbridge-data.tar.gz -C /data && chown -R "10001:$RUNTIME_GID" /data'

docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:18080/readyz
```

恢复后重新完成渠道测试和签名事件验收，不要只以容器处于 `running` 判断恢复成功。

## 12. 常见故障

| 现象 | 主要检查项 |
| --- | --- |
| 容器启动后立即退出 | `docker compose logs alertbridge`；配置 JSON；Secret 是否为空；权限是否超过 `0640` |
| 日志提示 Secret 权限不安全 | 执行 `chmod 750 config secrets` 和 `chmod 640 config/config.json secrets/*`，确认 `.env` 的 `ALERTBRIDGE_GID` 等于宿主机文件组 ID |
| 后台登录后又回到登录页 | 必须通过 HTTPS 访问；确认 Nginx 传递 `Host` 和 `X-Forwarded-Proto`；生产环境保持 `secure_cookie: true` |
| `/readyz` 返回 503 | 检查数据卷权限、磁盘空间和 SQLite 日志；`/healthz` 正常只代表进程仍存活 |
| API 返回 401 | 检查 Client ID、服务器时间、Nonce 是否重复，以及签名后正文是否被再次序列化或改写 |
| API 返回 `422 route_unavailable` | 当前 `routing_key + severity` 没有绑定已启用渠道；在后台补充路由矩阵 |
| API 返回 `403 route_forbidden` | 客户端的允许路由中不包含正文提交的 `routing_key` |
| 飞书返回关键词错误 `19024` | 后台渠道中的“安全关键词”必须命中飞书机器人配置的任一关键词 |
| 渠道持续重试或进入死信 | 检查 VPS 出站 DNS/HTTPS、目标主机白名单、Token/密码和第三方服务响应 |
| 18080 端口被占用 | 在 `.env` 设置新的 `ALERTBRIDGE_PORT`，再执行 `docker compose up -d --force-recreate` |

不要通过关闭 HMAC、放宽 Secret 权限、启用明文 SMTP 或把容器端口暴露公网来绕过故障。先根据日志和结构化错误码定位边界。
