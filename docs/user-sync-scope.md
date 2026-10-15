# 节点用户同步范围

Accounts 根据已注册 Agent 凭据和数据库中的有效订阅、团队成员关系决定用户集合。节点参数是期望范围，不能作为授权依据；不能按机器品牌、域名或用户数推断套餐。

| agent.resourceClass | 下发集合 |
| --- | --- |
| shared | 个人 free 和 plus；适合区域仅一台公共节点 |
| free | 有效 free 用户 |
| plus | 有效 plus 用户 |
| team | agent.teamId 对应的有效团队成员 |

多个公共节点按 free/plus 配置资源池。节点故障不能把 free 自动提升到 plus，也不能取消团队隔离。shared 不包含团队成员。套餐额度、限速仍由 Accounts 和 Agent/Xray 的既有计费链路执行，本参数不授予额外额度。

```yaml
agent:
  resourceClass: team
  teamId: tenant-id-from-accounts
```

安装脚本支持 `AGENT_RESOURCE_CLASS`、`AGENT_TEAM_ID`；进程环境变量同名，可覆盖 YAML。Ansible 参数为 `agent_resource_class`、`agent_team_id`。从 team 切换到公共类型时须同时清空 teamId。未配置类型仅用于兼容原有部署，不能据此声称具备套餐隔离。

## 控制端契约与上线顺序

先部署 Accounts：用已认证且绑定节点的凭据解析资源范围，按有效套餐和团队归属过滤；请求头 `X-Agent-Resource-Class`、`X-Agent-Team-ID` 仅用于核对，不能由请求自行授权。HTTP 与 SSE 修订通知须采用同一授权集合。

`GET /api/agent-server/v1/users` 返回完整快照：

```json
{"clients":[],"total":0,"revision":"scope-and-users-revision","scope":{"resourceClass":"team","teamId":"tenant-id-from-accounts"}}
```

然后启用 Agent 类型。显式配置类型后，响应 scope 缺失、类型不符或团队不符会拒绝整份快照，不更新同步状态、配置文件或 Xray 用户；保留上次成功配置。合法的空集合必须被应用，以撤销已移除的用户。控制端应把范围纳入 revision，确保成员和范围变更触发同步。状态上报的 resourceClass/teamId 仅是遥测。

本仓库不实现 Accounts 的订阅查询，也不配置负载均衡。单域名随机分发到互斥用户池不会保证认证成功；入口必须另有确定性的池选择和 XHTTP 会话保持机制。
