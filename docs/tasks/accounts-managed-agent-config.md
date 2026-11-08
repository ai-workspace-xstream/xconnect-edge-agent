# Accounts 节点配置托管

主文件 /etc/agent/account-agent.yaml 保留 agent.id、agent.controllerUrl、
agent.apiToken 和 agent.tls。Accounts 按节点提供版本化配置。Agent 将通过校验的
快照发布到 /etc/agent/account-agent.d/managed.yaml。非敏感期望配置由 GitOps
Backend 保存，敏感值由 Vault KV 管理。Portal 的 /panel/agent 调用 Accounts
管理接口。沿用现有认证与权限模型，无需 SQL schema 变更。

1. Accounts 定义节点配置读写协议。管理端按角色授权并记录修订与操作者。
   节点读取接口使用绑定到节点的凭据，以服务端身份确定配置归属。
   GitOps 保存非敏感字段及版本，Vault 通过范围明确的引用提供敏感字段。
   凭据值不进入 Git、Portal 回显或日志。
2. Agent 使用固定的文件名读取快照，保留旧 YAML 加载方式。同步端校验节点
   ID、修订、大小、字段和最终配置。主机路径与执行命令使用安装时固定的
   本机配置模板，Accounts 只传受限参数，避免远程配置成为任意命令入口。
   临时文件与目标同目录，设置 0600，校验通过后原子替换。启动与更新失败
   使用最后一个有效版本，并在回执中区分下载和实际应用成功。
3. 将配置更新接入 Agent 生命周期。影响 Xray 的更新先验证 XHTTP 和 TCP
   两个目标，再发布并切换。失败时恢复上一修订，保留失败原因和当前运行
   修订。Portal 显示期望修订、已应用修订和失败状态。
4. 联调历史 YAML、四项引导配置、断网、无效快照、跨节点授权拒绝和
   回退。依次在 JP Free、PH、SG 验证身份、心跳、两个同步目标和会话。
   SG 需要先确认 SSH 可达性。每台切换前备份二进制、systemd 配置和原 YAML。
   回退时恢复原二进制和原 YAML，并移走该轮快照。

当前分支仅实现第 2 步的本地快照启动加载。Accounts 下发、原子写入、
运行时应用及 Portal 管理仍待实现。

## 配置模板与白名单边界

- 非敏感配置按模板复用：`free-shared` 同时适用于 Free 与 Shared；`plus`、
  `pro`、`custom` 各自独立。单节点只允许对明确列出的非敏感字段做覆盖。
- 模板与节点覆盖不能改变 `resourceClass`、绑定身份、API 凭据、TLS 校验、
  静态 `agent.syncInterval`、Xray 本机路径/目标/命令，也不能携带用户白名单。
- Free 临时白名单仍在 `/panel/agent` 页面按节点操作，使用现有 Grants API；
  保留有效期、审计、提前撤销和到期自动回收，不把白名单并入模板配置。
- 管理员/运营修改模板或节点覆盖应生成 GitOps PR；PR 合并前不生效。模板
  变更影响同类别多个节点，页面需显示作用范围、差异和目标节点数，并记录
  期望修订与 Agent 已应用修订。GitOps 为公开仓库时不得提交内部节点主机名
  或私有路由；节点级配置需先确定受控存储位置。
- 目前 Agent 只支持加载本机预置快照；Accounts 拉取、Portal 配置页面、
  GitOps PR publisher、应用回执和节点灰度仍未实现，禁止据此宣称已托管。

## 渐进发布与历史兼容

| 阶段 | 配置和验证 |
| --- | --- |
| 兼容发布 | 新二进制沿用原 systemd 启动参数和完整 YAML。检查生效配置、心跳和 XHTTP/TCP 同步。离线分别验证 free、shared、plus、pro、custom 的历史配置。 |
| JP Free 灰度 | 先准备并校验节点快照，保留原二进制、完整 YAML 和服务定义，再添加 `-managed-config` 切换。 |
| PH、SG | JP Free 通过后逐台切换，分别验证业务连接及原有资源范围。SG 先恢复 SSH 可达性并核验主机身份。 |
| 回退 | 恢复原服务启动参数、完整 YAML 和原二进制。 |

历史配置由原加载入口处理。显式启用托管模式时要求快照存在且通过加载校验。
该开关目前仅用于已预置的本地快照，生产启用依赖 Accounts 同步和应用回执交付。

## 发布入口与顺序

1. **Accounts**。先在 SIT 验证新配置接口与旧 Agent 的用户同步，再固定镜像
   digest。通过 GitOps 更新 PROD `web-saas-prod` HelmRelease 中的 Accounts
   镜像。确认 Flux 应用预期提交、HelmRelease Ready、Flagger 候选指标有效并
   提升成功。验证节点绑定、配置读取、Vault 引用和原有同步接口。
2. **Console**。Accounts 验收后，用同一路径单独更新 Console 镜像 digest。
   确认 Flagger 提升成功，并验证 Portal 的授权、配置编辑和期望/已应用版本。
   两项发布各自记录回退 digest。需要整体回退时，先恢复 Console，再恢复 Accounts。
3. **Agent**。通过 SSH 按 JP Free → PH → SG 顺序操作。每台先确认服务实际使用的
   二进制路径、运行配置及当前同步状态，保留原二进制、完整 YAML 和 systemd
   配置。先按历史模式运行新二进制，再启用已通过校验的托管快照。
   检查新修订回执、XHTTP/TCP 同步、会话与用量链路后再进入下一台。
   如出现快照应用失败、配置漂移或业务回归，恢复该节点原启动参数、
   原二进制与完整 YAML。

截至 2026-11-08 的只读核查，JP Free 和 PH 的 Agent 服务为 active。
SG 已响应 SSH 握手，但本机尚无受信任的 ED25519 主机密钥。
完整的中心配置下发和运行时应用交付后，才进入本节发布流程。
