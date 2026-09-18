# XConnect 边缘收敛与 ACK 闭环：开发规划（执行手册）

> **Status**: 🟡 规划已定稿，按第 2 节的迭代顺序执行（当前迭代：I-0）
> **Date**: 2026-09-18
> **分工**: 评审与规划由 Claude Code 负责，编码由 Gemini / Antigravity 负责
> **Related PRs**: 每个任务完成后填入第 2 节状态表
> **本仓视角任务卡**: `XConnect-Gateway/docs/tasks/2026-09-18-gateway-ack-interval.md`、`XConnect-One/docs/tasks/2026-09-18-one-sync-watch-ack.md`

本文是跨仓总规划。执行者只做本文列出的事项；遇到本文没有覆盖的决策，写进第 11 节“待决策”并停下来等回复，不要自行扩展范围。

---

## 0. 硬性规则（每个任务都适用）

1. **一个功能点就是一轮迭代**：PR → 合并 main → 发版 → UAT 部署 → 验证 Goal（见第 2 节）。PR 标题格式为 `[I<n>][<任务ID>] <说明>`。不在 `main` 上直接开发，不 force push `main`，不保留长期存在的 feat 分支。
2. **测试先行**。PR 的第一个 commit 只包含会失败的测试，第二个 commit 起才写实现。PR 描述里贴出“失败 → 通过”两次测试输出。
3. **锁定的契约测试不许改断言**。测试失败时改代码，不改测试的期望值；确实需要改契约的，先写进第 11 节等决策。
4. **完成声明必须附证据**：
   - 已合并：给出 PR 链接和 commit SHA。
   - 已删除：`git ls-files <路径>` 输出为空。
   - 测试通过：粘贴命令输出。
   - UAT 验证通过：附接口返回的 JSON。
   没有证据的“已完成”一律视为未完成。
5. **accounts 和 portal 以 GitHub `ai-workspace-services/{accounts,portal}` 的 `origin/main` 为准。**
   - 本地 `ai-workspace-xstream/{accounts,portal}` 已过期。
   - 本地 `ai-workspace-service/{accounts,portal}` 停在旧分支，还有别人未提交的修改。
   - 这些工作区都不要动，另外 clone 或用 `git worktree add` 新建干净工作区。
6. **accounts 部署**：UAT 和 prod 共用同一个 `gcloud run deploy` 脚本，部署前确认目标 service 和 project。UAT 返回 503 时，先排除冷启动：只有一个 instanceId，且启动日志晚于失败请求，就是冷启动。
7. **第 9 节“不做清单”里的东西不要重新引入。**
8. 每完成一个任务，更新第 2 节状态表。

---

## 1. 已确认的事实（规划依据）

accounts 的引用基于 `ai-workspace-services/accounts@2693498`（origin/main），portal 的引用基于 origin/main。

| # | 事实 | 代码位置 |
|---|---|---|
| F1 | 面板“配置同步”只统计 `connection_status == "recent_ack"` 的设备；为 0 时显示“暂无 ACK” | portal `src/modules/extensions/builtin/xconnect-zero/routes/xconnect-zero-node-management.tsx:483`、`:911` |
| F2 | `recent_ack` 的条件：`max(ack.received_at, device.last_seen_at)` 在 **5 分钟**内，且 ACK 属于网络当前的 `config_generation`，`config_id` 也要匹配 | accounts `internal/overlay/service.go:37`（`adminDeviceACKRecentWindow`）、`toAdminDevice` |
| F3 | 同一个 generation 重复 ACK 不报错，会刷新 `last_seen_at`，所以周期性 ACK 可以当心跳用 | accounts `internal/overlay/repository.go:396` |
| F4 | 每有设备加入，网络的 `config_generation` 就加 1，其它设备的旧 ACK 随即失效 | accounts `internal/overlay/service.go:651`、`repository.go:318` |
| F5 | 目前只有两个有效的 ACK 接口：`POST /api/overlay/v1/enrollment/signed-config/:generation/ack`（enrollment token）和 `POST /api/overlay/v1/signed-config/:generation/ack`（用户 token）。**不存在** `/api/overlay/v1/devices/{id}/ack`，也**不存在** `/api/overlay/v1/config/ack`；旧的 `/api/overlay/config/ack` 写的是面板不读的表 | accounts `api/overlay_v1.go:24,41`、`api/overlay.go:59` |
| F6 | overview 和 devices 只统计 `owner_user_id` 等于当前登录用户的网络 | accounts `internal/overlay/service.go:177` |
| F7 | Gateway 同步定时器 `OnUnitActiveSec=5min`，systemd 默认还有 `AccuracySec=1min`，同步周期不短于判定窗口 | `XConnect-Gateway/packaging/systemd/xconnect-gateway-sync.timer` |
| F8 | One 没有任何周期同步，只能手动执行 `xconnect sync` | `XConnect-One/cmd/xconnect/main.go:235`（`runSync`） |
| F9 | One 的旧 ACK 路径请求 `/api/overlay/v1/config/ack`，会返回 404 | `XConnect-One/overlay/controlplane/client.go:107`、`overlay/usecase/join.go:313` |
| F10 | Gateway 的签名配置强制 `transport.port == 443`，Xray 直接监听 `0.0.0.0:443` 并自带 TLS；proxy 节点上 443 由 Caddy 占用，proxy 的 Xray 走 `/dev/shm/xray.sock` | `XConnect-Gateway/internal/gateway/contract.go:86,153`；edge-agent `internal/xrayconfig/templates.go:13,19` |
| F11 | accounts 已经实现了配额暂停：配额耗尽、欠费或运营暂停后，用户不再出现在 `/api/agent-server/v1/users`，agent 撤掉凭据并重启 Xray，恢复后自动下发 | accounts `docs/api/overview.md:185` |
| F12 | proxy 节点上删除用户或修改凭据仍然走重启 Xray | edge-agent `internal/xrayconfig/syncer.go:216-242` |
| F13 | One 有一个未合并的协议修复 `7417a57 fix: accept formal Zero enrollment metadata`（分支 `fix/formal-exchange-response-v2`） | XConnect-One |
| F14 | One 的 `NOTICE` 在 main 工作区里是未跟踪文件，从来没有提交过；XConnect-Gateway 没有 LICENSE | XConnect-One、XConnect-Gateway |

---

## 2. 交付循环与迭代目标（本节是执行主线）

每个功能点就是一轮迭代。一轮必须走完这条链路：**PR → 合并 main → 发版 → UAT 部署 → UAT 验证 Goal**。当前一轮的 Goal 没有全部满足，不开始下一轮。

### 2.1 交付循环（每一轮都照做）

1. **拉短分支**：从最新的 `origin/main` 拉分支，命名为 `<迭代号>-<任务ID>-<slug>`，例如 `i1-a1-gateway-timer`。不再使用长期存在的 feat 分支。
2. **RED**：第一个 commit 只包含失败的测试，本地跑出失败输出并保存下来。
3. **GREEN**：写实现，本地全量测试通过（Go 仓库执行 `go test ./... && go vet ./...`，portal 执行仓库自带的测试命令）。
4. **PR**：标题格式为 `[I<n>][<任务ID>] <说明>`。CI 通过后，把 PR 链接交给 Claude Code 评审，评审通过后 squash 合并到 `main`。
5. **发版**：按 2.3 表格操作，并确认产物存在：`gh release view <tag> -R <repo>`，或者查看 pipeline run 的链接。
6. **UAT 部署**：按 2.3 表格操作。One 和 Gateway 一律先 `dry-run` 再 `apply`：
   ```bash
   gh workflow run xconnect-one-uat.yaml -R ai-workspace-infra/platform-ops-toolkit \
     -f mode=dry-run -f cli_release_tag=<One 标签> -f gateway_release_tag=<Gateway 标签>
   gh run list -R ai-workspace-infra/platform-ops-toolkit --workflow xconnect-one-uat.yaml -L 1
   gh run watch -R ai-workspace-infra/platform-ops-toolkit <run-id>
   # dry-run 通过后，把 mode 换成 apply 再执行一次
   ```
   Gateway 所在的 tw-xconnect 同时承载 proxy 流量，**每次对 gateway 执行 apply 前，都要先得到用户确认**。
7. **UAT 验证**：逐条检查本轮 Goal，证据保存到 `docs/tasks/evidence/I<n>-*.log` 或截图，不能包含 token 等凭据。设备状态的标准采样命令如下：
   ```bash
   for i in $(seq 1 7); do
     date -u +%FT%TZ
     curl -fsS -H "Authorization: Bearer $ADMIN_TOKEN" "$ACCOUNTS_UAT/api/overlay/v1/admin/devices" \
       | jq -c '[(.devices? // .)[] | {role, name, connection_status, last_seen_at}]'
     sleep 300
   done | tee docs/tasks/evidence/I<n>-devices.log
   ```
8. **记录**：更新 2.2 和 2.5 的状态，按第 10 节格式汇报。
9. **判定**：Goal 全部满足才进入下一轮。有任何一项不满足，就先修复（修复也是一轮完整循环）或回滚（见 2.4）。
10. **不碰生产**：
    - accounts 和 portal 的 `v*` 标签、`release/**` 分支、`prod-release-*` 标签会触发 **PROD** 部署，不要创建。
    - daily-build 标签由跨仓每日快照自动生成，不要手动打。
    - 任何生产发布都需要用户明确批准。

### 2.2 迭代计划：每一轮的 Goal

Goal 必须能在 UAT 上观察到。“UAT 30 分钟常绿”指按 2.1 第 7 步采样 7 次，目标设备 7 次都是 `recent_ack`。

**M1：面板常绿（阶段 A）**

| 迭代 | 任务 | Goal（UAT 可验证） | 发版 | UAT 部署 | 状态 / 证据 |
|---|---|---|---|---|---|
| I-0 | T0-1 | edge-agent 的规划文档已合并进 main，原型已归档到 `wip/multicall-prototype` | 无 | 无 | ⬜ |
| I-1 | A1 | ① `gw-uat-tw-xconnect` 30 分钟常绿<br>② `journalctl -u xconnect-gateway-sync.service` 显示每 60±5 秒一次 sync，且 ACK 成功 | Gateway `v0.1.9` | toolkit workflow `gateway_release_tag=v0.1.9`。如果这个 workflow 不会升级 tw-xconnect 上的 gateway 或 timer，就在 playbooks 仓库配套提 PR，并把新 SHA 通过 `playbooks_ref` 传入；实在不行，经用户批准后按 A1 的 drop-in 手动加覆盖 | ⬜ |
| I-2 | A4 | ① accounts main 包含 overlay v1 路由 golden<br>② UAT 部署前后 `GET /api/overlay/v1/admin/overview` 的响应字段一致（没有行为变化） | 合并即部署 UAT | 自动（ci-pipeline：main → UAT） | ⬜ |
| I-3 | A3 | ① UAT One 执行 apply 后，一次完整 sync 期间 accounts UAT 日志里没有任何 `/api/overlay` 的 404<br>② ACK 只命中 F5 列出的两个接口 | One `v0.1.15` | toolkit `cli_release_tag=v0.1.15` | ⬜ |
| I-4 | T0-2、T0-3 | ① One 在 `net_uat` 上 dry-run 和 apply 都成功（包括 formal 元数据）<br>② `git ls-files NOTICE` 有输出 | One `v0.1.16`（如果决定不合并 `7417a57`，只提交 NOTICE，不发版） | toolkit `cli_release_tag=v0.1.16` | ⬜ |
| I-5 | A2a | 新版本不带 `--watch` 时行为与旧版一致：UAT apply 成功，一次性 sync 的 ACK 成功，没有回归。watch 循环的各种行为由单元测试覆盖 | One `v0.1.17` | toolkit `cli_release_tag=v0.1.17` | ⬜ |
| I-6 | A2b | ① UAT One 由 `xconnect-one-sync.service` 常驻运行，30 分钟常绿<br>② `systemctl restart` 和主机重启后都能自动恢复<br>③ 断网 3 分钟后，恢复网络 2 分钟内回到 `recent_ack`，期间 WireGuard 接口一直存在 | One `v0.1.18` | toolkit apply。如果 One 是由 playbooks 安装的，需要配套 playbooks PR，以 service 方式启动 | ⬜ |
| I-7 | A5 | UAT 面板的同步卡片按 recent / stale / never_seen 分项显示计数，并与 `admin/devices` 接口返回一致（截图加 JSON 对照） | 合并即部署 UAT | 自动（portal ci-pipeline） | ⬜ |
| I-8 | A6 | **M1 验收**：<br>① 用面板登录账号查看，Gateway ≥1、One ≥1，“配置同步”为绿，30 分钟常绿<br>② 新加入一台测试 One 后，2 分钟内所有设备恢复 `recent_ack`<br>③ 归属查询的 SQL 结果与登录账号一致 | 无 | 无 | ⬜ |

**M2：一个仓库、两个二进制（阶段 B，M1 达成后才开始）**

| 迭代 | 任务 | Goal（UAT 可验证） | 发版 | UAT 部署 | 状态 / 证据 |
|---|---|---|---|---|---|
| I-9 | B1 | ① edge-agent main 带上 One 和 Gateway 的完整历史<br>② UAT proxy 节点部署新版本后，现有用户流量不中断（xray-exporter 指标没有掉零），agent 用户同步正常 | edge-agent（main 构建） | platform-ops-toolkit 的 agent 部署 workflow。**开始本轮前先查清它的名称，写回 2.3 表格** | ⬜ |
| I-10 | B2 | ① edge-agent 的 release 同时产出 `xconnect-edge-agent` 和 5 个平台的 `xconnect`<br>② 用 Go 1.26 的 Dockerfile 构建出的 proxy 部署到 UAT 后，行为不变<br>③ 依赖边界检查通过 | edge-agent `v*` | 同 I-9 | ⬜ |
| I-11 | B3 + toolkit | ① toolkit 的 `xconnect-one-uat.yaml` 新增 `cli_release_repo` 输入（默认仍是 XConnect-One）<br>② UAT One 改用 edge-agent release 的 `xconnect` 执行 apply，30 分钟常绿<br>③ B3 的 golden 比对一致 | edge-agent `v*` | toolkit `cli_release_repo=xconnect-edge-agent` | ⬜ |
| I-12 | B4 | UAT gateway 换成 edge-agent 构建的 gateway 角色后 30 分钟常绿，期间 accounts 日志没有 4xx | edge-agent `v*` | 同 I-11（gateway 同样需要 `*_release_repo`），需要用户确认 | ⬜ |
| I-13 | B5 | ① `brew install` 和 `brew upgrade` 都能从 edge-agent release 安装<br>② Linux、macOS、Windows 三个安装脚本实测通过<br>③ 旧仓库 README 顶部标注“已迁移” | 无新版本 | 无 | ⬜ |

**M3：同一台机器跑 proxy 和 gateway（阶段 C）**

| 迭代 | 任务 | Goal（UAT 可验证） | 发版 | UAT 部署 | 状态 / 证据 |
|---|---|---|---|---|---|
| I-14 | C1 | ① UAT 上现有的 v1 gateway 继续拿到 v1，`recent_ack` 不中断<br>② 声明了 v2 能力的测试 gateway 拿到 v2，并且验签通过 | 合并即部署 UAT | 自动 | ⬜ |
| I-15 | C2、C3 | 在 **UAT 测试节点**（不是 tw-xconnect）上以 `--roles=proxy,gateway` 运行：<br>① proxy 测试用户可以正常连接<br>② Zero One 经 443 和 Caddy 能访问到 gateway，30 分钟常绿<br>③ 向 gateway 注入故障时，proxy 不受影响 | edge-agent `v*` | toolkit agent workflow 加测试节点 | ⬜ |
| I-16 | C4 | tw-xconnect 同时运行两个角色 24 小时：proxy 流量不中断，Zero 保持常绿。**需要用户批准后才能开始** | 沿用 I-15 的版本 | 灰度 | ⬜ |

**M4：配额（阶段 D）**

| 迭代 | 任务 | Goal（UAT 可验证） | 发版 | UAT 部署 | 状态 / 证据 |
|---|---|---|---|---|---|
| I-17 | D1 | 设计说明评审通过 | 无 | 无 | ⬜ |
| I-18 | D2 | ① 在 UAT proxy 节点暂停一个测试用户后，它的新连接被拒绝，并记录 Xray 是否重启、已有连接是否断开<br>② 恢复后该用户可以再次连接<br>③ 集成测试的结论写进 D2 | edge-agent `v*` | toolkit agent workflow | ⬜ |
| I-19 | D3 | 测试凭据在多台设备上同时在线时，accounts UAT 里看到的在线 IP 数与实际一致 | edge-agent `v*` 和 accounts | 同上，accounts 走自动部署 | ⬜ |

阶段 E 只出设计说明，不进入交付循环。

### 2.3 各仓库的发版与 UAT 部署方式

| 仓库 | 合并到 main 之后 | 版本号 | UAT 部署 | 回滚 |
|---|---|---|---|---|
| accounts | `ci-pipeline.yml` 自动部署 UAT（main → UAT） | 由流水线生成。记录 run 链接和 Cloud Run revision | 自动 | revert PR 并合并，流水线会自动重新部署 |
| portal | `ci-pipeline.yml`（Console Service Pipeline），main → UAT | 同上 | 自动 | 同上 |
| XConnect-Gateway | 在 main 上打 `v0.1.N` 标签，由 “CI and release” 发布 GitHub Release（当前最新 `v0.1.8`） | 从 `v0.1.9` 开始递增 | toolkit `xconnect-one-uat.yaml` 的 `gateway_release_tag` | 用上一个标签重新 apply |
| XConnect-One | 在 main 上打 `v0.1.N` 标签后发布 Release（当前最新 `v0.1.14`；Formula 里还停在 `v0.1.11`，I-13 一并修正） | 从 `v0.1.15` 开始递增 | toolkit `xconnect-one-uat.yaml` 的 `cli_release_tag` | 用上一个标签重新 apply |
| xconnect-edge-agent | `build-release-artifacts.yml` 在 push main 或打 `v*` 标签时构建 | 沿用现有的 `v*` 规则 | platform-ops-toolkit 的 agent 部署 workflow（名称待 I-9 前查明） | 部署上一个版本 |

### 2.4 回滚原则

- UAT 验证失败时，先回滚到上一个已验证的版本（见 2.3 表格），再开修复分支，修复也走完整的一轮循环。
- 不允许直接在 UAT 节点上手动改文件来“修好”。A1 的 drop-in 是唯一例外，而且需要用户批准，并记录在案。

### 2.5 任务状态表

| ID | 仓库 | 内容 | 依赖 | 状态 | PR / 证据 |
|---|---|---|---|---|---|
| T0-1 | edge-agent | 归档原型工作区，清理 feat 分支 | — | ⬜ | |
| T0-2 | One | 决定 `7417a57` 是否合并 | — | ⬜ 待决策 | |
| T0-3 | One | 提交 NOTICE（确认 `.gitignore` 的改动） | — | ⬜ | |
| A1 | Gateway | 同步间隔改为 60s，未变化时也 ACK | — | ⬜ | |
| A2 | One | 新增 `sync --watch` 与三平台常驻服务 | T0-2 | ⬜ | |
| A3 | One | 删除旧 ACK 路径，加路由契约测试 | A4 | ⬜ | |
| A4 | accounts | 导出 overlay v1 路由清单（golden） | — | ⬜ | |
| A5 | portal | 同步卡片区分 recent / stale / never_seen | — | ⬜ | |
| A6 | 运维 | UAT 数据核对与 30 分钟常绿验收 | A1、A2 | ⬜ | |
| B1 | edge-agent | 带 git 历史迁入 One 和 Gateway | A 全部、T0-3 | ⬜ | |
| B2 | edge-agent | 两个二进制与依赖边界检查 | B1 | ⬜ | |
| B3 | edge-agent | 行为等价验证 | B2 | ⬜ | |
| B4 | edge-agent | Gateway 改用共享的 controlplane | B3、A4 | ⬜ | |
| B5 | 多仓 | 迁移发布渠道，归档旧仓库 | B3 | ⬜ | |
| C1 | accounts | Gateway 签名配置 v2 | B4 | ⬜ | |
| C2 | edge-agent | gateway v2 渲染与 Caddy 片段 | C1 | ⬜ | |
| C3 | edge-agent | `--roles=proxy,gateway` | C2 | ⬜ | |
| C4 | 运维 | UAT 验证后 tw-xconnect 灰度 | C3 | ⬜ | |
| D1 | 多仓 | 调研计费键（只出设计说明） | — | ⬜ | |
| D2 | edge-agent | 用 `rmu` 移除用户，失败时重启兜底，加集成测试 | — | ⬜ | |
| D3 | edge-agent + accounts | 上报在线 IP 数 | D2 | ⬜ | |
| E1–E3 | 设计 | regionpool 迁到控制面、区域入口、Zero HA | — | ⬜ 只出设计 | |

执行顺序以 2.2 的迭代编号为准。一轮没通过 UAT 验证，就不开始下一轮。同一时间只有一个迭代处于“已部署、待验证”状态，这样 UAT 上的现象才能对应到唯一的改动。

---

## 3. T0：现场整理

### T0-1 归档原型（edge-agent）

当前 `feat/overlay-convergence` 上没有任何提交，所有改动都是未提交或未跟踪的文件。按下面的步骤操作，一个文件都不会丢：

```bash
cd /Users/shenlan/workspaces/xconnect-edge-agent
git switch -c wip/multicall-prototype
git add -A -- . ':(exclude)docs/tasks'
git commit -m "wip: multicall/ratelimit prototype (archive only, do not merge)"
git switch feat/overlay-convergence
git status --short            # 应只剩 docs/tasks/
git add docs/tasks && git commit -m "docs(tasks): add edge convergence plan"
git push -u origin feat/overlay-convergence
gh pr create --base main --title "[I0][T0-1] docs: edge convergence plan" --body "..."
# 合并后删除 feat/overlay-convergence，之后每一轮都从 origin/main 拉短分支
```

验收：
- `git rev-list --count main..feat/overlay-convergence` 输出 `1`，只有这份文档的提交。
- 在 feat 分支上，`git ls-files internal/ratelimit internal/overlay internal/gateway` 输出为空。
- 是否把 `wip/multicall-prototype` 推送到远端，见第 11 节。

`wip/multicall-prototype` 只留档，不合并，也不从里面挑代码回收。B1 会从 One 和 Gateway 的 tag 带历史重新迁入，原型里的内容都有正本。

### T0-2 决定 One 的 `7417a57`

- 调查：比对 accounts #6（formal Zero UAT bootstrap）之后 exchange 响应里新增的字段，确认 One main 能否正确解析。方法是用 accounts 的 exchange 响应样例跑 One 的 `usecase` / `controlplane` 测试。
- 如果需要这个修复：单独开 PR 合并到 One main，失败测试就用那个响应样例。
- 把结论写进第 2 节状态表。

### T0-3 One 提交 NOTICE

- `git diff .gitignore`，确认 One main 工作区里 `.gitignore` 的改动是否应该保留。
- 单独开 PR 提交 `NOTICE`（PROVENANCE.md 引用了它）。
- 验收：`git ls-files NOTICE` 有输出。

---

## 4. 阶段 A：ACK 闭环（最优先，与融合无关）

目标：让 UAT 面板上 Gateway 和 One 持续显示 `recent_ack`；新设备加入后，其它设备在 2 分钟内恢复。

### A1 Gateway：同步间隔 60s（XConnect-Gateway，迭代 I-1，分支 `i1-a1-gateway-timer`）

**先写的失败测试**
1. `internal/gateway/packaging_test.go`：读取 `../../packaging/systemd/xconnect-gateway-sync.timer`，解析 `OnUnitActiveSec` 和 `AccuracySec`。断言 `interval + accuracy <= 100s`，也就是不超过 accounts 5 分钟窗口的三分之一。常量 `ackRecentWindow = 5 * time.Minute` 旁边加注释，指向 accounts `internal/overlay/service.go:37`。
2. `internal/gateway/client_test.go` 增加用例：同一个 generation 连续执行两次 sync，httptest 服务端应收到 **2 次** ACK 请求。如果现有 sync 在配置未变化时跳过 ACK，这个测试会失败，说明要一起修。
3. ACK 请求失败时，`sync` 返回非 0 退出码，让 systemd 能记录失败。

**实现**
- timer 改为：
  ```ini
  [Timer]
  OnBootSec=30s
  OnUnitActiveSec=60s
  AccuracySec=5s
  ```
- 配置未变化时照常发 ACK（重复 ACK 会刷新 `last_seen_at`，见 F3）。
- 更新 `README.md:129` 附近和 `docs/self-hosted-install-and-validation.md:68-73`：写明同步间隔为什么必须小于 100s。

**已部署节点的滚动更新**：升级二进制不会更新 `/etc/systemd/system` 下的 unit，需要加 drop-in 覆盖：
```bash
sudo systemctl edit xconnect-gateway-sync.timer
# 写入：
# [Timer]
# OnUnitActiveSec=
# OnUnitActiveSec=60s
# AccuracySec=5s
sudo systemctl daemon-reload
sudo systemctl restart xconnect-gateway-sync.timer
systemctl list-timers xconnect-gateway-sync.timer
```

**验收**
- `journalctl -u xconnect-gateway-sync.service --since "-10min"` 显示至少 9 次运行，每次 ACK 都成功。
- A6 的 30 分钟采样中，gateway 全部是 `recent_ack`。

### A2 One：`sync --watch` 与常驻服务（XConnect-One，迭代 I-5 和 I-6，分支 `i5-a2a-sync-watch` 和 `i6-a2b-one-service`）

拆成两个 PR：A2a 做代码，A2b 做安装脚本和服务注册。

**A2a 先写的失败测试**，放在 `overlay/usecase/sync_loop_test.go`，使用 fake clock 和 fake control plane：
1. 每个 tick 恰好调用一次 `DeviceSessionManager.Sync`（`overlay/usecase/device_session.go:76`）。**配置未变化时也要发 ACK**：同一个 generation 两个 tick，对应 2 次 `AckEnrollmentSignedConfig`。
2. 收到 `ErrGenerationConflict`（409）时，在同一个 tick 内立即重新拉取配置并应用一次，最多重试 1 次。
3. 遇到网络错误、5xx 或 429 时，按 5s、10s、20s 递增退避，上限是 interval。**不拆隧道**：fake runtime 上不能出现 `Down` 调用。
4. 遇到凭据吊销或 401 这类不可恢复的错误，循环以对应的 fault code 退出，退出码非 0。对 runtime 的处理沿用现有的 leave 语义，不要新增拆除逻辑。
5. `ctx` 取消（SIGTERM）后 1 秒内返回 `nil`，不拆隧道。
6. `--interval` 校验：默认 60s，允许 15s 到 100s，超出范围直接报参数错误。

**A2a 实现**
- 新建 `overlay/usecase/sync_loop.go`，定义 `SyncLoop{Sync func(context.Context) (SyncResult, error); Clock; Interval; Backoff}`。
- `cmd/xconnect/main.go` 的 `runSync` 增加 `--watch` 和 `--interval` 参数，只负责把循环接起来；不带 `--watch` 时行为和现在完全一样。
- 如果 `overlay/state` 已有状态目录锁，watch 循环复用它；没有的话本任务不新增，记为遗留项。

**A2b 服务注册**（每个平台都给出安装、卸载和查看状态的命令）
- **Linux**：`scripts/install-xconnect-one.sh` 生成 `/etc/systemd/system/xconnect-one-sync.service`，内容为 `Type=simple`、`ExecStart=/usr/local/bin/xconnect sync --watch --interval=60s --state-dir <默认目录>`、`Restart=on-failure`、`RestartSec=10`。
- **macOS**：在 `Formula/xconnect-one.rb` 里加 `service do ... keep_alive true`。权限模型先按 One 现有的 macOS runtime（WireGuard 需要 root）确认，是否需要 `sudo brew services`。结论写进 PR 描述。
- **Windows**：`scripts/install-xconnect-one.ps1` 注册一个开机启动、以 SYSTEM 身份运行 `xconnect.exe sync --watch` 的计划任务。
- 测试：脚本要通过 `bash -n` / `shellcheck` 和 PowerShell 语法检查；生成的 unit 内容与 golden 文件比对。

**验收**（在 observability.svc.plus 上）
- 30 分钟内持续 `recent_ack`。
- 断网 3 分钟再恢复，2 分钟内回到 `recent_ack`，期间 WireGuard 接口始终存在。

### A3 One：删除旧 ACK 路径（XConnect-One，迭代 I-3，分支 `i3-a3-remove-legacy-ack`）

**先写的失败测试**：`overlay/controlplane/routes_contract_test.go`
- 在 controlplane 包里导出一份客户端会调用的 `{method, path 模板}` 清单，客户端构造请求时也必须使用这份清单，不能在别处拼路径。
- 与 `testdata/accounts_overlay_v1_routes.json` 比对，这个文件从 A4 的 golden 复制过来，文件头注明来源的 accounts SHA。
- 预期失败：`POST /api/overlay/v1/config/ack` 不在 accounts 的清单里。

**实现**
- 删除 `AckConfig`（`client.go:107`）和 `usecase/join.go:313` 的旧分支。
- 控制面不支持签名配置时，返回明确的 fault，不要静默走旧接口。
- 验收：`git grep -n "config/ack"` 在 One 仓库里没有结果，契约测试通过。

### A4 accounts：overlay v1 路由清单（accounts，干净 worktree）

**先写的失败测试**：`api/overlay_routes_inventory_test.go`
- 用 `api/*_test.go` 里现有的测试路由构造方式搭建 gin engine。
- 从 `engine.Routes()` 中筛选前缀为 `/api/overlay/v1` 的路由，按 method 和 path 排序后序列化。
- 与 `api/testdata/overlay_v1_routes.golden.json` 比对；支持 `-update` 参数重新生成。

要求：
- 本 PR **不改任何行为**，也不动 5 分钟窗口。
- 验收：CI 通过，golden 文件已提交，SHA 记进本文第 2 节。

### A5 portal：同步卡片区分状态（portal，干净 worktree）

**先写的失败测试**：沿用 `src/modules/extensions/builtin/xconnect-zero/routes/onboarding.test.tsx` 的测试写法：
- 设备为 `[stale, stale]`：显示“ACK 已过期（2 台）”，不显示“暂无 ACK”。
- 设备为 `[]` 或全部 `never_seen`：显示“暂无 ACK”。
- 设备为 `[recent_ack, stale]`：显示“1 个 ACK”，并注明过期 1 台。

**实现**
- 改 `xconnect-zero-node-management.tsx` 第 483 行附近的计数和第 905 到 915 行的文案，以及 `src/lib/xconnectZero.ts:53-65`。
- 加提示文字：“ACK 表示最近 5 分钟内收到当前配置版本的确认，不代表隧道已握手”。中英文都要有。

### A6 UAT 数据核对与验收（运维，不写代码）

1. **归属核对**（只读）：
   ```sql
   select n.id, n.owner_user_id, n.config_generation, d.id as device_id, d.role, d.status, d.last_seen_at
   from overlay_networks n join overlay_devices d on d.network_id = n.id
   order by n.id, d.role;
   ```
   把结果与登录面板的账号的 user_id 对比。不一致时**不要手改数据**，写进第 11 节等决策。
2. **30 分钟常绿**：以面板登录账号调用 `GET /api/overlay/v1/admin/devices`，每 5 分钟采样一次，共 7 次。每次保存目标设备的 `role`、`connection_status`、`last_seen_at`，全部应为 `recent_ack`。
3. **新设备加入**：加入一台测试用的 One，2 分钟内其它设备恢复 `recent_ack`。
4. 截取面板“配置同步”卡片的截图。
5. 以上证据全部附在本文第 2 节 A6 行。

---

## 5. 阶段 B：代码收敛（edge-agent，一个仓库、两个二进制）

**前置条件**：A1 已合并并打 tag（记为 `<GW_TAG>`）；A2、A3、T0-2、T0-3 已合并到 One 并打 tag（记为 `<ONE_TAG>`）；A6 验收通过。

### B1 带 git 历史迁入

需要 `brew install git-filter-repo`。

```bash
# One
git clone https://github.com/ai-workspace-xstream/XConnect-One.git /tmp/one-import
cd /tmp/one-import && git checkout -b import <ONE_TAG>
git filter-repo --force \
  --path overlay/ --path cmd/xconnect/ --path NOTICE --path PROVENANCE.md --path LICENSE \
  --path-rename overlay/:internal/overlay/ \
  --path-rename NOTICE:third_party/XConnect-One/NOTICE \
  --path-rename PROVENANCE.md:third_party/XConnect-One/PROVENANCE.md \
  --path-rename LICENSE:third_party/XConnect-One/LICENSE
cd /Users/shenlan/workspaces/xconnect-edge-agent
git fetch origin && git switch -c i9-b1-import-history origin/main
git fetch /tmp/one-import import:one-import
git merge --allow-unrelated-histories one-import -m "import: XConnect-One <ONE_TAG> with history"
```

- Gateway 用同样的方法迁入：`internal/gateway/` 路径不变，`cmd/xconnect-gateway/` 暂时原样保留，B2 再并入服务端。Gateway 没有 LICENSE，迁入前先按第 11 节确认许可证。
- **import 路径改写单独一个 commit**：把 `github.com/ai-workspace-xstream/XConnect-One/overlay` 改为 `github.com/ai-workspace-xstream/xconnect-edge-agent/internal/overlay`，Gateway 同理。
- `go.mod`：`go 1.26.4`，`golang.org/x/sys` 版本不低于 One 使用的版本，然后执行 `go mod tidy`。

**验收**
- `git log --follow --oneline internal/overlay/usecase/join.go` 能看到 One 的历史提交。
- `git diff <ONE_TAG 对应的导入提交> HEAD -- '*_test.go'` 只有 import 行有变化，把输出附在 PR 里。
- `go test ./...` 全部通过。

### B2 两个二进制与依赖边界

- **服务端**：保留 `cmd/agent`。二进制名和 Dockerfile 入口都不变，因为部署由 platform-ops-toolkit 负责，改名会破坏部署。
  - 增加 `--roles`：默认 `proxy`，可选值 `proxy`、`gateway`，用逗号组合。
  - Gateway 的运维命令以 `xconnect-edge-agent gateway <init|join|sync|up|status>` 子命令的形式提供。
- **客户端**：`cmd/xconnect`，也就是 One 的 CLI 原样迁入，包括 `app_bridge.go`。
- **禁止**按 argv[0] 做多调用分发，禁止 `one` 子命令，禁止 hybrid 子命令。
- **依赖边界测试**（先写，失败）：`scripts/check-deps.sh`，在 CI 里运行：
  - `go list -deps ./cmd/agent` 不得包含 `internal/overlay/runtime`、`internal/overlay/credential`。
  - `go list -deps ./cmd/xconnect` 不得包含 `internal/agentmode`、`internal/xrayconfig`、`internal/regionpool`、`internal/gateway`。
- **工具链**：`Dockerfile.tcp` 和 `Dockerfile.xhttp` 改为 `FROM golang:1.26-alpine`（官方镜像默认 `GOTOOLCHAIN=local`，不升级会构建失败）；CI 的 `setup-go` 使用 `go-version-file: go.mod`。
- **发布产物**：在 `.github/workflows/build-release-artifacts.yml` 里增加 `xconnect-{macos-arm64,macos-amd64,linux-amd64,linux-arm64,windows-amd64.exe}`，命名与 One 现有产物一致，这样 Formula 只需要改仓库地址。

**验收**
- 交叉编译 linux/amd64、linux/arm64、darwin/arm64、darwin/amd64、windows/amd64 全部成功。
- `docker build -f Dockerfile.xhttp .` 成功。
- 依赖边界检查通过。

### B3 行为等价

- One 和 Gateway 的全部测试（包括 One `cmd/xconnect` 的 924 行测试）除 import 行外原样通过。
- 分别用 `<ONE_TAG>` 构建的 `xconnect` 和本仓构建的 `xconnect`，执行 `version`、`status --state-dir <fixture>`、`diagnose --state-dir <fixture>`，输出与 golden 一致（版本号字段除外）。

### B4 Gateway 改用共享的 controlplane

- **先写的失败测试**：Gateway 调用的所有路由都必须在 A4 的 golden 里；用 accounts 的签名 gateway 配置样例，以测试密钥做验签。
- 把 `internal/gateway/client.go` 里手写的 exchange、device session、gateway signed-config 和 ACK 请求，全部换成 `internal/overlay/controlplane` 的方法；缺什么就补什么，例如 `GatewaySignedConfig`。
- 现有的 `internal/gateway/client_test.go` 仍需通过。
- 验收：`internal/gateway/client.go` 删除，或只剩适配层。

### B5 迁移发布渠道并归档旧仓库

- 需要修改的地方：
  - `Formula/xconnect-one.rb` 的 url，当前指向 `XConnect-One/releases/download/v0.1.11/...`。
  - `install-xconnect-one.sh` 和 `install-xconnect-one.ps1` 的下载地址。
  - Gateway 的 `install-xconnect-gateway.sh`。
- 旧仓库 README 顶部加“已迁移”说明，保留最后一个 release。
- **满足以下两条才能归档**：Formula 已切换；旧仓库最近 14 天 release 下载量为 0（用 `gh api repos/.../releases` 查看 `download_count`）。
- 验收：从旧版本执行 `brew upgrade` 能升级到新产物；三个平台的安装脚本都实测通过。

---

## 6. 阶段 C：同一台机器同时跑 proxy 和 gateway（tw-xconnect 的真实需求）

### C1 accounts：Gateway 签名配置 v2

- 新增 `schema_version: 2`，`transport` 增加 `public_port`（对外端口，443）和 `tls_termination`（`edge` 或 `xray`）。本地监听方式由 agent 自己决定，不写进契约。
- **协商方式**：gateway 在 session 或 exchange 时声明能力 `gateway-config-v2`，accounts 只给声明了该能力的 gateway 下发 v2，其余继续下发 v1。协商方式最终以第 11 节的决策为准。
- 先写的失败测试：v1 的 golden 保持不变；新增 v2 的 golden；签名覆盖所有新增字段；没有声明能力的请求一定拿到 v1。

### C2 edge-agent：gateway v2 渲染

- `tls_termination=edge` 时：
  - Xray inbound 监听 unix socket `/dev/shm/xconnect-gateway.sock`，`security: none`，xhttp path 取自契约，不再需要证书文件。
  - 生成 Caddy 片段，写法与现有 `/split` 的 Caddy 配置保持一致。Caddyfile 在 platform-ops-toolkit 的部署模板里，不在本仓，先找到 `/split` 的写法再照着写。
- Gateway 继续使用**独立的 Xray 进程**（`xconnect-gateway-xray.service`），不和 proxy 共用，因为 proxy 删除用户时仍会重启 Xray（F12）。
- 测试：
  - xray.json v2 与 golden 比对。
  - 沿用 `xray_local_validation_test.go`，本机有 xray 时执行 `xray run -test`。
  - Caddy 片段与 golden 比对。

### C3 `--roles=proxy,gateway`

- 一个进程同时运行 proxy 的 agentmode 和 gateway 的同步循环（每 60s，语义与 A1 相同）。启用 roles 后，gateway 不再需要 systemd timer。
- **gateway 出错不能影响 proxy**，也不能被忽略（禁止 `_ =`）。要记录日志、计数并重试。
- 先写的失败测试：fake gateway syncer 持续返回错误时，proxy runner 仍在运行，错误计数递增。

### C4 灰度发布

1. 先在 UAT 节点上验证：proxy 用户流量不中断（看 xray-exporter 指标）；Zero 的 One 经 443 和 Caddy 能到达 gateway；30 分钟持续 `recent_ack`。
2. 然后在 tw-xconnect 上灰度。回滚方式是关闭 gateway 角色，不影响 proxy。
3. 每一步的命令和证据都记录到本文。

---

## 7. 阶段 D：配额与限流（建立在 accounts 已有的配额暂停之上，F11）

### D1 调研计费键（只出设计说明）

- 查清以下三处读取 Xray 的 email 标签时用的是什么：edge-agent `internal/agentmode/billing_client.go`、accounts `api/accounting.go` 的心跳入口、xray-exporter。
- 产出 `docs/architecture/xray-user-label-migration.md`：把 email 标签迁移为 `u:<user_id>` 的双标签过渡方案，写清兼容窗口和回滚方式。
- **评审通过前不改代码。**

### D2 用 `xray api rmu` 移除用户，失败时重启兜底

- 在 `internal/xrayconfig/dynamic_users.go` 里、`CLIUserAdder` 旁边新增 `CLIUserRemover`。
- `syncer.go:216-242` 的删除路径改为先调用 remover，出错时再重启。
- API 地址从配置读取，默认 `127.0.0.1:10086`；inbound tag 为 `xhttp-vless`。
- **单元测试**：用 fake runner 验证命令参数、输出解析和兜底逻辑。
- **集成测试**（build tag `xrayintegration`，需要本机有 xray）：
  - 用随机端口启动带 API 的 xray，建立一个 VLESS 连接，然后执行 `rmu`。
  - 断言：被删除的 UUID 无法再建立新连接。
  - 记录：已经建立的连接会不会断开，把实测结果写进本文。
- 根据实测结果定策略：如果已有连接不会断开，配额暂停的场景仍然走重启，`rmu` 只用于普通删除。

### D3 上报在线 IP 数

- 先确认线上 Xray 版本是否支持在线统计（`statsUserOnline` policy 以及在线 IP 列表 API）。不支持就把任务标为阻塞并记录。
- 支持的话：agent 通过现有节点心跳上报每个凭据的在线 IP 数，处置策略由 accounts 决定。**agent 不在本地做拒绝。**

---

## 8. 阶段 E：regionpool 与区域入口（只出设计，评审后再写代码）

- **E1**：把 registry 迁到控制面（accounts 或独立调度服务），节点只上报心跳。edge-agent 的 `internal/regionpool` 之后删除，或者只保留心跳客户端。产出 `docs/architecture/regionpool-control-plane.md`。
- **E2**：区域入口放在 DNS 或负载均衡层（Cloudflare LB 加健康检查，或 GeoDNS），后面挂至少 2 个节点；客户端从订阅拿到节点列表后直连，流量不经过“区域网关”中转。
- **E3**：Zero gateway 高可用：每个网络至少 2 个 gateway，或者由 accounts 重新签发配置完成切换。写成设计说明。

---

## 9. 不做清单（不要重新引入）

- `internal/ratelimit`、agent 内存令牌桶、对 VLESS/xhttp 返回 HTTP 429。
- `internal/regionpool/middleware.go` 这类 HTTP 反向代理式的调度和凭据校验；registry 里的 `IsGateway` 和 `GatewayNode`，也就是每个区域一个入口节点。
- 单二进制多调用（按 argv[0] 分发、`one` 或 `hybrid` 子命令、`cmd_hybrid.go`）。
- Kong 或 APISIX。
- 把 email 当身份键；把凭据放进 URL query 或转发请求头。
- 任何对 `POST /api/overlay/v1/devices/{id}/ack` 或 `/api/overlay/v1/config/ack` 的调用。
- 从 `wip/multicall-prototype` 挑代码合进来。

---

## 10. 每个任务完成后的汇报格式

```
任务 ID：
PR：<链接> [OPEN/MERGED]
失败测试 commit：<sha>    实现 commit：<sha>
测试输出：<粘贴 go test / vitest 的关键片段，包括失败 → 通过两次>
证据：<命令输出 / 接口 JSON / 截图路径>
偏离计划之处及原因：<没有则写“无”>
```

---

## 11. 待决策（需要用户回复，执行者不要自行决定）

1. **T0-2**：One 的 `7417a57` 是否合并。
2. **A6**：如果网络归属与登录账号不一致，用什么方式修正（转移 owner、重新 bootstrap，还是换账号查看）。
3. **C1**：v2 的协商方式，是用 gateway 能力声明（推荐），还是按网络设置开关。
4. **T0-1**：`wip/multicall-prototype` 是否推送到远端。推送属于对外操作，需要确认。
5. **B1**：XConnect-Gateway 没有 LICENSE，迁入前确认许可证（建议统一为 Apache-2.0，与 One 和 edge-agent 一致）。
