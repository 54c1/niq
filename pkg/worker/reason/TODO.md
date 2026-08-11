# reason — TODO

记录 reason worker 已知的待办 / 设计后续。

## 1. 流式打断：保留已产出的内容到 messages

**现状**：reason() 已异步化（跑在独立 goroutine），`cancelReason()` 能真正打断在飞的 LLM 调用。但打断的语义是"整轮作废"——reason 在 Phase 1 用 `slices.Clone(w.messages)` 做快照，LLM 调用被打断后走 `reason.end(stop_reason="interrupted")` 直接收尾，**已经产出的部分内容（thinking / 部分 text）没有保留**。

**待办**：流式打断时应把已生成的 thinking / 文本片段保留进 `w.messages`，让被打断的内容成为对话上下文的一部分，而不是整轮丢弃。需要：
- LLMProvider 支持流式（`CompleteStream`）+ 断点续传 / 部分结果回收
- reason 决定"已产出多少算有效"、如何拼装成 assistant 消息
- 与占位符 / 工具调用（若已发出）的衔接

## 2. 对外广播打断事件的细粒度

**现状**：打断时只广播一个 `reason.end(stop_reason="interrupted")`，粒度较粗。

**待办**：考虑是否需要一个专门的打断事件（或更细的中间事件），携带：
- 已保留的内容 / 保留了多少
- 中断原因（abort / input 抢占 / 其他）
- 以便前端（webui）能区分"主动打断""用户输入抢占"等场景，做对应的 UI 呈现
