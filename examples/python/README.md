# Python HMAC 事件调用示例

Python 3 标准库即可，无额外依赖。签名覆盖实际发送的 UTF-8 原始字节；每次调用生成独立事件 ID、时间戳与 Nonce。脚本会真实提交事件，请优先使用本地 Demo 或专用测试路由。

1. `./scripts/local-demo.sh up`，在本机后台创建并测试通知渠道。
2. 创建 `ops` 路由的 `warning` 级别规则；创建仅允许 `ops` 路由的 HMAC 客户端。
3. 将只显示一次的客户端密钥通过编辑器保存到仓库外的私有文件，执行 `chmod 600 ~/.alertbridge-example-secret`。不要把密钥放在命令参数、版本库或公开截图里。

```sh
export ALERTBRIDGE_URL=http://127.0.0.1:18081
export ALERTBRIDGE_CLIENT_ID=填写客户端ID
export ALERTBRIDGE_ROUTING_KEY=ops
export ALERTBRIDGE_CLIENT_SECRET_FILE=~/.alertbridge-example-secret
python3 examples/python/send_event.py --status test
```

在后台“投递记录”确认结果；HTTP 202 只证明事件被接收处理，不证明渠道送达。需要验收接收效果时，还应在你控制的渠道确认收到消息。

```sh
python3 examples/python/send_event.py --status firing --dedupe-key example-disk
python3 examples/python/send_event.py --status firing --dedupe-key example-disk
python3 examples/python/send_event.py --status resolved --dedupe-key example-disk
```

保持客户端、路由和去重键一致：首次创建故障，第二次去重，最后恢复。去重窗口与路由配置以实际服务为准，勿高频运行。连接生产环境时改用 HTTPS 根地址并显式确认测试范围。脚本拒绝非 loopback 明文 HTTP、带路径/账号的 URL 和 HTTP 重定向。

管理员启用 OIDC 不改变此脚本的 HMAC 凭据或调用方法。不要在脚本里登录管理员后台或使用身份提供方 Token。

只发一次性消息时可选轻量 Bearer API，见 [API 文档](../../docs/api.md)，不要把 HMAC 密钥当作 Bearer 令牌。
