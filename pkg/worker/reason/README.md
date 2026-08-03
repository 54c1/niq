# reason — LLM Worker

reason 是 niq 中负责推理决策的 Worker。它从事件总线接收事件，调用 LLM 推理，以事件形式发布结果。

## 架构

```
watch() — 单 goroutine 事件循环
  │
  ├─ 休眠点: <-busCh（阻塞等待事件，零 CPU）
  ├─ process(evt) — 九路事件分派，纯状态路由，微秒级
  │     ├─ worker.discover  → 回复 worker.ready
  │     ├─ worker.abort     → 中断推理 + 取消工具
  │     ├─ capability       → 更新工具集
  │     ├─ timer.timeout    → 超时标记 pending 工具
  │     ├─ tool.result      → 更新占位符 + 检查 resolved
  │     ├─ tool.requested   → 处理内建工具（publish_message）
  │     └─ input            → 追加 messages + 设 needReason
  │     ├─ decision.made    → 人类审批结果
  │     ├─ timer.reminder   → 提醒唤醒 LLM
  │
  └─ tryReason() — 决策门
        └─ needReason && !isReasoning → reason()
              │
              ├─ LLM 调用（同步，可能秒级）
              ├─ 工具调用 → 插入占位符 → 发布到总线 → return
              └─ 文本响应 → 发布 → return
                    └─ tryReason() — 抓住重叠事件
```

## 文件

| 文件 | 职责 |
|---|---|
| `worker.go` | Worker struct, Config, NewWorker, Start, Stop |
| `watch.go` | watch 事件循环, process 八路分派, tryReason 决策门 |
| `reason.go` | reason 推理, insertPlaceholders, buildInstruction |
| `toolset.go` | 内建工具定义 (initBuiltinTools), handleCapability 工具发现, allTools 工具聚合, handleToolRequest 内建工具路由 |
| `tracker.go` | ToolCallTracker 生命周期 (Request/handleResponse/TimeoutPending/CancelAll/InterruptPending) |
| `result.go` | tool_result → LLM 消息转换, updatePlaceholder 占位符管理, appendLateResult 迟到结果, handleDecisionMade 人类决策 |
| `tickafter.go` | cancelActiveTickers 超时定时器清理, reasonEpoch + activeTickafters 防 stale 保护 |

## 核心概念

### process 八路分派

所有事件通过 `process()` 路由——不产生新概念，只是不同事件改变不同状态：

| 事件 | 影响状态 | 设 needReason? |
|---|---|---|
| worker.discover | 回复 worker.ready | no |
| worker.abort | cancel ctx + CancelAll + messages | no |
| timer.timeout | 超时: TimeoutPending → updatePlaceholder | yes |
| timer.reminder | 转 input 事件 | yes |
| worker.ready/gone | busTools | no |
| tool.requested | 内建工具路由 (publish_message) | no |
| decision.made | messages.append | yes |
| tool.completed/failed/rejected | callTracker + messages + placeholder | 当 resolved |
| input (hiw.input 等) | messages.append | 取决于 input_mode |

### 工具生命周期

`callTracker` 管理同批工具调用。每个 ToolCall 从 `ToolPending` 出发，到达四种终态：

- **ToolCompleted** — 正常返回
- **ToolTimedOut** — timer.timeout 触发或 TimeoutPending
- **ToolInterrupted** — 高优消息到达，主动不等（InterruptPending）
- **ToolRejected** — abort 触发或 CancelAll
- **ToolFailed** — 工具执行错误

### 时间事件：两种模式

`timer.timeout` 和 `timer.reminder` 两种事件，由定时器来源决定：

1. **超时**（`set_tool_timeout` 设置，事件 `timer.timeout`）：`TimeoutPending` 标记所有 pending 工具为超时，`updatePlaceholder` 原地更新，唤醒 LLM
2. **提醒**（`elapse` 设置，事件 `timer.reminder`）：直接走 `handleInput`，把事件内容转成 LLM 消息，LLM 醒来后自行判断意图

`reasonEpoch` + `activeTickafters`/`elapseTickafters` map 保护防止 stale 定时器误伤后续批次。

### Transcript 保护

LLM API 要求在 `assistant(tool_calls: [...])` 和后续 `tool_result(...)` 之间不能插入非 tool_result 消息。reason 采用**占位符方案**：产出工具调用时即刻在 messages 中插入 `tool_result(pending)` 占位。到达时 updatePlaceholder 原地更新，迟到时 appendLateResult 追加为 user 消息。

### 人类决策

`decision.made` 事件携带人类审批的 decision/reasoning/request_summary，被转换为 user 消息追加到对话，触发下一轮推理。这是 hiw 与 reason worker 之间的审批协作通道。

## 设计原则

1. **process 不调 LLM。** 事件处理是微秒级的，只做路由和状态更新。推理从 tryReason 发起。
2. **reason 不递归。** 一次推理一个结果。链通过 tryReason 的自然重入形成。
3. **没有内部队列。** process 实时处理。busCh 64 缓冲在微秒级消费下永远空着。
4. **特殊性不外泄。** tickafter 在总线上和 read_file 完全相同，唯一的"特殊逻辑"在 callTracker 内部的多一条动作。
