# docs/tasks：任务归档（项目记忆）

每个任务在这里建一个 Markdown 文件，写清目标、结论、关联 PR（带编号和状态）、验证方式和遗留待办，作为跨会话的项目记忆。

## 约定

- 文件名格式：`YYYY-MM-DD-<kebab-任务名>.md`。
- 文件顶部放状态块（Status / Date / Related PRs）。PR 用完整链接，并标注 [MERGED]、[OPEN] 或 [CLOSED]。
- 跨仓任务在每个相关仓库各留一份本仓视角的文件，互相引用。
- UAT 验证证据放在 `evidence/I<n>-*.log`，不得包含 token 等凭据。

## 索引

| 日期 | 任务 | 状态 | 关联 PR |
|---|---|---|---|
| 2026-09-18 | [XConnect 边缘收敛与 ACK 闭环：开发规划](2026-09-18-xconnect-edge-convergence-plan.md) | 🟡 执行中（I-0） | 待填 |
