# Gateway / One 统一注册与状态上报

仓库边界：edge-agent 负责 Gateway / One 注册、向 Zero 上报，以及未来 GPG 证书、UUID 和用户 Auth 统一管理；Gateway 负责核心 relay；One 负责边缘 WireGuard 和逐 peer 路径管理。本次不新增 GPG / Auth 实现，也不迁移现有身份凭据。

既有 Gateway / One role 注册流程继续保留，legacy agent-proxy 同步逻辑不接管这些节点。可选配置：

```yaml
agent:
  role: one
  # id / nodeId / networkId 使用本节点既有注册值
  overlayStatusPath: /var/lib/xconnect-one/runtime/overlay-status.json
```

Gateway 使用本机 `<gateway-state-dir>/runtime/overlay-status.json`。路径由部署配置指定，不按设备 ID 硬编码。留空时保持原报告行为。

Agent 只读绝对路径、非 symlink 的普通文件，最大 64 KiB；按类型白名单解析，验证 NodeID / NetworkID / Role、时间、TTL 和最多 256 条路径。过期、缺失或跨设备报告使状态 unhealthy；不控制切换、不探测端点、不读取 WireGuard 私钥。允许附带的字段只有能力、健康、路径类别、LAN endpoint、RTT、原因、收发计数及时间；未知字段不会转发。

本地测试：`go test -race ./...`，覆盖状态采集、绑定、过期和非敏感字段。Gateway / One 运行与 UAT 由对应仓库验证。
