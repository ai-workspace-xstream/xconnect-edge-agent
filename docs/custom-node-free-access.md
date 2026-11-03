# 节点 Free 准入与邮箱白名单

`agent.customAllowFree` 默认为 `false`，可与 `shared`、`free`、`plus`、`pro`、`custom` 和 `team` 组合。它只控制节点准入，不改变用户套餐、配额或账号状态。

```yaml
agent:
  resourceClass: plus
  customAllowFree: true
  teamId: ""
  allowedEmails:
    - free-user@example.com
  entryPoint: custom-node.example.com
```

- 未配置或 false：按节点套餐门槛判断；`allowedEmails` 命中者可额外访问该节点。
- `true`：Free 用户也可访问该节点。custom 节点仍必须命中其 `allowedEmails` 白名单。
- `allowedEmails` 对公开套餐节点是额外准入白名单：命中可越过套餐门槛，但只增加该节点访问权，不升级套餐。空字符串、通配符和无效地址均拒绝。
- team 节点仍严格检查团队成员关系；`customAllowFree` 不会让白名单或 Free 准入跨越团队隔离。
- 账号停用、流量暂停、黑名单和配额耗尽仍由 Accounts 判定。
- 新用户是否需要验证邮箱遵循 Accounts 当前注册策略，此开关不改变邮箱验证或账号状态。
- 单节点与未来虚拟节点均按各节点独立配置，不是宿主机全局免流或套餐开关。

需要同时升级 Accounts 与 Agent；先发布 Accounts，再更新 Agent。Agent 通过 `X-Agent-Custom-Allow-Free: true` 声明意图。公开节点仅允许具备 public-pool 管理权限的认证部署凭据启用；team 节点仅允许同一已登记节点和团队范围内的凭据启用。默认关闭不会改变现有节点授权。

Accounts 在用户快照 scope 中返回 `customAllowFree`，Agent 校验回执，旧后端不识别时拒绝同步而非静默宣称已启用。用户事件流和用户快照使用相同过滤规则；节点列表/区域、订阅使用服务端认可的 scope 和邮箱名单，配置开关不会改变传输路径。

修改 `/etc/agent/account-agent.yaml` 后按现有部署方式受控重启 Agent；它会同步其 Xray 目标。检查两个传输后端的配置与服务健康，不能仅凭 Agent active 判断用户可连接。无需修改既有 init.sh；以后重新执行安装脚本可能重新渲染 YAML，运维应通过配置所有者保留此选项。

回退时先置 false 并重启新版 Agent，确认 Free 用户从 Xray 和节点列表撤销，再按需回退二进制；回退 Accounts 前先关闭节点开关。不删除用户、配置用户套餐或修改 SQL schema。

公开 free/plus/pro/shared 节点的邮箱补充授权采用并集，见 [公开节点邮箱授权](node-email-access.md)。custom 节点仍受其非空名单限制。
