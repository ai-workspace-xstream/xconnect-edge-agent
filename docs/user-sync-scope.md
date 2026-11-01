# 节点用户同步范围

Accounts 根据已注册 Agent 凭据和数据库中的有效订阅、团队成员关系决定用户集合。节点属性由 `AGENT_RESOURCE_CLASS` 明确指定；`AGENT_POOL` 仅标识逻辑池，不参与类型选择。控制面验证节点管理权限后应用该属性，不能按机器品牌、域名或用户数推断套餐。

| agent.resourceClass | 下发集合 |
| --- | --- |
| shared | Free + Plus + Pro，排除团队用户及黑名单 |
| free | Free + Plus + Pro（未分组个人用户按 Free），排除黑名单 |
| plus | 有效 Plus + Pro 用户，排除黑名单 |
| pro | 仅有效 Pro 用户，排除黑名单 |
| custom | 邮箱名单内的有效 Plus/Pro 个人用户，继续校验订阅、额度、黑名单；不包含团队用户 |
| team | agent.teamId 指定团队的管理员和成员，排除其他团队、公共用户及黑名单 |

多个公共节点按 free/plus 配置资源池。节点故障不能把 free 自动提升到 plus，也不能取消团队隔离。shared 不包含团队成员。套餐额度、限速仍由 Accounts 和 Agent/Xray 的既有计费链路执行，本参数不授予额外额度。

```yaml
agent:
  resourceClass: team
  teamId: tenant-id-from-accounts
```

安装脚本支持 `AGENT_RESOURCE_CLASS`、`AGENT_TEAM_ID`；进程环境变量同名，可覆盖 YAML。Ansible 参数为 `agent_resource_class`、`agent_team_id`。从 team 切换到公共类型时须同时清空 teamId。未配置类型仅用于兼容原有部署，不能据此声称具备套餐隔离。

## 控制端契约与上线顺序

先部署配套 Accounts 修复，再升级 Agent。Agent 用 `X-Agent-Resource-Class`、`X-Agent-Team-ID` 传递显式属性；Accounts 验证凭据权限后选择完整用户集合。具备 `agent:configure-public` 的凭据可为自己绑定的节点选择 `shared/free/plus/pro/custom`；内部通配部署凭据（ID 为 `*`、含 `internal` 且授权为 shared）可管理公共节点别名。类型变更覆盖旧公共池登记，状态心跳同步更新库存和数据库。固定为 Free/Plus 的受限凭据不能自行扩大范围。公共管理凭据不能创建或覆盖团队节点；Team 仍要求凭据、团队 ID 和节点身份一致。HTTP、SSE 和状态上报采用同一范围，修订包含 scope，即使用户集合为空也可区分不同类型。

`GET /api/agent-server/v1/users` 返回完整快照：

```json
{"clients":[],"total":0,"revision":"scope-and-users-revision","scope":{"resourceClass":"team","teamId":"tenant-id-from-accounts"}}
```

然后启用 Agent 类型。显式配置类型后，响应 scope 缺失、类型不符或团队不符会拒绝整份快照，不更新同步状态、配置文件或 Xray 用户；保留上次成功配置。合法的空集合必须被应用，以撤销已移除的用户。控制端应把范围纳入 revision，确保成员和范围变更触发同步。状态上报的 resourceClass/teamId 仅是遥测。

本仓库不实现 Accounts 的订阅查询，也不配置负载均衡。单域名随机分发到互斥用户池不会保证认证成功；入口必须另有确定性的池选择和 XHTTP 会话保持机制。

## 配置与诊断

```bash
export AGENT_RESOURCE_CLASS="shared" # shared / free / plus / pro / custom / team
export AGENT_POOL="jp-main"          # 任意逻辑池名，不决定用户范围
```

普通安装会把显式设置的类型写入已有 `/etc/agent/account-agent.yaml`。只在当前 Shell 中 export 再执行 `systemctl restart` 不会让 systemd 服务继承该变量；应通过安装脚本落盘或配置服务环境。`--upgrade-only` 保留未显式覆盖的配置；显式设置的节点属性仍会写入 YAML。Team 切回公共类型时同时清空团队 ID。

启动日志展示实际 resourceClass、teamId 和 pool。若控制面响应范围不匹配，错误会显示 expected/received 类型，但不会打印 token；仍拒绝应用该快照，不通过忽略 scope 修复。控制面未更新或缺少公共节点管理权限时，Agent 单独升级无法补全 Shared 用户集合。

## Team 节点登记与管理员分配

`AGENT_RESOURCE_CLASS=team` 支持不填写 `AGENT_TEAM_ID`。该字段填写时仅作为建议归属；使用共享部署凭据的 Team 节点先进入管理端待分配清单，管理员分配团队并批准后才能获取该团队用户。

`enrollmentStatus=pending/rejected/revoked` 的用户快照必须为空，Agent 仍应用空快照以撤销旧用户；`approved` 的 `scope.teamId` 是控制端最终分配结果，优先于节点建议值。缺失审批字段时保留原有独立凭据的严格范围校验。

重新分配先撤销并等待空用户配置应用确认。传输路径保持 `/split`，不受类型或团队影响。该审批功能如涉及数据库迁移，应按对应发布流程单独执行；不要将其迁移步骤套用到无 schema 变更的 custom 范围更新。先 SIT/UAT 验证。

## 用户专属节点

在 `xconnect-edge-agent.service` 的 `[Service]` 中可设置：

```ini
Environment='AGENT_RESOURCE_CLASS=custom'
Environment='AGENT_CUSTOM_EMAILS=["alice@example.com","bob@example.com"]'
```

`custom` 为用户专属类型，`AGENT_CUSTOM_EMAILS` 支持单个邮箱或非空 JSON 邮箱数组，且为必填项，也可配置 YAML `agent.allowedEmails`。未启用 custom 且未配置名单时保持原有行为；custom 缺少名单会启动失败；显式环境变量为空、空数组或包含无效邮箱时启动失败。邮箱匹配忽略大小写及首尾空格。名单仅进一步收紧该节点的用户集合，不授予订阅或流量权益：有效期、Plus/Pro 等级、黑名单及不限流量福利仍由 Accounts 现有规则决定。名单随认证请求和节点状态上报到 Accounts，Accounts 和节点同步均过滤实际 Xray 客户端；邮箱不会返回给普通用户。

先更新 Accounts 再更新 Agent 和 Portal。使用独立的节点入口，避免与公共节点共享同一入口；启用或修改名单后，检查最新心跳与 Xray 成功同步，再验证名单内外账号的节点列表及连接。旧状态和旧 Xray 配置在完成新心跳及同步前可能仍然存在，因此不能将配置变更本身当作隔离完成。切回公共类型时显式设置原 resourceClass，清空 teamId，并移除 allowedEmails；通过安装脚本切换且未提供 AGENT_CUSTOM_EMAILS 时会移除旧名单。成功同步后再确认恢复。

## 从 init.sh 变量渲染并验证

已有初始化脚本可导出这些变量，再调用安装脚本，无需把邮箱写入安装脚本源码：

```bash
export AGENT_RESOURCE_CLASS="custom"
export AGENT_TEAM_ID=""
export AGENT_CUSTOM_EMAILS="alice@example.com"
# 多用户时：export AGENT_CUSTOM_EMAILS='["alice@example.com","bob@example.com"]'
```

安装脚本读取导出的变量，生成 `/etc/agent/account-agent.yaml`：

```yaml
agent:
  teamId: ""
  resourceClass: "custom"
  allowedEmails:
    - "alice@example.com"
```

`AGENT_CUSTOM_EMAILS` 是环境变量名，YAML 字段名为 `allowedEmails`，类型为列表。安装器可能写成 JSON 内联数组，它与 YAML 多行列表等价。systemd 服务重启后直接读取该文件，不依赖运行 init.sh 的 Shell 环境。若服务另配了同名环境变量，则环境变量优先，应核对覆盖关系。

修改 YAML 后需重启 `xconnect-edge-agent.service`，运行中的进程不会自动重新加载该文件。验收应核对实际 ExecStart 配置路径、最新成功同步、XHTTP/TCP 各目标生成的客户端集合，以及名单内外用户的节点列表。日志仍显示原客户端数量时，不能认定新配置已生效；同步失败时可能保留上一成功的 Xray 配置。邮箱、UUID、访问令牌不应出现在公开验收记录中。

custom 范围及安装器持久化分别由 PR #124、#125 引入；验证已有功能的原地部署不需要数据库迁移。其他功能如同时涉及 schema 变更，应单独按其发布流程处理。
