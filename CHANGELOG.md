# Changelog

## Unreleased — v1.0 candidate

本轮是开源体验里程碑候选，尚未发布 v1.0 镜像。运行服务、v1 API、SQLite 与凭据格式不变，无数据库迁移。

- 新增独立静态官网、交互模拟 Demo、手动 GitHub Pages 发布流程和宝塔静态部署指引。
- 明确产品价值、适用边界、英文快速介绍与贡献/安全规范。
- 新增 Python 完整事件调用示例、Dots 使用场景和验收步骤。
- 保持生产容器、卷、路由、凭据和 Nginx 配置不变。

## v0.4.2

- MCP `list_alerts` 支持 `order=desc` 直接读取最近告警；默认正序和历史游标保持兼容。

## v0.4.1

- 修复 Dots OAuth 授权页 CSP 拦截配置回调的问题。

## v0.4.0

- 新增按路由授权的只读 MCP 工具和 Dots OAuth + PKCE。

更早版本见 [GitHub Releases](https://github.com/xiongwei-git/alertbridge/releases)。
