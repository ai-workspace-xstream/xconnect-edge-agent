# XConnect Edge Agent 🚀

<p align="center">
  <strong>只要一个域名 + 一台 VPS，一键部署高性能、全自动证书的 AI 加速节点</strong>
</p>

<p align="center">
  <a href="https://console.svc.plus/products/xconnect"><img src="https://img.shields.io/badge/XConnect-Live%20Console-2563EB?style=flat-square" alt="XConnect Console" /></a>
  <a href="https://github.com/ai-workspace-xstream/xconnect-app/releases/tag/main-149"><img src="https://img.shields.io/badge/Client%20App-v85%25%20Preview-10B981?style=flat-square" alt="Client App 85% Preview" /></a>
  <img src="https://img.shields.io/badge/Protocol-XHTTP%20%7C%20TCP%20Vision-8B5CF6?style=flat-square" alt="Protocols" />
  <img src="https://img.shields.io/badge/Arch-AMD64%20%7C%20ARM64-F59E0B?style=flat-square" alt="Architectures" />
</p>

---

## 📑 目录

- [项目定位](#-项目定位)
- [核心能力](#-核心能力)
- [部署模式](#-部署模式)
- [快速开始](#-快速开始)
- [相关项目与资源](#-相关项目与资源)
- [扩展阅读：高级部署与运维](#-扩展阅读高级部署与运维)

## 📡 项目定位

XConnect Edge Agent 是运行在代理节点上的控制代理：向 [accounts](https://github.com/ai-workspace-services/accounts) 认证，接收用户和节点配置，在本机管理 Xray，并上报心跳与健康状态。Caddy 负责 HTTPS/TLS 和入口反向代理。

它不保存账号数据库、不提供计费真相源，也不替代监控系统；节点凭据和运行时密钥必须由 Vault 或其他 Secret Manager 注入。

## 🌟 核心能力

- 单命令安装或升级 Xray、Caddy 和 Edge Agent；支持 AMD64/ARM64。
- 自动申请和续期 HTTPS 证书，支持 XHTTP 与 TCP Vision。
- 可完全离线运行，也可接入云端或私有 `accounts` 控制面。
- 可选安装 Xray/Node/Process/Blackbox/Vector 监控组件。

## 🧩 部署模式

| 模式 | 拓扑 | 适用场景 |
| --- | --- | --- |
| 完全单节点自建 | Proxy Server + 本地控制组件 | 个人或离线环境 |
| 共享版 Proxy Server | 单节点 + 共享 Full 控制端 | 多节点共享控制面 |
| 团队版 Proxy Server | 单节点 + Full 控制端 + 团队/租户隔离 | 企业团队和专属节点 |

Proxy Server 与 Full 控制端是两个独立部署目标。区域、节点池、控制端地址和团队范围由环境配置提供；凭据只在运行时注入，不写入脚本、README 或 Git。

## 🚀 快速开始

### 1. 准备域名

将节点域名的 **A 记录** 指向 VPS 公网 IP，并放行 `80/443`。在 Debian/Ubuntu VPS 上以 root 执行安装命令。

### 2. 独立节点

```bash
curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \
  bash -s -- --node xhttp.example.com --standalone
```

### 3. 托管、共享或团队节点

先由 Vault/Secret Manager 注入以下环境变量，再执行安装；不要把真实值放进命令行历史：

```bash
export AGENT_PROXY_DOMAIN="xhttp.example.com"
export AUTH_URL="https://accounts.example.com"
export AGENT_REGION="jp"
export AGENT_POOL="jp-main"
export INTERNAL_SERVICE_TOKEN="<runtime-secret>"

curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \
  bash -s -- --node "$AGENT_PROXY_DOMAIN" --with-observability

unset INTERNAL_SERVICE_TOKEN
```

`--with-observability` 会在当前节点安装监控组件；它不替代控制端部署。团队版节点应使用专属节点凭据和对应团队范围，不能回退到公共节点。

### 4. 验证和导入客户端

```bash
curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/verify-log-tune.sh | sudo bash
```

脚本只读检查服务、日志、磁盘、内存和代理连通性，并输出 VLESS 导入链接。客户端可使用 [XConnect App](https://github.com/ai-workspace-xstream/xconnect-app/releases) 或兼容 VLESS 的 v2rayN、v2rayNG、Sing-box、Surge 等。

> 运行成功只代表节点本地服务可用；托管节点还需在 Accounts/Full 控制端确认节点心跳、团队授权和配置同步。

## 🔗 相关项目与资源

- [XConnect Console](https://console.svc.plus/products/xconnect)
- [XConnect App](https://github.com/ai-workspace-xstream/xconnect-app)
- [Accounts 控制面](https://github.com/ai-workspace-services/accounts)
- [Billing Service](https://github.com/ai-workspace-services/billing-service)
- [Content Service](https://github.com/ai-workspace-services/content-service)
- [Edge Gateway](https://github.com/ai-workspace-services/edge-gateway)
- [GitOps 拓扑声明](https://github.com/ai-workspace-infra/gitops)
- [Playbooks 主机部署](https://github.com/ai-workspace-infra/playbooks)
- [Platform Ops Toolkit 流水线](https://github.com/ai-workspace-infra/platform-ops-toolkit)
- [架构与设计](docs/design.md)

---

<details>
<summary>📚 扩展阅读：高级部署与运维</summary>

以下内容面向平台运维和私有化部署场景；普通用户只需完成上面的快速开始。

高级章节索引：

- [Accounts 服务对接部署](#-与-accounts-服务对接部署)
- [常用进阶参数与配置](#️-常用进阶参数与配置)
- [边缘与容器化部署](#️-边缘与容器化部署cloudflare--cloud-run)
- [常见问题排查](#-常见问题排查faq)

---

## 🔗 与 accounts 服务对接部署

### 1. 对接关系

`accounts` 是控制面，`xconnect-edge-agent` 是节点侧运行时。Agent 不直接访问
accounts 数据库，也不负责创建账号；它只通过 HTTPS 调用 accounts 的 Agent API：

| Agent 请求 | 用途 | 成功响应 |
| :--- | :--- | :--- |
| `GET /api/agent-server/v1/users` | 拉取当前允许接入 Xray 的用户及 UUID | `200`，返回 `clients`、`total` |
| `GET /api/agent-server/v1/users/events` | 监听用户配置版本变化，触发即时同步 | SSE 长连接 |
| `POST /api/agent-server/v1/status` | 上报心跳、健康状态、同步版本和 Xray 状态 | `204` |
| `GET /healthz` | 检查 accounts 服务是否可访问 | `200` |

每次 Agent API 请求都会携带以下请求头：

```text
Authorization: Bearer <INTERNAL_SERVICE_TOKEN>
X-Service-Token: <INTERNAL_SERVICE_TOKEN>
X-Agent-ID: <唯一节点 ID>
```

其中 `X-Agent-ID` 通常使用节点域名，例如 `hk-xhttp.example.com`。同一个 token
可以供多个节点使用，但每个节点的 `agent.id` 必须唯一且保持稳定，这样 accounts
才能分别记录节点状态。

### 2. 先部署 accounts 控制面

在 accounts 服务侧准备以下配置：

1. 使用 `server-agent` 运行模式，确保对外暴露 `/api/agent-server/v1/*`。
2. 为服务配置 HTTPS 公网地址，例如 `https://accounts.example.com`；`AUTH_URL`
   只填写域名根地址，不要把 `/api/agent-server/v1` 拼进去。
3. 在 accounts 的运行时密钥管理中设置 `INTERNAL_SERVICE_TOKEN`。该值不能提交到
   Git，也不要写入公开文档。
4. accounts 使用 PostgreSQL 持久化用户、节点和订阅状态；Agent 节点只需要能够
   访问 accounts 的 HTTPS 端口，不需要访问 accounts 数据库。

accounts 仓库提供 VPS、Docker 和 Cloud Run 三种部署路径。以 Docker 或 Cloud Run
为例，先完成 accounts 部署并记下服务 URL；以 VPS 方式部署时，Caddy 通常监听
`80/443` 并反向代理到 accounts 的 `:8080`：

```bash
# accounts 仓库
curl -fsSL "https://raw.githubusercontent.com/ai-workspace-services/accounts/main/scripts/setup.sh?$(date +%s)" \\
  | bash -s -- accounts.example.com --mode docker --deploy
```

如果 accounts 侧显式配置 Agent 凭据，可在其配置中使用与 Agent 相同的 token：

```yaml
agents:
  credentials:
    - id: "edge-node-hk-xhttp"
      name: "Hong Kong XHTTP node"
      token: "<与 INTERNAL_SERVICE_TOKEN 相同的值>"
      groups:
        - "default"
```

注意：accounts 启动时如果 `agents.credentials` 非空，会优先使用这组凭据；此时
`INTERNAL_SERVICE_TOKEN` 只作为其他内部服务的密钥，不会自动替换上面的
`token`。如果希望使用共享 token 兜底，请不要配置 `agents.credentials`，仅在
accounts 运行时注入 `INTERNAL_SERVICE_TOKEN`；生产环境仍建议通过 Vault/Secret
Manager 注入，不要把明文 token 提交到配置文件。

多节点场景建议使用一个专用的 Agent token，并通过每个节点的 `X-Agent-ID` 区分节点；
不要为每台机器复制数据库凭据或授予数据库访问权限。

### 3. 一键部署并接入 Agent 节点（推荐）

确认 accounts 已可访问后，在每台代理节点上执行。域名和服务地址通过当前
Shell 环境传入；不要把真实域名、token 或 Vault 配置固定写进脚本或仓库：

```bash
export AGENT_PROXY_DOMAIN="<本节点的代理域名>"
export AGENT_REGION="<部署区域代码，例如 hk 或 jpn-tky>"
export AGENT_POOL="<区域内的 pool 标识，例如 hk-main>"
export AUTH_URL="https://accounts.example.com"
export BILLING_SERVICE_URL="https://billing.example.com"

# 供同一 Shell 会话中的 Vault Agent 配置使用；不要写入脚本或仓库
export VAULT_ADDR="https://vault.example.com"
export VAULT_TLS_SECRET_PATH="<KV v2 data path>"

read -rsp "Accounts Agent token: " INTERNAL_SERVICE_TOKEN
echo
export INTERNAL_SERVICE_TOKEN
read -rsp "Vault server token: " VAULT_TOKEN
echo
export VAULT_TOKEN

curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \
  bash -s -- --node "$AGENT_PROXY_DOMAIN"

unset INTERNAL_SERVICE_TOKEN VAULT_TOKEN
```

如果需要脚本通过 Cloudflare API 更新 DNS，再从当前 Shell 提供
`CLOUDFLARE_ZONE_NAME` 和 `CLOUDFLARE_API_TOKEN`；没有设置 zone 时脚本会跳过
自动 DNS 更新。不要把 Cloudflare token 写入命令行参数、文档或仓库。

脚本会完成以下工作：

- 安装或更新 Xray、Caddy 和 `xconnect-edge-agent`；
- 生成 `/etc/agent/account-agent.yaml`，把 `agent.id` 写成 `--node` 的值；
- 将 `AGENT_REGION`、`AGENT_POOL` 写入 `agent.region`、`agent.pool`，供状态上报和区域入口汇总使用；未提供时保留节点已有值。
- 将 `AUTH_URL` 写入 `agent.controllerUrl`，将 `INTERNAL_SERVICE_TOKEN` 写入 `agent.apiToken`；
- 写入 `billing` 调度配置（`baseURL: "${BILLING_SERVICE_URL}"`）；
- 创建并启用 `xconnect-edge-agent.service`，同时配置 XHTTP/TCP 两套 Xray 同步目标。
- 当 `VAULT_ADDR`、`VAULT_TOKEN` 和 `VAULT_TLS_SECRET_PATH` 均已提供时，安装并启用
   Vault Agent，生成 `/etc/vault.d/agent.hcl`、`/etc/vault.d/token` 和
   `vault-agent-tls.service`，先完成证书同步再启动 Caddy。

bootstrap 完成后，节点上的 Vault Agent 常驻服务会使用同一组运行时配置自动同步
证书并 reload Caddy。`VAULT_TLS_SECRET_PATH` 应填写 Vault KV v2 的 data API
路径（例如 `kv/data/<运行时路径>`），不要把实际域名或路径固定在脚本中；
Vault token 只授予该 KV 路径的读取权限。


> **提示**：脚本兼容多种环境变量命名（如 `AUTH_URL`、`Accounts_AUTH_URL` 以及 `BILLING_SERVICE_URL`、`Billing-service_AUTH_URL`、`BILLING_BASE_URL` 等）。

### 4. 已安装节点修改 accounts / billing 地址或 token

重新执行上面的安装命令即可更新 Agent 配置；如果只执行 `--upgrade-only`，配置文件
不会被覆盖，适合只升级二进制的场景。也可以直接编辑配置文件 `/etc/agent/account-agent.yaml`：

```yaml
mode: "agent"

log:
  level: info

agent:
  id: "hk-xhttp.example.com"
  region: "hk"
  pool: "hk-main"
  controllerUrl: "https://accounts.example.com"
  apiToken: "<accounts 的 Agent token>"
  httpTimeout: 15s
  statusInterval: 1m
  syncInterval: 10m
  tls:
    insecureSkipVerify: false

billing:
  enabled: true
  baseURL: "https://billing.example.com"
  httpTimeout: 15s
  collectInterval: 1m
  reconcileInterval: 5m
```

`region` 是实际部署区域代码；`pool` 是该区域内的逻辑 pool 标识，同一 pool 的多个节点使用相同值。模板中的空值需要由部署环境或 inventory 提供，不能仅靠 `agent.id` 推断。Ansible 可设置 `agent_region`、`agent_pool`，容器可注入 `AGENT_REGION`、`AGENT_POOL`。

修改后重启并检查服务：

```bash
sudo systemctl restart xconnect-edge-agent
sudo systemctl is-active xconnect-edge-agent
sudo journalctl -u xconnect-edge-agent -n 100 --no-pager
```

### 5. 使用 Ansible 部署多台节点

仓库内的 Ansible 方案要求目标机器已经运行过 `scripts/setup-proxy.sh`，先完成
Xray、Caddy 和证书初始化，再由 Ansible 下发 Agent 二进制、配置和 systemd 服务。

```bash
cd deploy/ansible
export INTERNAL_SERVICE_TOKEN="<accounts 的 Agent token>"

# 生产 inventory；仅部署 Agent，不执行 Cloudflare DNS 更新
./deploy.sh --prod --deploy-only
```

自定义环境时，编辑 `inventory.ini` 和 `vars/xconnect_edge_agent.yml`，然后执行：

```bash
./deploy.sh --inventory inventory.ini --deploy-only
```

每台机器应在变量文件中设置不同的 `agent_id`；`agent_controller_url` 设置为同一个
accounts HTTPS 地址，token 统一从 `INTERNAL_SERVICE_TOKEN` 注入。

#### UAT Agent Server（shared / team）

UAT 的两台 Agent Server 使用独立 inventory，不依赖 Doco-CD，也不会消费 Web SaaS
的部署配置：

| edition | GCP VM | GCP labels | inventory | vars |
| :--- | :--- | :--- | :--- | :--- |
| shared | `agent-server-shared-uat` | `app=agent-server`, `edition=shared` | `inventory.uat-shared.ini` | `vars/xconnect_edge_agent.uat-shared.yml` |
| team | `agent-server-team-uat` | `app=agent-server`, `edition=team` | `inventory.uat-team.ini` | `vars/xconnect_edge_agent.uat-team.yml` |

先在每台节点完成 `scripts/setup-proxy.sh` 的 Xray、Caddy 和证书初始化，再从本目录
执行对应的 Ansible target。UAT target 不声明或修改 Cloudflare DNS；令牌只能在运行时
通过 Vault/受控环境注入，不能写入 inventory、vars、命令行或仓库：

```bash
cd deploy/ansible
# 由受控运行时注入 INTERNAL_SERVICE_TOKEN，不要写入 shell 历史
./deploy.sh --uat-shared --deploy-only
./deploy.sh --uat-team --deploy-only
```

两个命令必须分开执行；每个 target 只包含对应的一台 VM。部署前应确认该节点已有
`xray.service`、`xray-tcp.service`、`caddy.service` 和 `/etc/caddy/Caddyfile`。
部署后使用 `systemctl is-active xconnect-edge-agent`、`journalctl -u
xconnect-edge-agent`，以及带 `Authorization: Bearer` 和 `X-Service-Token` 的
`/api/agent-server/v1/users` 请求完成认证联调。若没有 UAT 节点域名或 token，停止在
前置检查，不要猜测域名、复用生产凭据或以 standalone 模式冒充托管 Agent 部署。

### 6. 对接完成后的验证

先验证 accounts 本身，再验证 Agent API 和节点服务：

```bash
export AUTH_URL="https://accounts.example.com"
export INTERNAL_SERVICE_TOKEN="<accounts 的 Agent token>"
export AGENT_ID="hk-xhttp.example.com"

# accounts 健康检查：预期 HTTP 200
curl -fsS "${AUTH_URL}/healthz"

# 拉取用户配置：预期 HTTP 200，并返回 clients/total
curl -fsS \\
  -H "Authorization: Bearer ${INTERNAL_SERVICE_TOKEN}" \\
  -H "X-Service-Token: ${INTERNAL_SERVICE_TOKEN}" \\
  -H "X-Agent-ID: ${AGENT_ID}" \\
  "${AUTH_URL}/api/agent-server/v1/users"

# 节点服务：预期 active
systemctl is-active xconnect-edge-agent
```

### 7. 常见故障

| 现象 | 常见原因 | 处理方式 |
| :--- | :--- | :--- |
| `401 Unauthorized` | Agent 与 accounts 的 token 不一致，或缺少 `Bearer` 前缀 | 对比 accounts 运行时的 `INTERNAL_SERVICE_TOKEN` 与 `/etc/agent/account-agent.yaml` 的 `apiToken` |
| `404 Not Found` | `AUTH_URL` 指向了错误服务，或 accounts 版本未包含 Agent API | 直接访问 `${AUTH_URL}/healthz` 和 `${AUTH_URL}/api/agent-server/v1/users`，确认路径未重复拼接 |
| `connection refused` / TLS 错误 | DNS、80/443、防火墙或 HTTPS 证书问题 | 从 Agent 节点执行 `curl -v ${AUTH_URL}/healthz`，先修复 accounts 公网访问 |
| 返回 `200` 但 `clients` 为空 | 用户未激活/未完成邮箱验证、被暂停，或没有可用 proxy UUID | 在 accounts 控制台检查用户状态；这不代表 Agent 鉴权失败 |
| `xconnect-edge-agent` 未启动 | 安装时没有同时提供 `AUTH_URL` 和 token | 补齐配置后执行 `sudo systemctl restart xconnect-edge-agent` |

---

## ⚙️ 常用进阶参数与配置

一键安装脚本 `scripts/setup-proxy.sh` 提供了丰富的可选参数：

```bash
# 1. 仅升级 Agent 与核心二进制（保留现有配置文件与证书不变）
curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \
  bash -s -- --upgrade-only

# 2. 与 postgresql.svc.plus 数据库同机部署（自动放行 5443/tcp 端口）
OPEN_STUNNEL_5443=true \
curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \
  bash -s -- --node xhttp.example.com --open-stunnel-5443

# 3. 搭配 Cloudflare API Token 自动配置 DNS 解析
CLOUDFLARE_API_TOKEN="your-cf-token" \
curl -fsSL https://raw.githubusercontent.com/ai-workspace-xstream/xconnect-edge-agent/main/scripts/setup-proxy.sh | \
  bash -s -- --node xhttp.example.com
```

---

## ☁️ 边缘与容器化部署（Cloudflare / Cloud Run）

对于需要无服务器（Serverless）或边缘计算的用户，本项目在 `deploy/` 下提供了开箱即用的支持：

### 1. Cloudflare Workers 边缘代理
位于 `deploy/cloudflare/workers`，支持快速部署边缘 API 转发：
```bash
make cf-worker-install
make cf-worker-deploy
```

### 2. Cloudflare Containers / Google Cloud Run
支持在单一容器内同时运行 Xray 与 Agent 守护进程：
```bash
make cf-containers-install
make cf-containers-deploy
```
详细指南请参考 [deploy/cloudflare/containers/README.md](deploy/cloudflare/containers/README.md)。

---

## ❓ 常见问题排查（FAQ）

<details>
<summary><strong>Q1: 脚本执行后提示证书申请失败？</strong></summary>

1. 请确认你的域名 A 记录已准确解析到当前 VPS 公网 IP。
2. 确认服务器防火墙已开放 `80` 和 `443` 端口（Caddy 通过 HTTP-01/TLS-ALPN-01 验证域名所有权）。
3. 如果 VPS 处于云厂商安全组内（如阿里云、腾讯云、AWS），请在云控制台安全组规则中放行入方向 `80` 与 `443` 端口。
</details>

<details>
<summary><strong>Q2: 如何查看服务运行状态与日志？</strong></summary>

```bash
# 查看 Caddy 状态与证书日志
systemctl status caddy
journalctl -u caddy -n 50 --no-pager

# 查看 Xray 服务状态
systemctl status xray
journalctl -u xray -n 50 --no-pager

# 查看 XConnect Edge Agent 控制服务（如已启用）
systemctl status xconnect-edge-agent
journalctl -u xconnect-edge-agent -n 50 --no-pager
```
</details>

<details>
<summary><strong>Q3: 为什么推荐使用 XHTTP 协议？</strong></summary>

XHTTP 基于 HTTP/3 与 HTTP/2 传输封装，在面对复杂的跨国网络抖动与封锁环境时，具有更高的抗丢包能力与更佳的多路复用性能，特别适合 AI 交互（如 Cursor 流式代码补全、Claude/ChatGPT 长上下文流式对话）等低延迟敏感型场景。
</details>

</details>

