# 节点用户同步范围

Accounts 根据已注册 Agent 凭据和数据库中的有效订阅、团队成员关系决定用户集合。节点属性由 `AGENT_RESOURCE_CLASS` 明确指定；`AGENT_POOL` 仅标识逻辑池，不参与类型选择。控制面验证节点管理权限后应用该属性，不能按机器品牌、域名或用户数推断套餐。

| agent.resourceClass | 下发集合 |
| --- | --- |
| shared | Free + Plus，排除团队用户及黑名单 |
| free | 仅注册 Free 用户（未分组个人用户也按 Free），排除黑名单 |
| plus | 仅有效 Plus 用户，排除黑名单 |
| team | agent.teamId 指定团队的管理员和成员，排除其他团队、公共用户及黑名单 |

多个公共节点按 free/plus 配置资源池。节点故障不能把 free 自动提升到 plus，也不能取消团队隔离。shared 不包含团队成员。套餐额度、限速仍由 Accounts 和 Agent/Xray 的既有计费链路执行，本参数不授予额外额度。

```yaml
agent:
  resourceClass: team
  teamId: tenant-id-from-accounts
```

安装脚本支持 `AGENT_RESOURCE_CLASS`、`AGENT_TEAM_ID`；进程环境变量同名，可覆盖 YAML。Ansible 参数为 `agent_resource_class`、`agent_team_id`。从 team 切换到公共类型时须同时清空 teamId。未配置类型仅用于兼容原有部署，不能据此声称具备套餐隔离。

## 控制端契约与上线顺序

先部署配套 Accounts 修复，再升级 Agent。Agent 用 `X-Agent-Resource-Class`、`X-Agent-Team-ID` 传递显式属性；Accounts 验证凭据权限后选择完整用户集合。具备 `agent:configure-public` 的凭据可为自己绑定的节点选择 `shared/free/plus`；内部通配部署凭据（ID 为 `*`、含 `internal` 且授权为 shared）可管理公共节点别名。类型变更覆盖旧公共池登记，状态心跳同步更新库存和数据库。固定为 Free/Plus 的受限凭据不能自行扩大范围。公共管理凭据不能创建或覆盖团队节点；Team 仍要求凭据、团队 ID 和节点身份一致。HTTP、SSE 和状态上报采用同一范围，修订包含 scope，即使用户集合为空也可区分不同类型。

`GET /api/agent-server/v1/users` 返回完整快照：

```json
{"clients":[],"total":0,"revision":"scope-and-users-revision","scope":{"resourceClass":"team","teamId":"tenant-id-from-accounts"}}
```

然后启用 Agent 类型。显式配置类型后，响应 scope 缺失、类型不符或团队不符会拒绝整份快照，不更新同步状态、配置文件或 Xray 用户；保留上次成功配置。合法的空集合必须被应用，以撤销已移除的用户。控制端应把范围纳入 revision，确保成员和范围变更触发同步。状态上报的 resourceClass/teamId 仅是遥测。

本仓库不实现 Accounts 的订阅查询，也不配置负载均衡。单域名随机分发到互斥用户池不会保证认证成功；入口必须另有确定性的池选择和 XHTTP 会话保持机制。

## 配置与诊断

```bash
export AGENT_RESOURCE_CLASS="shared" # shared / free / plus / team
export AGENT_POOL="jp-main"          # 任意逻辑池名，不决定用户范围
```

普通安装会把显式设置的类型写入已有 `/etc/agent/account-agent.yaml`。只在当前 Shell 中 export 再执行 `systemctl restart` 不会让 systemd 服务继承该变量；应通过安装脚本落盘或配置服务环境。`--upgrade-only` 保留原有配置。Team 切回公共类型时同时清空团队 ID。

启动日志展示实际 resourceClass、teamId 和 pool。若控制面响应范围不匹配，错误会显示 expected/received 类型，但不会打印 token；仍拒绝应用该快照，不通过忽略 scope 修复。控制面未更新或缺少公共节点管理权限时，Agent 单独升级无法补全 Shared 用户集合。
