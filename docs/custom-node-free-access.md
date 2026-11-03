# 节点 Free 准入与邮箱白名单

关联需求：https://github.com/ai-workspace-xstream/xconnect-edge-agent/issues/141

## 准入规则

1. **套餐准入照旧**：Plus/Pro 用户按节点等级门槛访问，不改变套餐、配额或团队隔离。
2. **`allowedEmails` 是独立白名单**：公开 shared/free/plus/pro 节点中，邮箱匹配者额外获得该节点访问权，可越过套餐门槛，不依赖 `customAllowFree`。默认空列表；省略或空列表不增加任何用户。
3. **`customAllowFree` 不扩大到所有 Free 用户**：默认为 `false`，必须结合非空 `allowedEmails` 才能产生额外 Free 准入，只作用于名单范围。名单为空时，即使为 `true`，也不能额外放行 Free 用户。公开节点的邮箱白名单独立生效，因此关闭此开关不会撤销名单本身授予的访问权。
4. **`custom` 节点**必须配置非空 `allowedEmails`，Plus/Pro 用户也必须命中名单；名单中的 Free 用户还需 `customAllowFree: true`。
5. **现有限制始终执行**：账号停用、流量暂停、黑名单、有效期、配额和团队隔离继续生效，不升级用户套餐，不修改 SQL schema。team 节点继续按团队成员关系授权，不接受邮箱扩权配置。

名单为空不影响套餐原本允许的用户。例如 free/shared 节点原本可接纳的 Free 用户仍按原套餐规则访问。

## Plus 节点行为对照

下表只列出额外获得访问权的 Free 用户；原本符合套餐门槛的用户始终按既有规则访问。

| `customAllowFree` | `allowedEmails` | 额外获得访问权的 Free 用户 |
| --- | --- | --- |
| 省略或 `false` | 省略或 `[]` | 无 |
| `true` | `[]` | 无 |
| 省略或 `false` | `[alice@example.com]` | 仅 Alice |
| `true` | `[alice@example.com]` | 仅 Alice |

custom 节点中，相同名单在开关关闭时不放行 Free，开启后仅放行名单中的 Free 用户。

## 配置示例

公开 Plus 节点默认不增加白名单用户：

```yaml
agent:
  resourceClass: plus
  teamId: ""
  customAllowFree: false
  allowedEmails: []
```

白名单可独立生效，以下配置允许 Alice、Bob 额外访问此 Plus 节点，包括他们使用 Free 套餐时：

```yaml
agent:
  resourceClass: plus
  teamId: ""
  customAllowFree: false
  allowedEmails:
    - alice@example.com
    - bob@example.com
```

将上例的 `customAllowFree` 改为 `true`，也不会增加名单之外的 Free 用户。

custom 专属节点仅对名单开放，开启开关后允许名单中的 Free 用户：

```yaml
agent:
  resourceClass: custom
  teamId: ""
  customAllowFree: true
  allowedEmails:
    - alice@example.com
```

`AGENT_CUSTOM_EMAILS` 对应 YAML 的 `allowedEmails`，支持单个邮箱或 JSON 数组。YAML 中 `[]` 表示没有补充名单；`[""]` 是无效地址，不能用来表示空名单。邮箱去空白、忽略大小写，不允许通配符。环境变量优先级与现有安装器一致。

## 同步与发布

Accounts 用户快照、事件流、区域和节点列表、订阅导出必须采用相同的准入规则。Agent 校验服务端 scope 和名单摘要回执。名单只增加所配置节点的资格，不授予额外额度或免流，也不会改变传输路径。

先发布配套 Accounts，再更新 Agent。修改配置后完成受控同步，核对 Xray 实际客户端名单及名单内外用户的节点列表，不能仅凭服务 active 或同步数量确认准入正确。现有线上版本是否满足本规则，需单独进行运行验证。

撤销公开节点的补充授权应移除对应邮箱并确认同步；仅关闭 `customAllowFree` 不会撤销独立白名单。custom 节点可关闭开关撤销名单中 Free 用户的资格。回退前先完成资格撤销，旧连接终止遵循既有会话撤销能力。

公开节点的并集协议、权限和发布顺序详见 [公开节点邮箱授权](node-email-access.md)。
