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
# 区域资源池配置

`AGENT_POOL` 是区域内的逻辑池标识；`AGENT_RESOURCE_CLASS` 选择该节点同步的用户范围。支持以下资源类型：

| 模式 | 同步用户 | 日本示例池 | 节点域名 |
| --- | --- | --- | --- |
| `pro` | Pro 订阅用户 | `jp-pro` | 按部署配置 |
| `shared` | Free + Plus + Pro 通用用户 | `jp-shared` | `jp-xconnect.svc.plus` |
| `plus` | Plus 用户，包含 Plus 专属池用户 | `jp-plus` | `jp-plus-xconnect-selfhost-prod-v2-01.svc.plus` |
| `team` | 指定团队的管理员和成员 | `jp-team` | `jp-team-xconnect-selfhost-prod-v2-01.svc.plus` |

`jp-xconnect.svc.plus` 同时是 Shared 通用池的实际节点域名和通用稳定入口，不再为 Shared 池单独配置另一个域名。Plus 用户可使用 Shared 通用池和 Plus 专属池；Free 用户只使用 Shared 通用池。Team 池只服务指定团队，不接收公共用户。

Agent 配置仍支持 `free` resource class，用于 Free 独立节点；当前三池拓扑将 Free 用户放在 `shared` 通用池，因此不需要额外的 Free 专属池。

Shared 节点示例：

```bash
export AGENT_PROXY_DOMAIN="jp-xconnect.svc.plus"
export AGENT_ENTRY_POINT="jp-xconnect.svc.plus"
export AGENT_REGION="jp"
export AGENT_POOL="jp-shared"
export AGENT_RESOURCE_CLASS="shared"
unset AGENT_TEAM_ID
```

Plus 节点将域名和池名分别设为 `jp-plus-xconnect-selfhost-prod-v2-01.svc.plus` 与 `jp-plus`，类型设为 `plus`。Team 节点对应 `jp-team-xconnect-selfhost-prod-v2-01.svc.plus`、`jp-team` 和 `team`，并必须设置实际的 `AGENT_TEAM_ID`。

Team 同步范围由 `AGENT_TEAM_ID` 限定，必须与控制面登记的团队授权一致。节点编号使用 `-01`、`-02`、`-03`，在同一区域、类型和环境内递增。
