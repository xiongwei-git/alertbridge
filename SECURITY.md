# 安全政策

当前发布版本为 v0.5.0。安全修复优先进入最新发布版本，不承诺向旧版本分支回补。升级前阅读发布说明，并备份数据库与主密钥恢复对；启用 OIDC 时还需保留服务器配置文件和身份提供方的恢复资料。

## 私密报告

请使用仓库的 [Report a vulnerability](https://github.com/xiongwei-git/alertbridge/security/advisories/new) 私密报告入口。若入口不可用，请不要在公开 Issue 中披露漏洞细节或凭据；先用不包含细节的 Issue 请求维护者开启私密报告。响应由维护者尽力提供，不承诺修复时限。

提供受影响版本、最小复现、预期与实际行为、影响范围。使用虚构数据；不提交 `.env`、数据库、Webhook、密钥或密码。请勿对未经授权的部署测试。

## 信任边界

- 公网 TLS 由反向代理提供，应用只绑定 loopback。
- HMAC、轻量通知令牌、管理员 OIDC 和 MCP OAuth/Bearer 使用不同的凭据和授权规则。
- OIDC 仅允许服务器配置的 issuer + subject；登录事务校验 state、nonce 和 PKCE，身份提供方 Token 不持久化。配置文件应放在私有目录中，推荐 UID 10001、0400；不得记录完整 callback 查询参数。
- MCP 仅按服务端路由授权读取，不提供确认、关闭或执行命令。
- 告警正文是不可信内容，不能成为 Agent 执行指令的授权。
- 交互 Demo 完全在浏览器中模拟，不连接网关，也不能证明真实投递。

更多部署约束见 [运维指南](docs/deployment.md)。
