# reason — LLM Worker

reason 是 niq 中负责推理决策的 Worker。它从事件总线接收事件，调用 LLM 推理，以事件形式发布结果。

## 架构

```
watch() — 单 goroutine 事件循环
  │
  ├─ 休眠点: <-busCh（阻塞等待事件，零 CPU）
  ├─ process(evt) — 事件分派，纯状态路由，微秒级
  │     ├─ worker.discover  → 回复 worker.ready
  │     ├─ worker.abort     → park 工具 + 追回(tool.cancel) + 结束会话
  │     ├─ capability       → 更新工具集
  │     ├─ timer.timeout    → park 残留工具（cause=timeout）
  │     ├─ tool.result      → resolve / park-late / 更新占位符
  │     ├─ tool.requested   → 处理内建工具（publish_message）
  │     ├─ input            → 追加 messages + park + 设 needReason
  │     ├─ decision.made    → 人类审批结果
  │     └─ timer.reminder   → 提醒唤醒 LLM
  │
  └─ tryReason() — 决策门
        └─ needReason && !isReasoning → go reason()（独立 goroutine）
              │
              ├─ LLM 调用（秒级，不阻塞事件循环）
              ├─ 工具调用 → 插入占位符 → 发布到总线 → return
              └─ 文本响应 → 发布 → return
                    └─ tryReason() — 抓住重叠事件
```

## 文件

| 文件 | 职责 |
|---|---|
| `worker.go` | Worker struct, Config, NewWorker, Start, Stop, Snapshot/Restore（委托 contextBuilder） |
| `watch.go` | watch 事件循环, process 分派, tryReason 决策门, 事件处理器, cancelTimeout |
| `reason.go` | reason 推理核心, 推理生命周期发布 (start/end/response/thinking), ErrorContextLength 压缩重试 |
| `message.go` | 事件->消息转换 + 生命周期->BuilderInput 翻译: convertEvent/DefaultConverter, updatePlaceholderFromEvent, appendLateResult |
| `budget.go` | 上下文预算: token 账本 (Usage 快照), 软/硬阈值 (提醒/直接紧缩), 紧缩编排 (投影->摘要->应用), 投影 (剥 image/thinking/截断) |
| `toolset.go` | 工具相关: initBuiltinTools, handleWorkerReady/Gone, allTools, toolDefs/sanitize, publishToolRequests, 内建工具路由 |
| `tooltracker.go` | ToolCallTracker 状态机 (Add/handleResponse/parkAll/resolveLate)，仅管 map |
| `systemprompt.go` | buildInstruction, system prompt 模板 |
| `builder/` | **上下文构造器子包**：ContextBuilder 接口 + 封闭输入变体 (builder.go)、tool 配对不变式基座 (transcript.go)、默认累加实现 (accumulate.go)。转录的归属地，见 doc/design/reason_worker/context-builder.md |

## 核心概念

### 事件分派

所有事件通过 `process()` 路由——不产生新概念，只是不同事件改变不同状态：

| 事件 | 影响状态 | 设 needReason? |
|---|---|---|
| worker.discover | 回复 worker.ready | no |
| worker.abort | park(Cause=abort) + recall + messages，结束会话 | no |
| timer.timeout | park(Cause=timeout) → updatePlaceholderToParked | yes |
| timer.reminder | 转 input 事件 | yes |
| worker.ready/gone | busTools | no |
| tool.requested | 内建工具路由 (publish_message) | no |
| decision.made | messages.append | yes |
| tool.completed/failed/rejected | resolve 或 park-late + placeholder | 当 resolved |
| input (hiw.input 等) | messages.append + park | 取决于 input_mode |

### 工具生命周期

`callTracker` 只维护**仍在 map 中的调用**的状态，只有两种：

- **ToolPending** — 活动：reasoner 仍在等待
- **ToolParked** — 已 park：不再等待，但保留以备 late result（`ParkCause` 区分 input/timeout/abort）

工具结果事件到达 → 调用从 map 移除。**终态（completed / failed / rejected）不是 tracker 的状态，而是消息层概念**，由调用方从事件构造：

| 场景 | 构造方式 | 数据来源 |
|---|---|---|
| 正常解决 | `resultMessageFromEvent` | 读事件（call_id/name/result） |
| 未知工具 | `failMessage` | 纯函数（call_id/name/reason） |
| park | `parkResultMessage` | 读 `ParkCause` |
| 晚归 | `appendLateResult` | 读事件 + `ParkCause` |

**park 的 cause（`ToolCallCause`）**：驱动占位符文案与 late result 消息。
- `input` — 新输入抢占（default 模式）
- `timeout` — 批次超时
- `abort` — abort 信号结束会话

### 输入模式

`input_mode` 控制输入与推理/工具的关系：

- **default**（或未设置）— 立即响应：追加消息 + 打断在飞推理 + park 全部工具（cause=input）+ needReason。用户输入优先。
- **append** — 仅留言：追加消息，不设 needReason，当前任务跑完，下一轮再处理。

**不变式：`needReason ⟹ Pending 为空`。** 每条设 needReason 的路径（resolve 全完成 / timeout park / default park / abort park）都会先清空 Pending，因此推理轮次启动时不会与旧工具混批，无需跨 epoch park。

### 时间事件：两种模式

`timer.timeout` 和 `timer.reminder` 两种事件，由定时器来源决定：

1. **超时**（`set_tool_timeout` 设置，事件 `timer.timeout`）：`parkAll(Cause=timeout)` 标记所有 pending 工具为 park，`updatePlaceholderToParked` 原地更新，唤醒 LLM
2. **提醒**（`elapse` 设置，事件 `timer.reminder`）：直接走 `handleInput`，把事件内容转成 LLM 消息，LLM 醒来后自行判断意图

每轮最多一个有效超时定时器——第一个到点的会 park 全部工具。reason 用 `activeTimeout`（当前轮 set_tool_timeout 的 call_id，每轮重置）跟踪；孤儿定时器事件（call_id 不匹配）被静默丢弃，无需 epoch。提醒（`elapse`）不单独跟踪——`timer.reminder` 到达时直接作为一次唤醒输入处理（`setImmediateReasoning(CauseReminder)`）。

### Transcript 保护

LLM API 要求在 `assistant(tool_calls: [...])` 和后续 `tool_result(...)` 之间不能插入非 tool_result 消息。这套不变式现在由 `builder` 子包持有（占位符方案）：产出工具调用时即刻插入 `tool_result(pending)` 占位；到达时原位更新；park 时原位替换为 park 说明；迟到时追加为 user 消息（避免同一 call_id 的重复 tool 消息）。reason worker 把生命周期翻译成封闭的 BuilderInput 变体递给 builder，不再直接写转录。

### 上下文构造器与预算

转录不是 worker 的核心状态，是 `builder.ContextBuilder` 的内部投影。默认实现是累加 builder（平铺转录 + cursor）。两个动词已接入：

- **Compact（预算紧缩）**：每轮流式往返后从 `Usage` 记账（最近一次 Input+Output 快照），占比 ≥ 软线（0.85）注入提醒，LLM 调内建 `compress` 工具；≥ 硬线（0.97）系统直接紧缩；开流遇 `ErrorContextLength` 先紧缩再重试一次。摘要由自有 provider 非流式调用完成，directive 可由 program/配置覆盖（`compact_directive`），默认内置 fallback 模板；已有 digest 时走增量合并（早期目标不丢）。投影先行：剥 image/thinking、截断超长 tool_result。切点自动对齐配对边界。
- **翻篇（`context.close_episode` 工具）**：keepTail=2 的 Compact（保留翻篇调用本身的 assistant(tool_calls)+占位符），新集从 digest 开始。

紧缩在锁外满要（快照->摘要->应用三段），应用时重算 keepTail，摘要期间的并发追加不丢失；单飞（isCompacting）防重复。详见 doc/design/reason_worker/context-builder.md。

### abort 追回

abort 时对已发出的工具调用补发定向的 `tool.requested(name="cancel")` 到各目标 worker（与定时器 cancel 同形状），尝试追回但不保证成功。目标 worker（workspace/host/http）提供 best-effort 的 cancel 处理器。已 park 的工具调用保留在 tracker，晚归结果会以带 cause 的 late result 消息告知 LLM。

### 人类决策

`decision.made` 事件携带人类审批的 decision/reasoning/request_summary，被转换为 user 消息追加到对话，触发下一轮推理。这是 hiw 与 reason worker 之间的审批协作通道。

## 设计原则

1. **process 不调 LLM。** 事件处理是微秒级的，只做路由和状态更新。推理从 tryReason 发起。
2. **事件侧与推理侧解耦。** 事件只改状态 + 设 needReason；推理侧只消费 needReason，不反向推断 cause。
3. **reason 异步。** reason() 跑在独立 goroutine，watch 事件循环保持响应——LLM 秒级调用期间仍能实时处理 abort / input / tool 结果，`cancelReason()` 能真正打断在飞的推理。
4. **reason 不递归。** 一次推理一个结果。链通过 tryReason 的自然重入形成（isReasoning 保证同时至多一轮）。
5. **并发安全靠锁 + 快照。** reason 读 messages 用 `slices.Clone` 快照（LLM 拿稳定副本，process 并发追加 w.messages 不冲突）；所有共享字段（isReasoning/needReason/cancelReason/currentTraceID/tickafter map）都在 w.mu 下读写。
6. **没有内部队列。** process 微秒级消费，busCh 64 缓冲实时排空。
7. **特殊性不外泄。** tickafter 在总线上和 read_file 完全相同，唯一的"特殊逻辑"在 callTracker 内部的多一条动作。
