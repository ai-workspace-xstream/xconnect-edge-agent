# 虚拟节点配置示例（待实现）

这些文件属于 [Draft v2 配置设计](../../virtual-nodes-configuration-design.md)，不是当前 Agent 已支持的运行配置。不要直接覆盖生产 `/etc/agent/account-agent.yaml`；旧版 Agent 可能忽略新字段。必须先实现并验证配置版本门禁、运行适配器与计量链路。

| 示例 | 说明 |
| --- | --- |
| [single-node.yaml](single-node.yaml) | enabled=false，低配主机单节点；兼容既有服务部署，不附带创建服务 |
| [multi-node.yaml](multi-node.yaml) | enabled=true，同一个 Agent 管理下面两个节点，共用 Caddy |
| [nodes/us-a/agent.yaml](nodes/us-a/agent.yaml) | Plus 节点业务配置 |
| [nodes/us-a/runtime.yaml](nodes/us-a/runtime.yaml) | A 的 Xray/systemd、入口、exporter 描述 |
| [nodes/us-a/xray-template.json](nodes/us-a/xray-template.json) | A 的独立 XHTTP 模板，clients 由同步生成 |
| [nodes/us-b/agent.yaml](nodes/us-b/agent.yaml) | Custom 节点业务配置，使用文档示例邮箱 |
| [nodes/us-b/runtime.yaml](nodes/us-b/runtime.yaml) | B 的独立运行描述 |
| [nodes/us-b/xray-template.json](nodes/us-b/xray-template.json) | B 的独立 XHTTP 模板 |

设计实现后，示例的布局对应：把 single-node.yaml 或 multi-node.yaml 选一份作为 `/etc/agent/account-agent.yaml`，nodes 目录放到 `/etc/agent/nodes`。此处只描述映射，不提供当前可执行安装命令。

| 节点 | 专属域名 | Xray XHTTP | Xray API/统计 | exporter 指标 |
| --- | --- | --- | --- | --- |
| us-a | us-a.example.com:443 | 127.0.0.1:12001 | 127.0.0.1:11001 | 127.0.0.1:19101 |
| us-b | us-b.example.com:443 | 127.0.0.1:12002 | 127.0.0.1:11002 | 127.0.0.1:19102 |

443 是共用 Caddy 入口；不同域名路由到不同后端。两个模板的 path 都是 `/split`，不从 plus/custom 推导路径。统计接口与代理后端只允许本机访问。

示例资源限制每 Xray 为一个 CPU 时间配额、512 MiB 内存；不预留独占 CPU，也不分配独占带宽。两个节点不会复制用户的套餐额度或拥有独立公网出口 IP。

未提供且必须由部署侧补齐的依赖：真实 Accounts 地址与节点授权、域名/DNS/TLS、固定 Xray 二进制及带 version/sha256 的 `/etc/agent/artifacts/xray.yaml`、运行时凭据文件、服务用户/目录权限、systemd Unit 和受限服务控制、共享 Caddy 配置、exporter/转发/Billing 的兼容实现与认证。不得填写虚构 checksum 或把当前全局 token 伪装成已隔离的节点身份。

单节点示例引用现有模板位置，不包含服务安装；空 validateCommand/restartCommand 表示必须按既有运行方式补齐，而非保证无需校验或重载。多节点示例则由拟议 runtime adapter 执行固定校验与重载，不接受任意命令。

本目录只有文档示例，不启动额外 Agent。Xray 模板保留空 clients 并启用用户统计策略；生成器必须写入稳定统计身份、实际客户端及对应 level。JSON/YAML 能解析不等于通过 Xray 校验、代理连接或计费验收。

Custom 示例 B 显式设置 `customAllowFree: true`，允许白名单内 Free 用户测试；默认关闭，仍检查账号启用、暂停和配额，不给用户升级套餐。此开关需要配套 Accounts/Agent，虚拟节点模式仍属于后续规划。

## 首批预检实现的边界

本任务首批改动增加以下无运行副作用入口：

```sh
xconnect-edge-agent -capabilities
xconnect-edge-agent -check-config -config single-node.yaml
```

`-check-config` 只校验主配置语法、字段和授权范围，不验证运行依赖或真实连接。
当前返回 `virtualNodeRuntime: false`，`multi-node.yaml` 必须失败退出，且不会读取
节点子文件。子文件/runtime/计量格式仍为设计草案；后续能力、固定制品和验收齐全后才可部署。
