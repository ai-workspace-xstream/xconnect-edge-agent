# 公开套餐节点的邮箱补充授权

公开节点的准入范围为：**原套餐准入范围 ∪ 指定邮箱**。

```sh
export AGENT_RESOURCE_CLASS="plus"
export AGENT_CUSTOM_EMAILS='["alice@example.com","bob@example.com"]'
```

或者配置 Agent YAML：

```yaml
agent:
  resourceClass: plus
  allowedEmails:
    - alice@example.com
    - bob@example.com
```

Plus/Pro 用户继续按原规则使用该 Plus 节点，即使没有列在邮箱名单中。Alice、Bob 即使是 Free，也额外允许使用此节点；其他 Free 用户仍不允许。邮箱只授予所配置节点的资格，不升级套餐、不增加额度、不提供免流，也不会创建新的节点。

支持公开等级 shared、free、plus、pro；不配置名单保持原行为。环境变量仍接受单个邮箱或 JSON 邮箱数组；去空白、忽略大小写、去重，最多 256 项，不允许通配符和显示名称。YAML allowedEmails 可用空数组撤销补充授权。全局变量遵循既有优先级，修改后按现有部署方式重启 Agent。

强制门槛继续执行：账号启用、现有注册验证政策、流量暂停、黑名单、期限、配额以及团队隔离。team 节点拒绝邮箱扩权配置。custom 节点继续要求名单，并保留 customAllowFree 默认 false 的现有语义；本功能不会改变已部署 custom 节点的访问范围。

需要配套 Accounts 和 Agent。公开名单由有节点配置权限的认证部署凭据确认；受限或团队凭据不能借请求头扩权。协议使用 X-Agent-Email-Policy: public-union-v1 与 X-Agent-Allowed-Emails，Accounts 返回策略版本和规范化名单摘要。Agent 核对回执后使用控制端的并集快照，不对公开节点再取邮箱交集；旧后端忽略该策略或摘要不符时拒绝同步。custom 仍保留本地名单限制。

Accounts 的区域、节点列表、完整配置、用户事件版本与用户下发保持一致。订阅从已有授权后的节点集合导出，传输路径保持原值。Console 继续消费现有接口，不需要改变 Plus 节点的资源等级或给用户标记为 Plus。

发布顺序：先 Accounts，再 Agent。移除邮箱后，受控同步和节点状态更新会撤销 Free 用户的节点资格；旧连接终止遵循既有会话撤销能力，不承诺所有长连接立即断开。回退前先移除公开节点的补充名单，确认同步撤销，再回退版本。
