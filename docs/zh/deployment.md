# 部署

该仓库同时包含 Go 运行时代理、部署自动化以及边缘集成相关资产。

本页用于统一部署前提、支持的拓扑、运维检查项与回滚注意事项。

## UAT Agent Server 部署

UAT shared 和 team 是两个独立的 Agent Server target，分别使用专用 Ansible
inventory；它们不依赖 Doco-CD，也不读取 Web SaaS 的部署配置。

| edition | GCP VM | labels |
| :--- | :--- | :--- |
| shared | `agent-server-shared-uat` | `app=agent-server`, `edition=shared` |
| team | `agent-server-team-uat` | `app=agent-server`, `edition=team` |

部署顺序如下：

1. 先在目标节点完成 `scripts/setup-proxy.sh`，准备 Xray、Caddy、证书和必要配置。
2. 从 `deploy/ansible` 分别执行 `./deploy.sh --uat-shared --deploy-only` 或
   `./deploy.sh --uat-team --deploy-only`。
3. 通过受控运行时注入 `INTERNAL_SERVICE_TOKEN`；不得写入 inventory、vars、命令行
   或 Git。UAT target 不执行 Cloudflare DNS 更新。
4. 验证 `xconnect-edge-agent` 为 active，并使用 accounts Agent API 做一次认证请求。

Ansible inventory 必须只包含对应的单台 VM。缺少节点域名、证书或 UAT token 时，停止
在前置检查，不得猜测域名、复用生产凭据，或使用 standalone 模式替代托管 Agent 部署。

## 与当前代码对齐的说明

- 文档目标仓库: `xconnect-edge-agent`
- 仓库类型: `hybrid-agent`
- 构建与运行依据: go.mod (`xconnect-edge-agent`)
- 主要实现与运维目录: `cmd/`, `internal/`, `agent/`, `deploy/`, `scripts/`, `example/`, `config/`
- `package.json` 脚本快照: `deploy`

## 需要继续归并的现有文档

- `mcp-ssh-manager-setup.md`

## 本页下一步应补充的内容

- 先描述当前已落地实现，再补充未来规划，避免只写愿景不写现状。
- 术语需要与仓库根 README、构建清单和实际目录保持一致。
- 将上方列出的历史 runbook、spec、子系统说明逐步链接并归并到本页。
- 每次发布前，依据当前脚本、清单、CI/CD 流程和环境契约重新核对部署步骤。
