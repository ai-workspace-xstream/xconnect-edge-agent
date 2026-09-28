# API Reference

This page is the code-level reference layer for `xconnect-edge-agent`.

It is written for maintainers of this repository rather than for third-party Go consumers. Most runtime code lives under `internal/`, so these APIs are intentionally module-local. The goal of this page is to bridge the design layer and the code layer: package responsibilities, exported types, exported functions and methods, their parameters and return values, and the runtime call chain that ties them together.

In this repository, the user-facing shorthand maps to Go concepts as follows:

- Library: Go package
- Class: exported named type, usually `struct`
- Function: exported function or method
- Interface: Go `interface`, or an external HTTP contract when explicitly marked as such

## Overview

`xconnect-edge-agent` is a VM-side runtime agent. Its main path is:

1. Load YAML and environment-backed runtime config
2. Validate run mode
3. Build controller and billing clients
4. Periodically fetch active Xray clients
5. Regenerate Xray config files
6. Validate and restart Xray if commands are configured
7. Report node status back to the controller

The current code reference covers the effective runtime path only. It documents current behavior as implemented, not future compatibility promises.

## Runtime Entry Flow

The runtime starts in [`cmd/agent/main.go`](../../cmd/agent/main.go).

```text
main
  -> config.Load(configPath)
  -> validate cfg.Mode == "agent"
  -> agentmode.Run(ctx, agentmode.Options{...})
       -> optional NewBillingClient(...)
       -> optional startBillingSchedulers(...)
       -> NewClient(controllerURL, token, ClientOptions{...})
       -> newSyncTracker()
       -> NewHTTPClientSource(client, tracker)
       -> xrayconfig.NewPeriodicSyncer(...)
            -> source.ListClients(ctx)
            -> generator.Generate(clients)
            -> optional validate command
            -> optional restart command
       -> runStatusReporter(...)
            -> client.ReportStatus(ctx, agentproto.StatusReport)
```

Key data flow:

- `internal/config` owns local configuration decoding and environment overrides.
- `internal/agentmode` owns control-loop orchestration, controller I/O, and billing triggers.
- `internal/xrayconfig` owns Xray config rendering and sync execution.
- `internal/agentproto` owns controller-facing payload shapes.

## Package Map

| Package | Role | Used by |
| --- | --- | --- |
| `cmd/agent` | Binary entrypoint and process lifecycle wiring | runtime process |
| `internal/config` | YAML config model and load helpers | `cmd/agent`, `internal/agentmode` |
| `internal/agentmode` | Main control loop, controller client, billing trigger client, status reporting | `cmd/agent` |
| `internal/xrayconfig` | Xray config template model, rendering, periodic sync loop | `internal/agentmode` |
| `internal/agentproto` | Shared HTTP payload types for controller communication | `internal/agentmode` |

The deployment helper under `deploy/cloudflare/containers/container-src/healthz` is not documented here because it does not expose a reusable exported API surface.

## Exported Types and Functions

### Package `internal/config`

This package defines the local runtime configuration model.

#### `Config`

- Signature: `type Config struct`
- Package: `internal/config`
- Purpose: Top-level YAML configuration object consumed by the agent process.
- Parameters: None.
- Returns: None.
- Usage and constraints: Returned by `Load` and `LoadReader`. `cmd/agent/main.go` requires `Mode == "agent"` before entering the runtime loop.
- Relationships: Aggregates `Log`, `Agent`, `Xray`, and `Billing`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Mode` | `string` | Run mode selector. Current binary expects `agent`. |
| `Log` | `Log` | Logging configuration container. |
| `Agent` | `Agent` | Controller and node runtime settings. |
| `Xray` | `Xray` | Xray sync settings. |
| `Billing` | `Billing` | Billing trigger settings. |

#### `Log`

- Signature: `type Log struct`
- Package: `internal/config`
- Purpose: Minimal logging configuration model.
- Parameters: None.
- Returns: None.
- Usage and constraints: Present in config shape, but current runtime uses a JSON `slog` handler directly and does not branch on this value yet.
- Relationships: Embedded under `Config`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Level` | `string` | Intended log level value from YAML. |

#### `Agent`

- Signature: `type Agent struct`
- Package: `internal/config`
- Purpose: Node identity, controller connection, status cadence, and TLS behavior.
- Parameters: None.
- Returns: None.
- Usage and constraints: `ControllerURL` and `APIToken` are required by `agentmode.Run`. Empty intervals and timeouts are normalized inside `Run`.
- Relationships: Embedded under `Config`; consumed by `agentmode.Options`.

| Field | Type | Meaning |
| --- | --- | --- |
| `ID` | `string` | Agent identity reported to the controller. |
| `NodeID` | `string` | Optional node identifier surfaced in status reports. |
| `Region` | `string` | Optional region label for status payloads. |
| `LineCode` | `string` | Optional routing/line label. |
| `PricingGroup` | `string` | Optional billing grouping label. |
| `StatsEnabled` | `bool` | Whether node stats are expected to be available. |
| `ControllerURL` | `string` | Base URL of the controller service. |
| `APIToken` | `string` | Shared service token for controller requests. |
| `Domain` | `string` | Hostname injected into Xray template rendering. |
| `HTTPTimeout` | `time.Duration` | Per-request timeout for controller requests. |
| `StatusInterval` | `time.Duration` | Period for status reporting. |
| `SyncInterval` | `time.Duration` | Period for Xray sync work; overrides Xray sync interval when set. |
| `TLS` | `TLS` | TLS client behavior for controller requests. |

#### `TLS`

- Signature: `type TLS struct`
- Package: `internal/config`
- Purpose: TLS client override settings.
- Parameters: None.
- Returns: None.
- Usage and constraints: Applied by `agentmode.NewClient` when building the controller HTTP client.
- Relationships: Embedded under `Agent`.

| Field | Type | Meaning |
| --- | --- | --- |
| `InsecureSkipVerify` | `bool` | If true, disables TLS certificate verification for controller calls. |

#### `Xray`

- Signature: `type Xray struct`
- Package: `internal/config`
- Purpose: Container for Xray sync settings.
- Parameters: None.
- Returns: None.
- Usage and constraints: Passed through `agentmode.Options`.
- Relationships: Embeds `XraySync`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Sync` | `XraySync` | Sync scheduling and target definitions. |

#### `Billing`

- Signature: `type Billing struct`
- Package: `internal/config`
- Purpose: Optional billing trigger configuration.
- Parameters: None.
- Returns: None.
- Usage and constraints: Only used when `Enabled` is true. Empty timeout and intervals are normalized in `agentmode.Run`.
- Relationships: Embedded under `Config`; used by `agentmode.Options`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Enabled` | `bool` | Enables billing job trigger loops. |
| `BaseURL` | `string` | Billing service base URL. |
| `HTTPTimeout` | `time.Duration` | Timeout per billing trigger request. |
| `CollectInterval` | `time.Duration` | Period for collect-and-rate job triggers. |
| `ReconcileInterval` | `time.Duration` | Period for reconcile job triggers. |

#### `XraySync`

- Signature: `type XraySync struct`
- Package: `internal/config`
- Purpose: Defines how Xray config files should be synchronized.
- Parameters: None.
- Returns: None.
- Usage and constraints: `Targets` is the main current path. Legacy single-target fields are still recognized when `Targets` is empty.
- Relationships: Embedded under `Xray`; consumed by `agentmode.Run`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Enabled` | `bool` | Declares whether sync is enabled in config shape. |
| `Interval` | `time.Duration` | Default sync interval when `Agent.SyncInterval` is not set. |
| `Targets` | `[]SyncTarget` | Current multi-target sync definition. |
| `OutputPath` | `string` | Legacy single-target output path. |
| `TemplatePath` | `string` | Legacy single-target template path. |
| `ValidateCommand` | `[]string` | Legacy single-target validation command. |
| `RestartCommand` | `[]string` | Legacy single-target restart command. |

#### `SyncTarget`

- Signature: `type SyncTarget struct`
- Package: `internal/config`
- Purpose: Defines one Xray config output target.
- Parameters: None.
- Returns: None.
- Usage and constraints: Built into one `xrayconfig.PeriodicSyncer` per target.
- Relationships: Used inside `XraySync.Targets`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Name` | `string` | Logical target name used in logs. |
| `OutputPath` | `string` | Destination config file path. |
| `TemplatePath` | `string` | Optional JSON template file path. |
| `ValidateCommand` | `[]string` | Optional command run after render and before restart. |
| `RestartCommand` | `[]string` | Optional command run after successful validation. |
| `DynamicUsers` | `DynamicUserSync` | Optional local Xray HandlerService endpoint used to add users without restart. Any client withdrawal restarts this target's Xray once per reconciliation because HandlerService removal cannot close established sessions; Caddy is not restarted. |

#### `Load`

- Signature: `func Load(path string) (*Config, error)`
- Package: `internal/config`
- Purpose: Opens a YAML config file from disk and decodes it.
- Parameters:
  - `path`: filesystem path to the YAML file.
- Returns:
  - `*Config`: parsed config object.
  - `error`: file open or decode error.
- Usage and constraints: Used by the main binary entrypoint. Delegates decode work to `LoadReader`.
- Relationships: Calls `LoadReader`.

#### `LoadReader`

- Signature: `func LoadReader(r io.Reader) (*Config, error)`
- Package: `internal/config`
- Purpose: Decodes config from any `io.Reader` and applies supported environment overrides.
- Parameters:
  - `r`: reader that provides YAML bytes.
- Returns:
  - `*Config`: parsed and overridden config object.
  - `error`: decode error.
- Usage and constraints:
  - Applies environment overrides for `AuthUrl`, `INTERNAL_SERVICE_TOKEN`, `DOMAIN`, and `BILLING_SERVICE_BASE_URL`.
  - The override stage is part of the current runtime contract.
- Relationships: Shared decode path for tests and file-based loading.

### Package `internal/agentmode`

This package owns the control loop and all runtime-side HTTP interactions.

#### `Options`

- Signature: `type Options struct`
- Package: `internal/agentmode`
- Purpose: Dependency bundle passed into `Run`.
- Parameters: None.
- Returns: None.
- Usage and constraints: `Logger` may be nil; runtime falls back to `slog.Default()`.
- Relationships: Wraps `config.Agent`, `config.Xray`, and `config.Billing`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Logger` | `*slog.Logger` | Runtime logger. |
| `Agent` | `config.Agent` | Controller and node config. |
| `Xray` | `config.Xray` | Xray sync config. |
| `Billing` | `config.Billing` | Billing trigger config. |

#### `ClientOptions`

- Signature: `type ClientOptions struct`
- Package: `internal/agentmode`
- Purpose: HTTP client construction options for controller requests.
- Parameters: None.
- Returns: None.
- Usage and constraints: Empty timeout defaults to 15 seconds.
- Relationships: Consumed by `NewClient`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Timeout` | `time.Duration` | Request timeout. |
| `InsecureSkipVerify` | `bool` | TLS verification override. |
| `UserAgent` | `string` | User-Agent header value. |
| `AgentID` | `string` | Optional `X-Agent-ID` header value. |

#### `Client`

- Signature: `type Client struct`
- Package: `internal/agentmode`
- Purpose: Authenticated controller client.
- Parameters: None.
- Returns: None.
- Usage and constraints: State is encapsulated; construct through `NewClient`.
- Relationships: Produces `agentproto.ClientListResponse` and accepts `agentproto.StatusReport`.

#### `BillingClient`

- Signature: `type BillingClient struct`
- Package: `internal/agentmode`
- Purpose: Minimal HTTP client for billing job trigger endpoints.
- Parameters: None.
- Returns: None.
- Usage and constraints: State is encapsulated; construct through `NewBillingClient`.
- Relationships: Used by billing scheduler loops started from `Run`.

#### `HTTPClientSource`

- Signature: `type HTTPClientSource struct`
- Package: `internal/agentmode`
- Purpose: Adapter from controller client to `xrayconfig.ClientSource`.
- Parameters: None.
- Returns: None.
- Usage and constraints: The constructor signature includes the package-private `*syncTracker`, so this exported type is still tightly coupled to `agentmode` internals.
- Relationships: Implements `xrayconfig.ClientSource`.

#### `Run`

- Signature: `func Run(ctx context.Context, opts Options) error`
- Package: `internal/agentmode`
- Purpose: Starts the full runtime control loop.
- Parameters:
  - `ctx`: cancellation boundary for the runtime.
  - `opts`: logger, agent config, Xray config, and billing config.
- Returns:
  - `error`: setup or fatal runtime error.
- Usage and constraints:
  - Requires `ctx` to be non-nil.
  - Requires `opts.Agent.ControllerURL` and `opts.Agent.APIToken`.
  - Normalizes empty intervals and timeouts.
  - Creates one periodic syncer per resolved Xray target.
- Relationships:
  - Calls `NewBillingClient`, `NewClient`, and `NewHTTPClientSource`.
  - Constructs `xrayconfig.Generator` and `xrayconfig.PeriodicSyncer`.
  - Emits `agentproto.StatusReport` through `Client.ReportStatus`.

#### `NewClient`

- Signature: `func NewClient(baseURL, token string, opts ClientOptions) (*Client, error)`
- Package: `internal/agentmode`
- Purpose: Builds the authenticated controller HTTP client.
- Parameters:
  - `baseURL`: controller base URL.
  - `token`: shared service token.
  - `opts`: timeout, TLS, User-Agent, and optional agent ID header settings.
- Returns:
  - `*Client`: initialized controller client.
  - `error`: URL parsing, missing input, or setup error.
- Usage and constraints:
  - Rejects empty base URL and token.
  - If `InsecureSkipVerify` is true, clones the transport and relaxes TLS validation.
- Relationships: Returned client is used by `NewHTTPClientSource`, `ListClients`, and `ReportStatus`.

#### `(*Client) ListClients`

- Signature: `func (c *Client) ListClients(ctx context.Context) (agentproto.ClientListResponse, error)`
- Package: `internal/agentmode`
- Purpose: Fetches the active Xray client set from the controller.
- Parameters:
  - `ctx`: request-scoped context.
- Returns:
  - `agentproto.ClientListResponse`: controller payload including clients, total, and revision.
  - `error`: network, status-code, or decode error.
- Usage and constraints:
  - Sends authentication headers through the internal header helper.
  - Accepts only `200 OK` as success.
  - Treats `404 Not Found` as an unavailable endpoint and keeps the retry/fallback pattern open, even though the current path list has one item.
- Relationships: Wrapped by `HTTPClientSource.ListClients`.

#### `(*Client) ReportStatus`

- Signature: `func (c *Client) ReportStatus(ctx context.Context, report agentproto.StatusReport) error`
- Package: `internal/agentmode`
- Purpose: Sends runtime status back to the controller.
- Parameters:
  - `ctx`: request-scoped context.
  - `report`: status payload built by the runtime.
- Returns:
  - `error`: encode, network, or non-2xx response error.
- Usage and constraints:
  - Marshals the payload as JSON.
  - Treats any `2xx` response as success.
- Relationships: Called by the status reporter started inside `Run`.

#### `NewBillingClient`

- Signature: `func NewBillingClient(baseURL string, timeout time.Duration) (*BillingClient, error)`
- Package: `internal/agentmode`
- Purpose: Builds the billing trigger HTTP client.
- Parameters:
  - `baseURL`: billing service base URL.
  - `timeout`: request timeout; defaults to 15 seconds when empty or non-positive.
- Returns:
  - `*BillingClient`: initialized client.
  - `error`: empty or invalid URL.
- Usage and constraints: Only constructed when billing is enabled.
- Relationships: Used by `Run` and billing scheduler loops.

#### `(*BillingClient) TriggerCollectAndRate`

- Signature: `func (c *BillingClient) TriggerCollectAndRate(ctx context.Context) error`
- Package: `internal/agentmode`
- Purpose: Triggers the billing collect-and-rate job.
- Parameters:
  - `ctx`: request-scoped context.
- Returns:
  - `error`: request or non-2xx response error.
- Usage and constraints: Sends `POST /v1/jobs/collect-and-rate`.
- Relationships: Invoked by the collect billing loop.

#### `(*BillingClient) TriggerReconcile`

- Signature: `func (c *BillingClient) TriggerReconcile(ctx context.Context) error`
- Package: `internal/agentmode`
- Purpose: Triggers the billing reconcile job.
- Parameters:
  - `ctx`: request-scoped context.
- Returns:
  - `error`: request or non-2xx response error.
- Usage and constraints: Sends `POST /v1/jobs/reconcile`.
- Relationships: Invoked by the reconcile billing loop.

#### `NewHTTPClientSource`

- Signature: `func NewHTTPClientSource(client *Client, tracker *syncTracker) *HTTPClientSource`
- Package: `internal/agentmode`
- Purpose: Creates a `ClientSource` adapter backed by the controller client.
- Parameters:
  - `client`: controller client used for user fetches.
  - `tracker`: package-private sync tracker used to record fetch metadata.
- Returns:
  - `*HTTPClientSource`: source adapter.
- Usage and constraints:
  - Although exported, the `tracker` parameter uses the private `syncTracker` type, so this constructor is effectively intended for package-local orchestration.
- Relationships: Returned type implements `xrayconfig.ClientSource`.

#### `(*HTTPClientSource) ListClients`

- Signature: `func (s *HTTPClientSource) ListClients(ctx context.Context) ([]xrayconfig.Client, error)`
- Package: `internal/agentmode`
- Purpose: Adapts controller client responses to the Xray syncer input shape.
- Parameters:
  - `ctx`: request-scoped context.
- Returns:
  - `[]xrayconfig.Client`: client slice for config generation.
  - `error`: controller request error.
- Usage and constraints: Updates the sync tracker when present.
- Relationships: Satisfies `xrayconfig.ClientSource`.

### Package `internal/xrayconfig`

This package owns Xray config rendering and the periodic sync loop.

#### `DefaultFlow`

- Signature: `const DefaultFlow = "xtls-rprx-vision"`
- Package: `internal/xrayconfig`
- Purpose: Default VLESS flow applied when no explicit flow is provided.
- Parameters: None.
- Returns: None.
- Usage and constraints: Used while building rendered client entries for non-`xhttp` paths.
- Relationships: Referenced during `Generator.Render`.

#### `Client`

- Signature: `type Client struct`
- Package: `internal/xrayconfig`
- Purpose: Minimal Xray client record used during config rendering.
- Parameters: None.
- Returns: None.
- Usage and constraints:
  - `ID` is required.
  - `Email` is required by current rendering logic because it is used as the Xray stats key.
- Relationships: Returned by `ClientSource`; embedded inside `agentproto.ClientListResponse`.

| Field | Type | Meaning |
| --- | --- | --- |
| `ID` | `string` | Client UUID. |
| `Email` | `string` | Xray stats key and client label. |
| `Flow` | `string` | Optional explicit VLESS flow override. |

#### `Generator`

- Signature: `type Generator struct`
- Package: `internal/xrayconfig`
- Purpose: Renders and writes one Xray config file.
- Parameters: None.
- Returns: None.
- Usage and constraints: `OutputPath` is required. `Definition` falls back to `DefaultDefinition()` when nil.
- Relationships: Used by `PeriodicSyncer`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Definition` | `Definition` | Base config definition provider. |
| `OutputPath` | `string` | Destination file path. |
| `FileMode` | `fs.FileMode` | Output file mode; defaults to `0644`. |
| `Domain` | `string` | Hostname injected into template rendering. |

#### `Definition`

- Signature: `type Definition interface`
- Package: `internal/xrayconfig`
- Purpose: Contract for providing a fresh mutable Xray config base document.
- Parameters: None.
- Returns: None.
- Usage and constraints: Each call to `Base` must return a fresh copy so later mutation is safe.
- Relationships: Implemented by `JSONDefinition`; consumed by `Generator`.

#### `JSONDefinition`

- Signature: `type JSONDefinition struct`
- Package: `internal/xrayconfig`
- Purpose: JSON-backed `Definition` implementation.
- Parameters: None.
- Returns: None.
- Usage and constraints: Stores raw JSON bytes and decodes them on each `Base` call.
- Relationships: Returned by `DefaultDefinition`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Raw` | `[]byte` | Raw JSON document kept as the definition source. |

#### `ClientSource`

- Signature: `type ClientSource interface`
- Package: `internal/xrayconfig`
- Purpose: Contract for obtaining the active client list for sync work.
- Parameters: None.
- Returns: None.
- Usage and constraints: Must return the full active client set for one sync pass.
- Relationships: Implemented by `agentmode.HTTPClientSource`; consumed by `PeriodicSyncer`.

#### `PeriodicOptions`

- Signature: `type PeriodicOptions struct`
- Package: `internal/xrayconfig`
- Purpose: Constructor options for `PeriodicSyncer`.
- Parameters: None.
- Returns: None.
- Usage and constraints:
  - `Source`, `Generator.OutputPath`, and positive `Interval` are required.
  - `Runner` is optional and defaults to a shell command runner based on `exec.CommandContext`.
- Relationships: Consumed by `NewPeriodicSyncer`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Logger` | `*slog.Logger` | Logger for sync activity. |
| `Interval` | `time.Duration` | Sync period. |
| `Source` | `ClientSource` | Client provider for each sync pass. |
| `Generator` | `Generator` | Config renderer/writer. |
| `ValidateCommand` | `[]string` | Optional validation command. |
| `RestartCommand` | `[]string` | Optional restart command. |
| `Runner` | `commandRunner` | Optional command executor override. |
| `OnSync` | `func(SyncResult)` | Optional callback invoked after each sync attempt. |

#### `PeriodicSyncer`

- Signature: `type PeriodicSyncer struct`
- Package: `internal/xrayconfig`
- Purpose: Event-driven client reconciliation with a maximum 30-second full-state fallback; pure additions use HandlerService, while withdrawals restart the affected Xray target to close established sessions.
- Parameters: None.
- Returns: None.
- Usage and constraints: Internal state is encapsulated; create with `NewPeriodicSyncer`.
- Relationships: Coordinates `ClientSource`, `Generator`, and shell commands.

#### `SyncResult`

- Signature: `type SyncResult struct`
- Package: `internal/xrayconfig`
- Purpose: Result payload emitted after each sync attempt.
- Parameters: None.
- Returns: None.
- Usage and constraints: `Error` is nil on success.
- Relationships: Passed to the optional `OnSync` callback.

| Field | Type | Meaning |
| --- | --- | --- |
| `Clients` | `int` | Number of clients processed. |
| `Error` | `error` | Sync error when the attempt failed. |
| `CompletedAt` | `time.Time` | Completion timestamp in UTC. |

#### `DefaultDefinition`

- Signature: `func DefaultDefinition() Definition`
- Package: `internal/xrayconfig`
- Purpose: Returns the built-in default Xray config template.
- Parameters: None.
- Returns:
  - `Definition`: JSON-backed built-in template.
- Usage and constraints: Used automatically when `Generator.Definition` is nil.
- Relationships: Returns `JSONDefinition`.

#### `JSONDefinition.Base`

- Signature: `func (d JSONDefinition) Base() (map[string]interface{}, error)`
- Package: `internal/xrayconfig`
- Purpose: Decodes the raw JSON definition into a fresh mutable map.
- Parameters: None.
- Returns:
  - `map[string]interface{}`: deep copy of the base config tree.
  - `error`: JSON decode error.
- Usage and constraints: The returned map is safe for caller mutation.
- Relationships: Satisfies `Definition`.

#### `Generator.Generate`

- Signature: `func (g Generator) Generate(clients []Client) error`
- Package: `internal/xrayconfig`
- Purpose: Renders the config and atomically writes it to disk.
- Parameters:
  - `clients`: active client list for the config.
- Returns:
  - `error`: render or file-write error.
- Usage and constraints:
  - Requires `OutputPath`.
  - Uses `Render` and then writes atomically.
  - Defaults file mode to `0644`.
- Relationships: Called by `PeriodicSyncer`.

#### `Generator.Render`

- Signature: `func (g Generator) Render(clients []Client) ([]byte, error)`
- Package: `internal/xrayconfig`
- Purpose: Builds the final JSON config in memory without writing it.
- Parameters:
  - `clients`: active client list for the config.
- Returns:
  - `[]byte`: indented JSON ending with a newline.
  - `error`: template, decode, validation, or structural update error.
- Usage and constraints:
  - Requires each client to have `ID`.
  - Requires `Email` for current stats-aware rendering behavior.
  - Applies both text-template interpolation and structural client-list updates.
- Relationships: Called by `Generate`.

#### `NewPeriodicSyncer`

- Signature: `func NewPeriodicSyncer(opts PeriodicOptions) (*PeriodicSyncer, error)`
- Package: `internal/xrayconfig`
- Purpose: Validates options and constructs a periodic syncer.
- Parameters:
  - `opts`: syncer configuration bundle.
- Returns:
  - `*PeriodicSyncer`: initialized syncer.
  - `error`: missing required dependencies or invalid interval/output path.
- Usage and constraints: Rejects nil `Source`, empty `Generator.OutputPath`, and non-positive `Interval`.
- Relationships: Called from `agentmode.Run`.

#### `(*PeriodicSyncer) Start`

- Signature: `func (s *PeriodicSyncer) Start(ctx context.Context) (func(context.Context) error, error)`
- Package: `internal/xrayconfig`
- Purpose: Starts the background sync loop and returns a stop function.
- Parameters:
  - `ctx`: lifetime context for the loop.
- Returns:
  - `func(context.Context) error`: stop function that cancels the loop and waits for shutdown.
  - `error`: nil syncer or startup error.
- Usage and constraints:
  - The stop function accepts its own wait context.
  - The first sync attempt runs immediately before the ticker loop settles into periodic execution.
- Relationships: Managed by `agentmode.Run`.

### Package `internal/agentproto`

This package defines controller-facing payload types.

#### `ClientListResponse`

- Signature: `type ClientListResponse struct`
- Package: `internal/agentproto`
- Purpose: Response payload returned by the controller when the agent asks for active clients.
- Parameters: None.
- Returns: None.
- Usage and constraints: Current `agentmode.Client.ListClients` expects JSON in this shape.
- Relationships: Contains `[]xrayconfig.Client`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Clients` | `[]xrayconfig.Client` | Active Xray client list. |
| `Total` | `int` | Controller-side total count. |
| `GeneratedAt` | `time.Time` | Controller generation timestamp. |
| `Revision` | `string` | Optional controller revision marker. |

#### `StatusReport`

- Signature: `type StatusReport struct`
- Package: `internal/agentproto`
- Purpose: Runtime status payload reported by the agent to the controller.
- Parameters: None.
- Returns: None.
- Usage and constraints: Built inside `agentmode` from sync tracker state and agent config.
- Relationships: Contains `XrayStatus`.

| Field | Type | Meaning |
| --- | --- | --- |
| `AgentID` | `string` | Self-reported agent identity. |
| `Healthy` | `bool` | High-level node health signal. |
| `Message` | `string` | Optional health or error detail. |
| `Users` | `int` | Number of active users seen in the latest sync state. |
| `SyncRevision` | `string` | Optional revision marker from the latest fetch. |
| `Xray` | `XrayStatus` | Nested Xray runtime status. |

#### `XrayStatus`

- Signature: `type XrayStatus struct`
- Package: `internal/agentproto`
- Purpose: Nested status object describing the managed Xray instance.
- Parameters: None.
- Returns: None.
- Usage and constraints: `LastSync` is optional.
- Relationships: Embedded in `StatusReport`.

| Field | Type | Meaning |
| --- | --- | --- |
| `Running` | `bool` | Whether Xray is considered recently synced and therefore running from the agent's point of view. |
| `Clients` | `int` | Active client count. |
| `LastSync` | `*time.Time` | Timestamp of the last successful sync. |
| `ConfigHash` | `string` | Optional config hash field; currently not populated by the runtime. |
| `NodeID` | `string` | Optional node identifier. |
| `Region` | `string` | Optional region label. |
| `LineCode` | `string` | Optional line label. |
| `PricingGroup` | `string` | Optional pricing group label. |
| `StatsEnabled` | `bool` | Whether stats are expected to be available. |
| `XrayRevision` | `string` | Optional revision marker mirrored from the latest sync revision. |

## HTTP Contracts

This section documents external HTTP interfaces. These are not Go interfaces.

### Controller contracts

#### `GET /api/agent-server/v1/users`

- Caller: `agentmode.(*Client).ListClients`
- Auth headers:
  - `Authorization: Bearer <token>`
  - `X-Service-Token: <token>`
  - `X-Agent-ID: <agentID>` when configured
  - `User-Agent: <userAgent>`
- Success response: `200 OK`
- Response body: `agentproto.ClientListResponse`
- Failure handling:
  - Non-`200` responses become errors
  - `404` is treated as endpoint unavailable in the current retry/fallback pattern

#### `POST /api/agent-server/v1/status`

- Caller: `agentmode.(*Client).ReportStatus`
- Auth headers:
  - `Authorization: Bearer <token>`
  - `X-Service-Token: <token>`
  - `X-Agent-ID: <agentID>` when configured
  - `User-Agent: <userAgent>`
- Request body: `agentproto.StatusReport`
- Success response: any `2xx`
- Failure handling:
  - Non-`2xx` responses become errors
  - `404` is treated as endpoint unavailable in the current retry/fallback pattern

### Billing contracts

These endpoints are operational triggers. They are not treated as the billing source of truth.

#### `POST /v1/jobs/collect-and-rate`

- Caller: `agentmode.(*BillingClient).TriggerCollectAndRate`
- Request body: none
- Success response: any `2xx`
- Failure handling: non-`2xx` responses return an error with the response body snippet

#### `POST /v1/jobs/reconcile`

- Caller: `agentmode.(*BillingClient).TriggerReconcile`
- Request body: none
- Success response: any `2xx`
- Failure handling: non-`2xx` responses return an error with the response body snippet

## Notes on Compatibility Fields

The current code still carries one compatibility bridge in config shape:

- `config.XraySync.Targets` is the current multi-target path.
- `config.XraySync.OutputPath`
- `config.XraySync.TemplatePath`
- `config.XraySync.ValidateCommand`
- `config.XraySync.RestartCommand`

When `Targets` is empty, `agentmode.Run` converts those legacy single-target fields into one synthetic `config.SyncTarget`.

This page documents that behavior as current state only. It does not extend or strengthen the compatibility contract beyond what the code already does.

## Source Index

| Source file | Main exported symbols |
| --- | --- |
| [`cmd/agent/main.go`](../../cmd/agent/main.go) | runtime entrypoint |
| [`internal/config/config.go`](../../internal/config/config.go) | `Config`, `Log`, `Agent`, `TLS`, `Xray`, `Billing`, `XraySync`, `SyncTarget`, `Load`, `LoadReader` |
| [`internal/agentmode/runner.go`](../../internal/agentmode/runner.go) | `Options`, `Run` |
| [`internal/agentmode/client.go`](../../internal/agentmode/client.go) | `ClientOptions`, `Client`, `NewClient`, `(*Client).ListClients`, `(*Client).ReportStatus` |
| [`internal/agentmode/source_http.go`](../../internal/agentmode/source_http.go) | `HTTPClientSource`, `NewHTTPClientSource`, `(*HTTPClientSource).ListClients` |
| [`internal/agentmode/billing_client.go`](../../internal/agentmode/billing_client.go) | `BillingClient`, `NewBillingClient`, `(*BillingClient).TriggerCollectAndRate`, `(*BillingClient).TriggerReconcile` |
| [`internal/xrayconfig/generator.go`](../../internal/xrayconfig/generator.go) | `Client`, `Generator`, `(*Generator).Generate`, `(*Generator).Render` |
| [`internal/xrayconfig/definition.go`](../../internal/xrayconfig/definition.go) | `Definition`, `JSONDefinition`, `Base` |
| [`internal/xrayconfig/templates.go`](../../internal/xrayconfig/templates.go) | `DefaultDefinition`, `DefaultFlow` |
| [`internal/xrayconfig/syncer.go`](../../internal/xrayconfig/syncer.go) | `ClientSource`, `PeriodicOptions`, `PeriodicSyncer`, `SyncResult`, `NewPeriodicSyncer`, `(*PeriodicSyncer).Start` |
| [`internal/agentproto/types.go`](../../internal/agentproto/types.go) | `ClientListResponse`, `StatusReport`, `XrayStatus` |
