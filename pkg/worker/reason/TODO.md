# reason — TODO

记录 reason worker 已知的待办 / 设计后续。

## ✅ 1. 流式打断：保留已产出的内容到 messages (已实现)

**实现**：
- `reasonCtx.Done()` 分支在 drain 前保存 `thinkingBuf`/`textBuf` 内容，flush 后构建 `llm.Message{Role: RoleAssistant, StopReason: "interrupted"}` 追加到 `w.messages`
- 由 `watch.go` 中的 `handleAbort`（`w.interruptReason = "abort"`）和 `handleInput`（`w.interruptReason = "input"`）在调用 `cancelReason()` 前设置中断原因
- `reason()` Phase 3 广播 `reason.interrupted` 事件携带中断原因和保留量

## ✅ 2. 对外广播打断事件的细粒度 (已实现)

**实现**：
- 新增 `reason.interrupted` 事件，携带 `reason`（abort/input/unknown）、`preserved_chars`、`preserved_text`
- 在 `reason.end(stop_reason="interrupted")` 之前广播，确保消费者能区分中断场景
