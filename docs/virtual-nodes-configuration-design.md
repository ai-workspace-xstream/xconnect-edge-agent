# 虚拟节点配置格式设计（Draft v2）

> Status: DRAFT — only the guarded main-envelope preflight is implemented; virtual-node runtime and child/runtime contracts remain unimplemented.
> Current evidence: [P0 audit](tasks/2026-11-02-virtual-nodes-p0-audit.md)
> Date: 2026-11-02
> Related PRs: 尚未创建
> 总体方案：[虚拟节点规划](tasks/2026-11-02-virtual-nodes-plan.md)
> 完整示例：[示例目录与使用说明](examples/virtual-nodes/README.md)

## 1. 模式开关

低配节点继续用单节点，高配、高流量宿主机按配置分成多个逻辑节点：

```yaml
virtualNodes:
  enabled: false # 单节点：使用顶层 agent 和 xray
```

```yaml
virtualNodes:
  enabled: true # 多节点：一个 Agent 进程，加载 nodes 中的 1～N 个节点
  hostId: host-us-01
  configRoot: /etc/agent/nodes
  maxConcurrentReconciles: 2
  nodes:
    - configFile: nodes/us-a/agent.yaml
      runtimeFile: nodes/us-a/runtime.yaml
    - configFile: nodes/us-b/agent.yaml
      runtimeFile: nodes/us-b/runtime.yaml
```

| 配置情况 | 新版实现预期行为 |
| --- | --- |
| 完全没有 virtualNodes | 原单节点模式，老配置和老环境变量按原逻辑生效 |
| enabled 缺省或 false | 单节点模式，只运行顶层 agent/xray；不读取、登记或启动清单里的节点 |
| enabled=true，nodes 含 1 项 | 虚拟节点模式，运行 1 个节点；便于同一配置结构后续扩展 |
| enabled=true，nodes 含 N 项 | 虚拟节点模式，运行 N 个 Xray/逻辑身份；无额外默认节点 |
| enabled=true，nodes 为空 | 配置错误，启动失败，不回退单节点 |
| enabled 为字符串等错误类型 | 配置错误，不进行宽松类型转换 |

N 等于清单长度，不额外引入 nodeCount、模板循环或自动克隆字段。每项必须提供独立配置。首期虚拟模式每项一个 Xray、一个 xhttp target；原单节点多 target 功能不变。虚拟节点多传输扩展需另行把每 target 的 runtime/计量源映射定义完整，不能让多个进程复用一个计量源。

这是逻辑拆分：域名、用户范围、配置和统计独立，不自动分配独立出口 IP、物理带宽或套餐额度。首期没有节点网速/流量份额切割功能，CPU/内存限制也不是独占资源保证。

## 2. 文件组织与版本

```text
/etc/agent/
  account-agent.yaml             # 主清单（只启动一个 Agent）
  artifacts/xray.yaml             # 部署系统提供的不可变制品清单
  nodes/
    us-a/
      agent.yaml                 # 完整业务配置
      runtime.yaml               # systemd 运行与入口描述
      xray-template.json         # 独立 Xray 模板，不含真实用户
    us-b/
      agent.yaml
      runtime.yaml
      xray-template.json
/run/credentials/xconnect-edge-agent/
  us-a-accounts-token            # 运行时注入，不在仓库
  us-b-accounts-token
/var/lib/agent/nodes/<nodeId>/
  xray.json                      # 渲染输出，包含敏感用户材料
  metering/                      # 采集所有者的 checkpoint/outbox
```

拟议主配置与子配置使用 `configVersion: 2`；子配置以 `kind: virtual-node` / `kind: xray-runtime` 区分。旧主配置未声明 configVersion 时走原格式兼容路径；新 virtualNodes 块要求显式版本 2，未知版本拒绝启动。所有 v2 文件严格解码，拒绝重复键、未知字段和多文档 YAML；false 模式不读取子文件，但仍检查主文件字段与类型。

**旧 YAML loader 可能静默忽略新字段。首批改动提供 `-capabilities` 和 `-check-config -config <file>`，但明确返回 `virtualNodeRuntime: false`；enabled=true 仍拒绝启动，不读取子文件。** 部署必须检查固定制品能力，不能仅凭 configVersion 认定支持多节点。预检目前只验证主配置语法、字段和授权范围，不验证运行依赖；本文后续子文件、运行时和计量校验仍是待实现契约。

不支持任意 include、远程配置 URL、YAML 自定义 tag、shell 展开、`${VAR}` 通用插值和自动 glob。清单显式列文件；复制示例不会替换域名或生成凭据。

## 3. 主配置字段

| 字段 | 类型/默认 | 约束与行为 |
| --- | --- | --- |
| configVersion | 整数；旧格式可缺省 | 新清单固定为 2 |
| mode | 字符串 agent | 复用原 Agent 模式 |
| log.level | 字符串 info | 共用日志级别，日志按 hostId/nodeId/sourceId 区分；不能输出凭据或邮箱列表 |
| agent、xray | 原结构 | enabled=false 时使用；true 时禁止出现，避免身份和 target 来源歧义 |
| billing | 原结构 | 全进程级可选作业触发，最多初始化一次；不在子文件出现 |
| virtualNodes.enabled | 布尔 false | 唯一模式开关，不通过环境变量隐式打开 |
| virtualNodes.hostId | 字符串；true 时必填 | 稳定宿主机标识；不等于 nodeId，不影响用户授权 |
| virtualNodes.configRoot | 绝对目录；true 时必填 | 子配置与模板允许读取的根目录，部署侧管理、业务进程不可改写 |
| virtualNodes.maxConcurrentReconciles | 正整数；默认 2 | 只限制同一时刻同步/渲染/应用的任务数；不能让长连接事件流永久占用工作槽；不是 N 上限 |
| virtualNodes.nodes | 显式列表 | true 时至少 1 项；每项只含 configFile、runtimeFile，无身份或权限覆盖字段 |

configFile/runtimeFile 相对路径基于**主配置所在目录**，绝对路径也必须在 configRoot 内；解析真实路径后检查边界。子文件 templatePath 相对其 agent.yaml 所在目录，且必须在同一允许根内。输出、状态、日志路径必须是批准数据根下的绝对路径；不能借符号链接、`..` 或重复挂载越界。凭据目录单独配置为部署侧批准的只读来源，不受模板目录授权扩散。

false 模式可保留未使用的清单字段，以便运维切换；发出脱敏提示“virtual node list ignored”，不得尝试读取子文件。此时顶层单节点配置仍必须有效，缺失则报错，不从清单挑第一个节点。

## 4. 每节点 agent.yaml

完整两份样例分别为 [us-a](examples/virtual-nodes/nodes/us-a/agent.yaml) 和 [us-b](examples/virtual-nodes/nodes/us-b/agent.yaml)。二者独立配置，B 不继承 A 的字段。

| 字段组 | 要求 |
| --- | --- |
| agent.id / nodeId | 非空、清单内唯一；新虚拟节点首期采用同值减少映射歧义，仍由服务端验证逻辑身份与凭据关系；迁移旧节点保持原身份，不强制改名 |
| agent.role | 固定 agent-proxy，禁止接管 gateway/one 数据面 |
| agent.controllerUrl | 显式 HTTPS 地址，不能从顶层业务配置继承 |
| agent.credentialFile | 新增，运行时纯文本 token 文件；读取后仅去掉末尾换行，空值/不可读即失败；禁止与 apiToken 同时配置 |
| agent.domain / entryPoint | 显式专属域名，首期二者与 runtime.ingress.host 必须一致；不使用域名作永久主键 |
| agent.region / pool | 沿用原展示与分类语义，不能由 pool 推导权限或传输路径 |
| agent.resourceClass / teamId / allowedEmails | 复用现有权限；custom 必须非空邮箱列表，其他等级不接受 allowedEmails；团队规则按既有契约，不能由本地文件扩大服务端授权 |
| agent.customAllowFree | 可选布尔，默认 false，仅适用于 custom；true 时在 Plus/Pro 基础上允许邮箱白名单内 Free 用户，账号状态/暂停/配额和团队隔离继续生效，不升级套餐。此字段由独立兼容 PR 实现，多节点配置结构本身仍未实现 |
| agent.openToUsers | 保留原发布语义，不绕过 custom/team 过滤；最终访问范围来自 Accounts |
| agent.statsEnabled | 可选能力/状态元数据，不能替代 metering.enabled；不是免计费开关 |
| agent.httpTimeout / statusInterval / syncInterval | 正时长，例 15s / 1m / 5m；不能为 0 或负数 |
| agent.tls.insecureSkipVerify | 示例固定 false，生产必须验证证书 |
| xray.sync | 沿用模板渲染、调优与用户同步结构；首期 enabled=true 且恰好一个 target |
| target.templatePath / outputPath | 独立模板与输出，输出必须和 runtime.xray.configPath 一致 |
| target.dynamicUsers | 沿用现有支持的可选功能；server 必须指向本节点 API，executable 匹配批准制品；会话撤销能力按现有版本验证 |

新虚拟模式的 validate/restart 不从 target.validateCommand/restartCommand 运行任意命令，而由 runtime driver 生成固定参数；这些字段若非空则报错，防止绕过实例白名单。旧单节点模式继续保留原命令配置行为。

凭据文件由部署身份预置，限定业务身份读取。每节点文件不代表后台已具备独立 token 签发：Accounts 当前认证是否可做节点级绑定是实施 P0 门槛，不能通过多个文件包装同一个全局秘密就宣称实现最小权限。

有效期、用户套餐、福利包和用户额度沿用 Accounts/Billing 权威状态，不在文件中增加本地 expiresAt、price、unlimitedTraffic、userQuota 等覆盖字段。

## 5. 每节点 runtime.yaml

完整样例：[us-a](examples/virtual-nodes/nodes/us-a/runtime.yaml)、[us-b](examples/virtual-nodes/nodes/us-b/runtime.yaml)。这是部署描述，不是 systemd Unit，不能直接交给 systemctl 或当前 Agent 执行。

| 字段 | 约束与解释 |
| --- | --- |
| nodeId | 必须匹配关联 agent.yaml 的 nodeId |
| driver | 首期仅 systemd，其他值拒绝；未来容器驱动另行定义 |
| xray.service | 明确实例服务名，必须在部署侧批准的服务白名单中 |
| xray.binaryPath / artifactManifest | 指向部署侧固定版本二进制和制品清单；清单至少提供 version、sha256，匹配实际二进制；不接受 latest 或占位 checksum |
| xray.configPath | 与 target.outputPath 完全相同 |
| xray.user / group | 专用业务身份，部署侧创建；Agent 不负责新增系统账户 |
| xray.stateDir / logDir | 独立目录；logDir 为文件日志挂载预留，当前示例 Xray stdout/stderr 由 journal 收集，不表示已经写文件 |
| xray.resources.cpuQuotaPercent | 正整数；100 表示一个 CPU 的时间配额，允许大于 100；systemd adapter 映射 CPUQuota，不是保证预留 |
| xray.resources.memoryMaxBytes | 正整数 bytes，映射 MemoryMax；示例每节点 512 MiB，实际按容量测试选择 |
| xray.health | type=tcp、endpoint、interval、timeout；仅说明后端监听可达，不能作为真实代理链路验证 |
| xray.stopTimeout | 正时长；受控停机等待期限，不保证长连接无中断 |
| ingress | owner 固定 shared-caddy，明确 host、publicPort、transport、path、upstream、tlsTermination |
| exporter | service、listenAddress、sourceId，绑定已有采集链路；不是在 Agent 中启动全局计费作业 |

部署侧根据 ingress 描述汇总、校验并发布一份 Caddy 配置，预置 Xray/exporter 服务；Agent 仅持有固定实例的受限校验/重载能力，不自行下载二进制、写系统 Unit、管理证书或获得 Caddy 通用管理权限。制品清单由部署侧提供，本例不捏造实际版本或 checksum。

首期示例的入口是 TLS 在 Caddy 终止的 XHTTP，后端只监听 loopback。Xray JSON 中的端口/API/path 必须和 runtime、metering 一致；模板里的 clients 为空，由授权同步产生真实客户端和稳定统计身份。必须通过指定 Xray 版本校验及真实流量验证，不能凭 JSON 能解析就宣称可用。通用 TCP/SNI 分流不在此示例中假定支持。

## 6. metering 字段与计量所有者

| 字段 | 语义 |
| --- | --- |
| enabled | 布尔，首期公开服务的虚拟节点必须 true；缺省 true；false 仅供未发布的隔离测试，不能隐式转为免流 |
| collector | 首期固定 external-exporter，表示复用/适配外部采集器，Agent 不并行读取扣费计数 |
| sourceId | 必填、宿主机清单内唯一，匹配 runtime.exporter.sourceId；服务端去重仍需结合来源授权和 epoch |
| statsProtocol / statsEndpoint | 首期拟议 xray-grpc 与本机地址，和模板 API/dynamicUsers 一致；现有 exporter 是否原生支持须先核实，否则适配后才可放行 |
| counterMode / resetOnRead | 拟议 cumulative / false；采集不能清零，差分、epoch 与服务端去重需一起实现；不能只改配置就转换既有上报语义 |
| scrapeInterval | 正时长，示例 15s，需在 UAT 验证开销与时延 |
| stateDir | 独立持久目录，由唯一计量所有者写入；Agent 不能与 exporter 同时推进 checkpoint |

nodeId 从对应节点配置绑定、hostId 从主清单绑定，不接受另一个可覆盖这些身份的计费字段。epoch、运行实例 ID、事件序列、ACK/outbox 进度由运行时产生并持久化，不由人手填固定值。hostId 作为采集时归属写入记录，不能仅在查询时用“当前宿主机”反查。

全局 `billing.enabled=false` 只停用 Agent 的可选作业触发，不关闭采集、扣费、配额或退款；现有中心调度可继续处理数据。节点统计、免流权益和计费调度三个开关不能互相替代。

## 7. 加载、环境变量和错误处理

1. 解析主文件版本及类型，决定 enabled。
2. false：按旧路径加载顶层 agent/xray 和旧环境覆盖，不打开任何虚拟节点子文件。
3. true：拒绝顶层 agent/xray；解析显式文件，校验版本/kind、根路径、身份、授权配置和引用一致性。
4. 对整个清单检查重复节点 ID、agentId、域名、服务、输出/状态路径、sourceId 和本地监听地址。检查 Xray 模板和 runtime/采集配置对应。不能只比较字符串：解析地址、端口和 wildcard/IPv6 绑定，拒绝实际 socket 冲突。
5. 检查制品、权限和受限服务动作。任一配置/凭据错误则本轮不启动、不注册、不渲染任何新节点；保留已在运行的服务，不自动清空配置。
6. 成功后创建各节点上下文，独立同步/上报/重试；计费触发器只创建一次。运行期单节点临时故障按节点退避，不退出其他节点循环，不改换身份。

true 模式不接受 AuthUrl、INTERNAL_SERVICE_TOKEN、DOMAIN、XCONNECT_ROLE、AGENT_RESOURCE_CLASS、AGENT_TEAM_ID、AGENT_CUSTOM_EMAILS 等旧业务覆盖环境变量；若存在（包括空值）即报出变量名及冲突字段，绝不输出值。控制端地址和凭据在各节点显式提供。BILLING_SERVICE_BASE_URL 保留全局 billing 覆盖语义，因为作业本来就是主进程级；日志级别等纯运行参数按既有规则处理。

子文件不支持 defaults 或节点间继承，避免“顶层 plus 覆盖全部 custom”及邮箱列表合并。更新后的多节点配置应先离线预检再重启 Agent；运行时不自动监听文件变更。凭据轮换也采用部署后重启受控加载，首期不做隐式热重读。

拟议错误应包含文件、字段和脱敏节点标识，例如：`nodes/us-b/agent.yaml: metering.sourceId duplicates another node`；不能把 token、完整邮箱列表或渲染后的用户配置放入错误日志。

## 8. 开关切换与验收

开关控制下一次 Agent 加载模式，**不会自动停止之前部署的 Xray、删除 Accounts 记录或清空计量状态**。因此不能把修改 true/false 当作完整迁移流程。

- 单 → 多：先预置不冲突的域名、服务、目录和节点身份；验证新清单与凭据；旧节点保持或受控排空后切换。若复用旧 nodeId，先完成旧写者及计量进度交接，不能让两套数据面同时使用该身份。
- 多 → 单：先准备有效顶层单节点配置；受控摘除不保留节点的入口、排空连接、确认已采集记录上传并停用相应数据面/采集写者，再置 false 并重启 Agent。不能只关 Agent 的节点循环却遗留仍能接入的旧 Xray。
- 历史账单、checkpoint 和退役节点身份保留，停用节点不回收 ID。失败时按现有版本回退，不能删除已发生用量。

配置验收至少覆盖：enabled 缺省/false/true、true 下 N=1/N=2、空清单、错误布尔/未知字段、重复键、不可读子文件、目录逃逸、端口冲突、遗留环境变量、凭据读取失败、custom 空白名单、nodeId 不匹配、共享统计源、计量关闭但节点已发布、制品不匹配。预检不得改服务；false 必须证明未读取清单。功能验收还需真实代理、跨节点授权撤销、重试归账、全局配额和模式切换回归。

本文及示例只完成格式设计与静态数据检查；尚无实现级 schema 校验、Xray/Caddy 运行验收或生产变更。
