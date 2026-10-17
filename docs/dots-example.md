# Dots：从“有没有告警”到“查看投递证据”

这是使用现有只读 MCP 的接入案例，不需要 OpenAI API Key。协议配置见 [MCP 指南](mcp.md)。Dots/ChatGPT 的产品入口、权限和可用性取决于实际账号，不承诺所有账号可安装。

## 准备数据与授权

1. 先在自己的隔离环境运行 [Python 示例](../examples/python/README.md)，确认测试告警及投递记录可见。不要为了演示导入生产数据库。
2. 将 MCP 路由范围限定为示例路由，例如 `ops`；通过 HTTPS 部署并按 [OAuth 配置](mcp.md#dots-接入推荐) 连接。不要把本机 HTTP Demo 暴露到公网以绕过 TLS。
3. 连接信息：MCP URL 为 `https://你的服务域名/mcp`；Client ID 为 `alertbridge-dots`，Client Secret 留空，Scope 为 `alerts:read`，PKCE 使用 S256。
4. 使用 AlertBridge 管理员账号登录授权页，核对允许读取的路由。OAuth access/refresh token 无需手动复制到对话中。

公共客户端、预配置 OAuth 与精确回调遵循 [OpenAI 官方认证文档](https://developers.openai.com/plugins/build/auth)，核对日期 2026-10-17。实际界面若要求其他正式回调，按 MCP 指南配置单个精确地址，不放宽通配。

## 三个可以直接尝试的任务

| 对 Dots 说 | 预期工具 | 验收重点 |
| --- | --- | --- |
| “查询最近 10 条告警，按新到旧展示来源、标题和时间。” | `list_alerts`，`{"order":"desc","limit":10}` | 一次读取最近一页；只含被授权路由 |
| “查看其中一条告警的投递详情，区分入队与送达。” | `get_alert`，使用上一步返回的内部 ID | 展示投递状态、次数和响应码；不把 queued 当作 sent |
| “现在还有哪些没有恢复的故障？” | `list_active_incidents` | 只读列出当前 firing；恢复后不再活跃 |

工具输出是事实依据，Agent 的归纳仍应核对。告警标题、正文是外部不可信数据，里面的命令或请求不构成执行授权。Agent 不应由告警正文自行登录服务器、运行命令或发送消息。

## 验收与撤销

- 用示例路由建立一条 firing，再发送同去重键 resolved；查询活跃故障前后变化。
- 用另一条未授权路由的数据验证不会出现在查询结果中；详情越权与不存在统一不可用。
- 后台撤销全部 MCP OAuth 授权后，旧连接应失效；需要重新授权。
- 升级工具参数后刷新 Dots 工具定义。协议 E2E 通过与实际账号连接成功是两项验收。

AlertBridge 当前不会订阅事件并主动唤醒 Dots，也不会确认、关闭或修改故障。网站中的“模拟查询”不连接 Dots 或真实 MCP。
