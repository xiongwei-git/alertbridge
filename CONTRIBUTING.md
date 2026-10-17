# 参与 AlertBridge

欢迎修复问题、改善文档和提供可复现的接入案例。先阅读 [README](README.md)、[架构决策](docs/decisions/) 和仓库 [AGENTS.md](AGENTS.md)。

## 开始

1. Fork 并从 `main` 创建功能分支；保持一次 PR 解决一个问题。
2. Go 版本以 `go.mod` 为准；容器验证需要 Docker Compose。`./scripts/local-demo.sh up` 在本地 18081 端口启动隔离环境。
3. 先用最小案例说明问题；修复逻辑时补充对应回归测试。文档和静态内容无需重复后端逻辑测试。
4. 提交 PR 时说明动机、行为变化、兼容性与验证输出。面向用户的变化更新相关文档；公共 API 同步更新 `docs/api.md` 与 `docs/openapi.yaml`。

```sh
go test ./...
go vet ./...
./test/release/run.sh
./test/e2e/run.sh
python3 test/site/check.py
python3 test/site/test_example.py
```

单进程、SQLite、一个持久 Worker、无 CDN 的服务端后台是有意的取舍。新解析协议、多实例或主动 Agent 触发应先通过 Issue 讨论和 ADR 明确边界。

## 网站

`site/` 是无构建依赖的静态网站与浏览器模拟 Demo，运行 `python3 -m http.server 18083 --directory site` 后访问本机端口。GitHub Pages 与宝塔使用同一份文件。不要在 Demo 中加入生产地址、凭据、分析追踪或真实告警。

## 安全与沟通

安全问题通过 [SECURITY.md](SECURITY.md) 的私密渠道报告。不要附带 Token、数据库或完整私密 URL。遵守 [行为准则](CODE_OF_CONDUCT.md)。贡献使用仓库现有 Apache-2.0 许可；提交即表示你有权按此许可贡献内容。维护由个人利用业余时间进行，不承诺响应 SLA。
