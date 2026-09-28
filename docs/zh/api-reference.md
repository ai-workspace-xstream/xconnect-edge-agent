# API 参考

本页是 `xconnect-edge-agent` 的代码级文档层。

它面向仓库维护者，而不是面向第三方 Go SDK 使用者。当前运行时代码大多位于 `internal/` 下，因此这些 API 本身就是模块内使用的。本文档的目标是把设计层和代码层接起来：说明包职责、导出类型、导出函数与方法、它们的参数和返回值，以及把这些部件串起来的运行时主链路。

在本仓库里，常见的软件工程表述与 Go 概念的映射如下：

- 库：Go package
- 类：导出命名类型，通常是 `struct`
- 函数：导出函数或方法
- 接口：Go `interface`；若明确标注为 HTTP 契约，则指外部 HTTP 接口

## 概览

`xconnect-edge-agent` 是部署在 VM 侧的运行时代理。当前主路径是：

1. 读取 YAML 与环境变量驱动的运行时配置
2. 校验运行模式
3. 构建 controller 与 billing 的 HTTP client
4. 周期性拉取活跃 Xray 客户端列表
5. 重新生成 Xray 配置文件
6. 如配置了命令，则先校验再重启 Xray
7. 将节点状态回报给 controller

本页只覆盖当前有效代码路径，描述的是“当前实现”，不是未来版本的兼容承诺。

## 运行时入口链路

运行入口位于 [`cmd/agent/main.go`](../../cmd/agent/main.go)。

```text
main
  -> config.Load(configPath)
  -> 校验 cfg.Mode == "agent"
  -> agentmode.Run(ctx, agentmode.Options{...})
       -> 可选 NewBillingClient(...)
       -> 可选 startBillingSchedulers(...)
       -> NewClient(controllerURL, token, ClientOptions{...})
       -> newSyncTracker()
       -> NewHTTPClientSource(client, tracker)
       -> xrayconfig.NewPeriodicSyncer(...)
            -> source.ListClients(ctx)
            -> generator.Generate(clients)
            -> 可选 validate command
            -> 可选 restart command
       -> runStatusReporter(...)
            -> client.ReportStatus(ctx, agentproto.StatusReport)
```

关键数据流：

- `internal/config` 负责本地配置解码与环境变量覆盖。
- `internal/agentmode` 负责控制循环编排、controller I/O 与 billing 触发。
- `internal/xrayconfig` 负责 Xray 配置渲染与周期同步执行。
- `internal/agentproto` 负责 controller 通信载荷结构。

## 包职责地图

| 包 | 职责 | 被谁使用 |
| --- | --- | --- |
| `cmd/agent` | 二进制入口与进程生命周期装配 | 运行时进程 |
| `internal/config` | YAML 配置模型与加载辅助函数 | `cmd/agent`、`internal/agentmode` |
| `internal/agentmode` | 主控制循环、controller client、billing trigger client、状态回报 | `cmd/agent` |
| `internal/xrayconfig` | Xray 配置模板模型、渲染、周期同步循环 | `internal/agentmode` |
| `internal/agentproto` | controller 通信使用的 HTTP 载荷类型 | `internal/agentmode` |

`deploy/cloudflare/containers/container-src/healthz` 下的辅助程序不纳入本页，因为它没有可复用的导出 API 面。

## 导出类型与函数

### 包 `internal/config`

该包定义本地运行时配置模型。

#### `Config`

- 签名：`type Config struct`
- 所属包：`internal/config`
- 作用：代理进程消费的顶层 YAML 配置对象。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：由 `Load` 与 `LoadReader` 返回。`cmd/agent/main.go` 在进入运行时主循环前要求 `Mode == "agent"`。
- 关联关系：聚合 `Log`、`Agent`、`Xray`、`Billing`。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Mode` | `string` | 运行模式选择器，当前二进制要求是 `agent`。 |
| `Log` | `Log` | 日志配置容器。 |
| `Agent` | `Agent` | controller 与节点运行时设置。 |
| `Xray` | `Xray` | Xray 同步设置。 |
| `Billing` | `Billing` | billing 触发设置。 |

#### `Log`

- 签名：`type Log struct`
- 所属包：`internal/config`
- 作用：最小日志配置模型。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：它属于配置结构的一部分，但当前运行时直接使用 JSON `slog` handler，尚未按该值分支。
- 关联关系：作为 `Config` 的子字段存在。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Level` | `string` | YAML 中声明的日志级别。 |

#### `Agent`

- 签名：`type Agent struct`
- 所属包：`internal/config`
- 作用：节点身份、controller 连接、状态上报节奏与 TLS 行为配置。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：`agentmode.Run` 要求 `ControllerURL` 与 `APIToken` 必填；为空的 interval 与 timeout 会在 `Run` 内归一化。
- 关联关系：作为 `Config` 的子字段；由 `agentmode.Options` 消费。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `ID` | `string` | 上报给 controller 的 agent 身份。 |
| `NodeID` | `string` | 可选节点标识，会体现在状态载荷中。 |
| `Region` | `string` | 可选区域标签。 |
| `LineCode` | `string` | 可选线路标签。 |
| `PricingGroup` | `string` | 可选计费分组标签。 |
| `StatsEnabled` | `bool` | 是否预期节点具备统计能力。 |
| `ControllerURL` | `string` | controller 服务基地址。 |
| `APIToken` | `string` | controller 请求使用的共享服务令牌。 |
| `Domain` | `string` | 注入到 Xray 模板渲染中的主机名。 |
| `HTTPTimeout` | `time.Duration` | controller 请求超时。 |
| `StatusInterval` | `time.Duration` | 状态上报周期。 |
| `SyncInterval` | `time.Duration` | Xray 同步周期；设置后优先于 Xray 自身同步周期。 |
| `TLS` | `TLS` | controller 请求的 TLS 行为。 |

#### `TLS`

- 签名：`type TLS struct`
- 所属包：`internal/config`
- 作用：TLS client 的覆盖设置。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：由 `agentmode.NewClient` 在构造 controller HTTP client 时应用。
- 关联关系：作为 `Agent` 的子字段存在。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `InsecureSkipVerify` | `bool` | 为 true 时关闭 controller HTTPS 证书校验。 |

#### `Xray`

- 签名：`type Xray struct`
- 所属包：`internal/config`
- 作用：Xray 同步设置容器。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：通过 `agentmode.Options` 传入运行时。
- 关联关系：嵌套 `XraySync`。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Sync` | `XraySync` | 同步调度与目标定义。 |

#### `Billing`

- 签名：`type Billing struct`
- 所属包：`internal/config`
- 作用：可选的 billing 任务触发配置。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：仅在 `Enabled` 为 true 时生效；为空的 timeout 与 interval 会在 `agentmode.Run` 中归一化。
- 关联关系：作为 `Config` 的子字段；由 `agentmode.Options` 消费。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Enabled` | `bool` | 是否启用 billing 任务触发循环。 |
| `BaseURL` | `string` | billing 服务基地址。 |
| `HTTPTimeout` | `time.Duration` | 单次 billing 触发请求超时。 |
| `CollectInterval` | `time.Duration` | `collect-and-rate` 任务触发周期。 |
| `ReconcileInterval` | `time.Duration` | `reconcile` 任务触发周期。 |

#### `XraySync`

- 签名：`type XraySync struct`
- 所属包：`internal/config`
- 作用：定义 Xray 配置如何被同步。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：`Targets` 是当前主路径；当 `Targets` 为空时，仍会识别 legacy 单目标字段。
- 关联关系：作为 `Xray` 的子字段；由 `agentmode.Run` 消费。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Enabled` | `bool` | 在配置层表达“是否启用同步”。 |
| `Interval` | `time.Duration` | 当 `Agent.SyncInterval` 未设置时使用的默认同步周期。 |
| `Targets` | `[]SyncTarget` | 当前多目标同步定义。 |
| `OutputPath` | `string` | 兼容模式下的单目标输出路径。 |
| `TemplatePath` | `string` | 兼容模式下的单目标模板路径。 |
| `ValidateCommand` | `[]string` | 兼容模式下的单目标校验命令。 |
| `RestartCommand` | `[]string` | 兼容模式下的单目标重启命令。 |

#### `SyncTarget`

- 签名：`type SyncTarget struct`
- 所属包：`internal/config`
- 作用：定义一个 Xray 配置输出目标。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：每个 target 最终都会对应一个 `xrayconfig.PeriodicSyncer`。
- 关联关系：用于 `XraySync.Targets`。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Name` | `string` | 日志中使用的逻辑目标名。 |
| `OutputPath` | `string` | 目标配置文件输出路径。 |
| `TemplatePath` | `string` | 可选 JSON 模板路径。 |
| `ValidateCommand` | `[]string` | 渲染后、重启前执行的校验命令。 |
| `RestartCommand` | `[]string` | 校验成功后执行的重启命令。 |
| `DynamicUsers` | `DynamicUserSync` | 可选的本机 Xray HandlerService 配置；纯新增在线生效。移除客户端时，每轮 reconciliation 仅重启该 target 的 Xray 一次，因为 HandlerService 无法关闭已建立的会话；不会重启 Caddy。 |

#### `Load`

- 签名：`func Load(path string) (*Config, error)`
- 所属包：`internal/config`
- 作用：从磁盘打开并解码 YAML 配置文件。
- 参数：
  - `path`：YAML 文件路径。
- 返回：
  - `*Config`：解析后的配置对象。
  - `error`：文件打开或解码错误。
- 调用时机 / 约束：由主程序入口使用；具体解码逻辑委托给 `LoadReader`。
- 关联关系：调用 `LoadReader`。

#### `LoadReader`

- 签名：`func LoadReader(r io.Reader) (*Config, error)`
- 所属包：`internal/config`
- 作用：从任意 `io.Reader` 解码配置，并应用受支持的环境变量覆盖。
- 参数：
  - `r`：提供 YAML 内容的 reader。
- 返回：
  - `*Config`：解析并应用覆盖后的配置对象。
  - `error`：解码错误。
- 调用时机 / 约束：
  - 会处理 `AuthUrl`、`INTERNAL_SERVICE_TOKEN`、`DOMAIN`、`BILLING_SERVICE_BASE_URL` 这几个环境变量覆盖。
  - 该覆盖过程属于当前运行时契约的一部分。
- 关联关系：测试场景与文件加载场景共享该解码路径。

### 包 `internal/agentmode`

该包负责控制循环与运行时侧 HTTP 交互。

#### `Options`

- 签名：`type Options struct`
- 所属包：`internal/agentmode`
- 作用：传入 `Run` 的依赖打包结构。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：`Logger` 可以为 nil；运行时会回退到 `slog.Default()`。
- 关联关系：封装 `config.Agent`、`config.Xray`、`config.Billing`。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Logger` | `*slog.Logger` | 运行时 logger。 |
| `Agent` | `config.Agent` | controller 与节点配置。 |
| `Xray` | `config.Xray` | Xray 同步配置。 |
| `Billing` | `config.Billing` | billing 触发配置。 |

#### `ClientOptions`

- 签名：`type ClientOptions struct`
- 所属包：`internal/agentmode`
- 作用：controller 请求 client 的构造参数。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：timeout 为空时默认是 15 秒。
- 关联关系：由 `NewClient` 消费。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Timeout` | `time.Duration` | 请求超时。 |
| `InsecureSkipVerify` | `bool` | TLS 校验覆盖开关。 |
| `UserAgent` | `string` | `User-Agent` 头值。 |
| `AgentID` | `string` | 可选的 `X-Agent-ID` 头值。 |

#### `Client`

- 签名：`type Client struct`
- 所属包：`internal/agentmode`
- 作用：带认证能力的 controller client。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：内部状态封装，不直接暴露字段，应通过 `NewClient` 构造。
- 关联关系：产生 `agentproto.ClientListResponse`，消费 `agentproto.StatusReport`。

#### `BillingClient`

- 签名：`type BillingClient struct`
- 所属包：`internal/agentmode`
- 作用：用于 billing 任务触发端点的最小 HTTP client。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：内部状态封装，应通过 `NewBillingClient` 构造。
- 关联关系：由 `Run` 内启动的 billing scheduler 使用。

#### `HTTPClientSource`

- 签名：`type HTTPClientSource struct`
- 所属包：`internal/agentmode`
- 作用：把 controller client 适配成 `xrayconfig.ClientSource`。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：其构造函数签名包含包私有类型 `*syncTracker`，因此虽然名字导出，但仍然强绑定于 `agentmode` 内部。
- 关联关系：实现了 `xrayconfig.ClientSource`。

#### `Run`

- 签名：`func Run(ctx context.Context, opts Options) error`
- 所属包：`internal/agentmode`
- 作用：启动完整的运行时控制循环。
- 参数：
  - `ctx`：运行时取消边界。
  - `opts`：logger、agent 配置、Xray 配置与 billing 配置。
- 返回：
  - `error`：初始化失败或致命运行时错误。
- 调用时机 / 约束：
  - 要求 `ctx` 非 nil。
  - 要求 `opts.Agent.ControllerURL` 与 `opts.Agent.APIToken` 非空。
  - 会归一化空的 interval 与 timeout。
  - 会为每个解析出来的 Xray target 创建一个周期 syncer。
- 关联关系：
  - 调用 `NewBillingClient`、`NewClient`、`NewHTTPClientSource`。
  - 构建 `xrayconfig.Generator` 与 `xrayconfig.PeriodicSyncer`。
  - 通过 `Client.ReportStatus` 上报 `agentproto.StatusReport`。

#### `NewClient`

- 签名：`func NewClient(baseURL, token string, opts ClientOptions) (*Client, error)`
- 所属包：`internal/agentmode`
- 作用：构建带认证的 controller HTTP client。
- 参数：
  - `baseURL`：controller 基地址。
  - `token`：共享服务令牌。
  - `opts`：timeout、TLS、User-Agent 与可选 agent ID 头设置。
- 返回：
  - `*Client`：初始化后的 controller client。
  - `error`：URL 解析错误、必填参数缺失或初始化错误。
- 调用时机 / 约束：
  - 拒绝空的 base URL 与 token。
  - 若 `InsecureSkipVerify` 为 true，会克隆 transport 并放宽 TLS 校验。
- 关联关系：返回的 client 会被 `NewHTTPClientSource`、`ListClients`、`ReportStatus` 使用。

#### `(*Client) ListClients`

- 签名：`func (c *Client) ListClients(ctx context.Context) (agentproto.ClientListResponse, error)`
- 所属包：`internal/agentmode`
- 作用：从 controller 拉取当前活跃 Xray 客户端集合。
- 参数：
  - `ctx`：请求上下文。
- 返回：
  - `agentproto.ClientListResponse`：包含 clients、total 与 revision 的 controller 响应。
  - `error`：网络错误、状态码错误或解码错误。
- 调用时机 / 约束：
  - 会通过内部 header helper 注入认证头。
  - 仅 `200 OK` 被视为成功。
  - `404 Not Found` 会被视为 endpoint unavailable，以保留当前 retry/fallback 模式，尽管当前 path 列表只有一个。
- 关联关系：由 `HTTPClientSource.ListClients` 包装调用。

#### `(*Client) ReportStatus`

- 签名：`func (c *Client) ReportStatus(ctx context.Context, report agentproto.StatusReport) error`
- 所属包：`internal/agentmode`
- 作用：向 controller 上报运行时状态。
- 参数：
  - `ctx`：请求上下文。
  - `report`：运行时构建出的状态载荷。
- 返回：
  - `error`：编码错误、网络错误或非 2xx 响应错误。
- 调用时机 / 约束：
  - 请求体会序列化为 JSON。
  - 任意 `2xx` 都视为成功。
- 关联关系：由 `Run` 内部启动的 status reporter 调用。

#### `NewBillingClient`

- 签名：`func NewBillingClient(baseURL string, timeout time.Duration) (*BillingClient, error)`
- 所属包：`internal/agentmode`
- 作用：构建 billing trigger HTTP client。
- 参数：
  - `baseURL`：billing 服务基地址。
  - `timeout`：请求超时；为空或非正数时默认 15 秒。
- 返回：
  - `*BillingClient`：初始化后的 client。
  - `error`：URL 为空或非法。
- 调用时机 / 约束：仅当 billing 功能启用时才会构造。
- 关联关系：由 `Run` 与 billing scheduler 使用。

#### `(*BillingClient) TriggerCollectAndRate`

- 签名：`func (c *BillingClient) TriggerCollectAndRate(ctx context.Context) error`
- 所属包：`internal/agentmode`
- 作用：触发 billing 的 collect-and-rate 任务。
- 参数：
  - `ctx`：请求上下文。
- 返回：
  - `error`：请求错误或非 2xx 响应错误。
- 调用时机 / 约束：发送 `POST /v1/jobs/collect-and-rate`。
- 关联关系：由 collect billing loop 调用。

#### `(*BillingClient) TriggerReconcile`

- 签名：`func (c *BillingClient) TriggerReconcile(ctx context.Context) error`
- 所属包：`internal/agentmode`
- 作用：触发 billing 的 reconcile 任务。
- 参数：
  - `ctx`：请求上下文。
- 返回：
  - `error`：请求错误或非 2xx 响应错误。
- 调用时机 / 约束：发送 `POST /v1/jobs/reconcile`。
- 关联关系：由 reconcile billing loop 调用。

#### `NewHTTPClientSource`

- 签名：`func NewHTTPClientSource(client *Client, tracker *syncTracker) *HTTPClientSource`
- 所属包：`internal/agentmode`
- 作用：创建一个由 controller client 驱动的 `ClientSource` 适配器。
- 参数：
  - `client`：用于拉取用户列表的 controller client。
  - `tracker`：包私有的同步状态追踪器，用于记录 fetch 元数据。
- 返回：
  - `*HTTPClientSource`：source 适配器。
- 调用时机 / 约束：
  - 虽然函数名是导出的，但 `tracker` 参数使用了私有类型 `syncTracker`，因此它本质上仍然是面向包内编排代码的构造辅助函数。
- 关联关系：返回值实现 `xrayconfig.ClientSource`。

#### `(*HTTPClientSource) ListClients`

- 签名：`func (s *HTTPClientSource) ListClients(ctx context.Context) ([]xrayconfig.Client, error)`
- 所属包：`internal/agentmode`
- 作用：把 controller 响应适配成 Xray syncer 需要的输入形状。
- 参数：
  - `ctx`：请求上下文。
- 返回：
  - `[]xrayconfig.Client`：用于配置生成的客户端列表。
  - `error`：controller 请求错误。
- 调用时机 / 约束：如果 tracker 存在，会同步更新 fetch 元数据。
- 关联关系：满足 `xrayconfig.ClientSource` 接口。

### 包 `internal/xrayconfig`

该包负责 Xray 配置渲染与周期同步循环。

#### `DefaultFlow`

- 签名：`const DefaultFlow = "xtls-rprx-vision"`
- 所属包：`internal/xrayconfig`
- 作用：在未显式提供 flow 时，为 VLESS 客户端使用的默认 flow。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：在构建非 `xhttp` 路径的渲染结果时会使用。
- 关联关系：在 `Generator.Render` 中被引用。

#### `Client`

- 签名：`type Client struct`
- 所属包：`internal/xrayconfig`
- 作用：配置渲染阶段使用的最小 Xray 客户端记录。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：
  - `ID` 必填。
  - `Email` 在当前实现里也是必填，因为它被用作 Xray stats key。
- 关联关系：由 `ClientSource` 返回；也作为 `agentproto.ClientListResponse` 的嵌套元素存在。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `ID` | `string` | 客户端 UUID。 |
| `Email` | `string` | Xray stats key 与客户端标签。 |
| `Flow` | `string` | 可选的 VLESS flow 覆盖值。 |

#### `Generator`

- 签名：`type Generator struct`
- 所属包：`internal/xrayconfig`
- 作用：负责渲染并写出一个 Xray 配置文件。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：`OutputPath` 必填；若 `Definition` 为空，则回退到 `DefaultDefinition()`。
- 关联关系：由 `PeriodicSyncer` 使用。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Definition` | `Definition` | 基础配置定义提供者。 |
| `OutputPath` | `string` | 目标文件路径。 |
| `FileMode` | `fs.FileMode` | 输出文件权限；默认是 `0644`。 |
| `Domain` | `string` | 注入到模板渲染中的主机名。 |

#### `Definition`

- 签名：`type Definition interface`
- 所属包：`internal/xrayconfig`
- 作用：为 Xray 配置提供“可安全修改的基础文档”的接口契约。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：每次调用 `Base` 都必须返回一个全新的副本，避免后续调用共享可变状态。
- 关联关系：由 `JSONDefinition` 实现；由 `Generator` 消费。

#### `JSONDefinition`

- 签名：`type JSONDefinition struct`
- 所属包：`internal/xrayconfig`
- 作用：基于 JSON 的 `Definition` 实现。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：保存原始 JSON 字节，并在每次 `Base` 调用时重新解码。
- 关联关系：由 `DefaultDefinition` 返回。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Raw` | `[]byte` | 作为定义来源保存的原始 JSON 文档。 |

#### `ClientSource`

- 签名：`type ClientSource interface`
- 所属包：`internal/xrayconfig`
- 作用：为同步流程提供活跃客户端列表的接口契约。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：每次同步都应返回完整的活跃客户端集合。
- 关联关系：由 `agentmode.HTTPClientSource` 实现；由 `PeriodicSyncer` 消费。

#### `PeriodicOptions`

- 签名：`type PeriodicOptions struct`
- 所属包：`internal/xrayconfig`
- 作用：`PeriodicSyncer` 的构造参数集合。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：
  - `Source`、`Generator.OutputPath` 与正数 `Interval` 为必填。
  - `Runner` 可选；为空时默认用基于 `exec.CommandContext` 的命令执行器。
- 关联关系：由 `NewPeriodicSyncer` 消费。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Logger` | `*slog.Logger` | 同步活动日志器。 |
| `Interval` | `time.Duration` | 同步周期。 |
| `Source` | `ClientSource` | 每轮同步的客户端提供者。 |
| `Generator` | `Generator` | 配置渲染与写入器。 |
| `ValidateCommand` | `[]string` | 可选校验命令。 |
| `RestartCommand` | `[]string` | 可选重启命令。 |
| `Runner` | `commandRunner` | 可选命令执行器覆盖。 |
| `OnSync` | `func(SyncResult)` | 每轮同步后触发的可选回调。 |

#### `PeriodicSyncer`

- 签名：`type PeriodicSyncer struct`
- 所属包：`internal/xrayconfig`
- 作用：事件驱动同步客户端，并至少每 30 秒全量兜底；纯新增通过 HandlerService 热添加，移除客户端时重启受影响的 Xray target 以关闭存量会话。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：内部状态封装，应通过 `NewPeriodicSyncer` 构造。
- 关联关系：协调 `ClientSource`、`Generator` 与 shell 命令。

#### `SyncResult`

- 签名：`type SyncResult struct`
- 所属包：`internal/xrayconfig`
- 作用：每次同步结束后发出的结果载荷。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：成功时 `Error` 为 nil。
- 关联关系：会传给可选的 `OnSync` 回调。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Clients` | `int` | 本轮处理的客户端数量。 |
| `Error` | `error` | 同步失败时的错误对象。 |
| `CompletedAt` | `time.Time` | 完成时间，UTC。 |

#### `DefaultDefinition`

- 签名：`func DefaultDefinition() Definition`
- 所属包：`internal/xrayconfig`
- 作用：返回内置的默认 Xray 配置模板。
- 参数：无。
- 返回：
  - `Definition`：基于 JSON 的内置模板。
- 调用时机 / 约束：当 `Generator.Definition` 为空时自动使用。
- 关联关系：返回 `JSONDefinition`。

#### `JSONDefinition.Base`

- 签名：`func (d JSONDefinition) Base() (map[string]interface{}, error)`
- 所属包：`internal/xrayconfig`
- 作用：把原始 JSON 定义解码成一个全新的可变 map。
- 参数：无。
- 返回：
  - `map[string]interface{}`：基础配置树的深拷贝结果。
  - `error`：JSON 解码错误。
- 调用时机 / 约束：返回值可以被调用方安全修改。
- 关联关系：满足 `Definition` 接口。

#### `Generator.Generate`

- 签名：`func (g Generator) Generate(clients []Client) error`
- 所属包：`internal/xrayconfig`
- 作用：渲染配置并以原子方式写入磁盘。
- 参数：
  - `clients`：当前活跃客户端列表。
- 返回：
  - `error`：渲染错误或文件写入错误。
- 调用时机 / 约束：
  - 要求 `OutputPath` 非空。
  - 内部会先调用 `Render`，然后再原子写文件。
  - 默认文件权限是 `0644`。
- 关联关系：由 `PeriodicSyncer` 调用。

#### `Generator.Render`

- 签名：`func (g Generator) Render(clients []Client) ([]byte, error)`
- 所属包：`internal/xrayconfig`
- 作用：仅在内存中生成最终 JSON 配置，不落盘。
- 参数：
  - `clients`：当前活跃客户端列表。
- 返回：
  - `[]byte`：带换行结尾的格式化 JSON。
  - `error`：模板、解码、校验或结构更新错误。
- 调用时机 / 约束：
  - 每个 client 都必须有 `ID`。
  - 当前统计驱动的渲染逻辑要求 `Email` 也必须有值。
  - 既会做 text-template 插值，也会做结构化 client 列表更新。
- 关联关系：由 `Generate` 调用。

#### `NewPeriodicSyncer`

- 签名：`func NewPeriodicSyncer(opts PeriodicOptions) (*PeriodicSyncer, error)`
- 所属包：`internal/xrayconfig`
- 作用：校验参数并构造一个周期 syncer。
- 参数：
  - `opts`：syncer 配置集合。
- 返回：
  - `*PeriodicSyncer`：初始化后的 syncer。
  - `error`：缺少必需依赖或 interval/output path 非法。
- 调用时机 / 约束：拒绝 nil `Source`、空 `Generator.OutputPath`、非正数 `Interval`。
- 关联关系：由 `agentmode.Run` 调用。

#### `(*PeriodicSyncer) Start`

- 签名：`func (s *PeriodicSyncer) Start(ctx context.Context) (func(context.Context) error, error)`
- 所属包：`internal/xrayconfig`
- 作用：启动后台同步循环，并返回一个 stop 函数。
- 参数：
  - `ctx`：循环生命周期上下文。
- 返回：
  - `func(context.Context) error`：用于取消循环并等待退出的 stop 函数。
  - `error`：syncer 为 nil 或启动错误。
- 调用时机 / 约束：
  - stop 函数自身还接受一个等待上下文。
  - 在进入 ticker 周期前，会先立即执行一次同步。
- 关联关系：由 `agentmode.Run` 管理其生命周期。

### 包 `internal/agentproto`

该包定义 controller 通信载荷类型。

#### `ClientListResponse`

- 签名：`type ClientListResponse struct`
- 所属包：`internal/agentproto`
- 作用：agent 请求活跃客户端列表时，controller 返回的响应体结构。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：当前 `agentmode.Client.ListClients` 期望接收该 JSON 形状。
- 关联关系：包含 `[]xrayconfig.Client`。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Clients` | `[]xrayconfig.Client` | 活跃 Xray 客户端列表。 |
| `Total` | `int` | controller 侧总数。 |
| `GeneratedAt` | `time.Time` | controller 生成时间。 |
| `Revision` | `string` | 可选的 revision 标记。 |

#### `StatusReport`

- 签名：`type StatusReport struct`
- 所属包：`internal/agentproto`
- 作用：agent 上报给 controller 的运行时状态结构。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：由 `agentmode` 根据 sync tracker 状态与 agent 配置构建。
- 关联关系：包含 `XrayStatus`。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `AgentID` | `string` | 自报的 agent 身份。 |
| `Healthy` | `bool` | 高层健康信号。 |
| `Message` | `string` | 可选健康说明或错误信息。 |
| `Users` | `int` | 最近同步状态中看到的活跃用户数。 |
| `SyncRevision` | `string` | 最近 fetch 的 revision 标记。 |
| `Xray` | `XrayStatus` | 嵌套的 Xray 状态对象。 |

#### `XrayStatus`

- 签名：`type XrayStatus struct`
- 所属包：`internal/agentproto`
- 作用：描述被管理的 Xray 运行状态的嵌套对象。
- 参数：无。
- 返回：无。
- 调用时机 / 约束：`LastSync` 是可选字段。
- 关联关系：嵌套于 `StatusReport` 中。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `Running` | `bool` | 从 agent 视角看，Xray 是否仍处于“最近成功同步，因此可视为运行中”的状态。 |
| `Clients` | `int` | 当前活跃客户端数量。 |
| `LastSync` | `*time.Time` | 最近一次成功同步时间。 |
| `ConfigHash` | `string` | 可选配置哈希字段；当前运行时尚未填充。 |
| `NodeID` | `string` | 可选节点标识。 |
| `Region` | `string` | 可选区域标签。 |
| `LineCode` | `string` | 可选线路标签。 |
| `PricingGroup` | `string` | 可选计费分组标签。 |
| `StatsEnabled` | `bool` | 是否预期统计能力可用。 |
| `XrayRevision` | `string` | 可选 revision 标记，来自最近一次同步。 |

## 外部 HTTP 契约

本节记录外部 HTTP 接口。这些不是 Go interface。

### Controller 契约

#### `GET /api/agent-server/v1/users`

- 调用方：`agentmode.(*Client).ListClients`
- 认证头：
  - `Authorization: Bearer <token>`
  - `X-Service-Token: <token>`
  - 配置了 agent ID 时会加上 `X-Agent-ID: <agentID>`
  - `User-Agent: <userAgent>`
- 成功响应：`200 OK`
- 响应体：`agentproto.ClientListResponse`
- 失败处理：
  - 非 `200` 响应会转成错误
  - `404` 在当前实现里会被视为 endpoint unavailable，以保留 retry/fallback 模式

#### `POST /api/agent-server/v1/status`

- 调用方：`agentmode.(*Client).ReportStatus`
- 认证头：
  - `Authorization: Bearer <token>`
  - `X-Service-Token: <token>`
  - 配置了 agent ID 时会加上 `X-Agent-ID: <agentID>`
  - `User-Agent: <userAgent>`
- 请求体：`agentproto.StatusReport`
- 成功响应：任意 `2xx`
- 失败处理：
  - 非 `2xx` 响应会转成错误
  - `404` 在当前实现里会被视为 endpoint unavailable，以保留 retry/fallback 模式

### Billing 契约

这些端点只负责运营触发，不构成 billing 真相源。

#### `POST /v1/jobs/collect-and-rate`

- 调用方：`agentmode.(*BillingClient).TriggerCollectAndRate`
- 请求体：无
- 成功响应：任意 `2xx`
- 失败处理：非 `2xx` 响应会返回携带响应体片段的错误

#### `POST /v1/jobs/reconcile`

- 调用方：`agentmode.(*BillingClient).TriggerReconcile`
- 请求体：无
- 成功响应：任意 `2xx`
- 失败处理：非 `2xx` 响应会返回携带响应体片段的错误

## 兼容字段说明

当前代码仍保留一组配置层兼容桥接：

- `config.XraySync.Targets` 是当前主路径
- `config.XraySync.OutputPath`
- `config.XraySync.TemplatePath`
- `config.XraySync.ValidateCommand`
- `config.XraySync.RestartCommand`

当 `Targets` 为空时，`agentmode.Run` 会把这些 legacy 单目标字段转换成一个合成的 `config.SyncTarget`。

本页只记录该行为是“当前状态”，不会把它扩展为额外的兼容承诺。

## 源码索引

| 源文件 | 主要导出符号 |
| --- | --- |
| [`cmd/agent/main.go`](../../cmd/agent/main.go) | 运行时入口 |
| [`internal/config/config.go`](../../internal/config/config.go) | `Config`、`Log`、`Agent`、`TLS`、`Xray`、`Billing`、`XraySync`、`SyncTarget`、`Load`、`LoadReader` |
| [`internal/agentmode/runner.go`](../../internal/agentmode/runner.go) | `Options`、`Run` |
| [`internal/agentmode/client.go`](../../internal/agentmode/client.go) | `ClientOptions`、`Client`、`NewClient`、`(*Client).ListClients`、`(*Client).ReportStatus` |
| [`internal/agentmode/source_http.go`](../../internal/agentmode/source_http.go) | `HTTPClientSource`、`NewHTTPClientSource`、`(*HTTPClientSource).ListClients` |
| [`internal/agentmode/billing_client.go`](../../internal/agentmode/billing_client.go) | `BillingClient`、`NewBillingClient`、`(*BillingClient).TriggerCollectAndRate`、`(*BillingClient).TriggerReconcile` |
| [`internal/xrayconfig/generator.go`](../../internal/xrayconfig/generator.go) | `Client`、`Generator`、`(*Generator).Generate`、`(*Generator).Render` |
| [`internal/xrayconfig/definition.go`](../../internal/xrayconfig/definition.go) | `Definition`、`JSONDefinition`、`Base` |
| [`internal/xrayconfig/templates.go`](../../internal/xrayconfig/templates.go) | `DefaultDefinition`、`DefaultFlow` |
| [`internal/xrayconfig/syncer.go`](../../internal/xrayconfig/syncer.go) | `ClientSource`、`PeriodicOptions`、`PeriodicSyncer`、`SyncResult`、`NewPeriodicSyncer`、`(*PeriodicSyncer).Start` |
| [`internal/agentproto/types.go`](../../internal/agentproto/types.go) | `ClientListResponse`、`StatusReport`、`XrayStatus` |
