# ADR-001: 使用 Go、SQLite 与单进程持久 Worker

## Status

Accepted

## Date

2026-07-15

## Context

AlertBridge 需要部署到个人宝塔 VPS，首要约束是低资源、稳定恢复、默认安全和简单运维。接收与发送必须解耦；在进程崩溃或容器重启后，已接收的告警不能只存在于内存中。第一版流量低，不需要多实例水平扩展。

## Decision

采用 Go 单二进制，标准库 HTTP 服务，`modernc.org/sqlite` 纯 Go 驱动，以及同一进程内的单投递 Worker。

SQLite 使用 WAL、`synchronous=FULL`、外键、5 秒 busy timeout 和单连接串行访问。事件与投递任务在同一事务中写入；Worker 通过带租约的状态转换领取任务，崩溃后可重新领取。运行容器使用 `scratch`、非 root UID 和只读根文件系统，HTTPS 终止于宝塔 Nginx。

已完成与已死信事件默认保留 30 天后清理，待投递和重试中的任务不受保留期清理影响，避免数据库无限增长。

## Alternatives Considered

- FastAPI + Apprise + SQLite：渠道覆盖广、迭代快，但首版需要 Python 运行时和更多依赖；路由、认证、状态与可靠队列仍需自行实现。保留 Apprise 的适配器思路，不在核心路径引入。
- Go + Redis：队列能力成熟，但增加一个服务、额外持久化和备份面；个人规模收益不足。
- PostgreSQL + 独立 Worker：适合多实例和高并发，但明显超出当前容量和运维需求。
- Rust + SQLite：可以获得更细的资源控制，但在当前负载下不足以抵消开发和维护复杂度。
- Caddy 容器：自动 TLS 很方便，但宝塔已经管理域名、证书和反代；重复 TLS 层增加端口和运维面。

## Consequences

- 首版部署只有一个业务容器和一个数据卷，资源与故障面较小。
- 单 Worker 有意限制飞书并发，天然低于机器人每秒限制；高吞吐需要后续按渠道实现限速并增加 Worker。
- 单 SQLite 文件不支持无协调的多实例写入。需要高可用时，应迁移到 PostgreSQL/Redis，并保持 v1 HTTP 契约。
- `synchronous=FULL` 牺牲少量写入吞吐，换取断电场景下更强的持久性。
- 宝塔必须只反代回环端口；容器自身不负责 TLS。
