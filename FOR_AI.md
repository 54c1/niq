# niq

给未来 AI 的文档。

## 如何阅读本文档

本文档不是使用手册，不是 API 参考，不是架构说明。
它是 niq 的"思维模型"——你读完它之后，应该能理解 niq 为什么长成现在这个样子，
以及它的每一个设计决策背后的推理链。

可以结合 `core/` 的代码读本文档

---

## 一句话

**niq 是一个实时产生并执行程序（tool call）的系统。Worker 是生产者和执行者，
Program 是源码，Event 是通信语言，总线是这些元素协作的介质。**

niq 没有"会话"（Session），没有"Agent 循环"（Agent Loop）。
niq 是一群 Worker 的集合——一个 Worker Swarm。这群 Worker 放在一起，才是一个完整的 Agent。

---

## 核心追问

niq 的起点不是"我要有什么功能"，而是一个问题：

> **一个 AI Agent，本质上在做什么？**

答案是：**它在生产程序，然后执行它们。**

用户请求是输入，Agent 的工作是生成一连串操作指令——先调用什么、再调用什么、
参数是什么——然后交给运行环境去执行。这些指令就是程序。它们运行在 Agent 和
外部世界之间。没有这一层"程序"，Agent 就只是一个聊天机器人。

这个答案不是"选择一个抽象"——它是"推导出所有抽象"的起点。

---

## 三个概念

niq 只有三个核心概念。它们不是从功能清单里选出来的——它们是从上面那个问题推导出来的。

### Worker

Worker 是能做事的单元。它订阅事件，处理事件，发布新事件。

所有功能——LLM 推理、工具执行、安全拦截、外部接入、生命周期管理——都是 Worker。
没有"Agent"、"Plugin"、"Hook"、"Middleware"、"Controller"、"Supervisor"等第二套概念。
niq 只有一个扩展概念：Worker。

Worker 的力量来自**平等性**。没有 Master Worker，没有调度 Worker，没有特权 Worker。
它们全通过事件总线通信，任何一个 Worker 能做的事，另一个 Worker 也能做。
Worker 的语言无关——任何语言，只要能接入事件总线，就能成为 niq 的一个 Worker。

Worker 的接口极简：

```go
type Worker interface {
    ID() string
    Start(ctx context.Context) error
    Subscriptions() []EventPattern
}
```

一个 Worker 订阅事件模式，从总线接收匹配的事件，向总线发布新的事件。
这就是全部抽象。

### Program

Program 是 Worker 能力的源码。它有两个维度：

**ContentType（内容类型——"用来说什么的"）：**

| ContentType | 作用 | 生命周期 |
|---|---|---|
| `instruction` | 纲领性约束，始终在场的指导原则——可以是 Worker 的身份定义，也可以是客观的业务规则或限制 | 始终在场 |
| `playbook` | 过程性操作步骤，描述"怎么做" | 按场景按需加载 |

**FormType（内容形态——"用什么语言写的"）：**

| FormType | 消费方式 | 编译者 |
|---|---|---|
| `prompt`（自然语言） | 注入 LLM 上下文 | LLM 在推理时"编译" |
| `script`（形式化 DSL） | 注册为工具，解释器执行 | Program Worker 的 DSL 解释器 |

关于 script 需要理解两个视角：

**第一，Script Program 是 niq 对传统脚本的答案。** 传统 skill 中的脚本存在的目的是执行确定性逻辑——有些事不需要 LLM 重新编译，确定性程序反而更可靠。Script Program 用 DSL 描述工具调用序列，由 Program Worker 编译为 tool call，不走 LLM。

**第二，传统脚本在 niq 中依然可以使用，但被降级为一次依赖文件系统的 tool call。** 它们不会直接打包在 Program 中——Program 中只保留它们的下载链接或安装命令（如 `npm install`、`curl -O`）。Reason Worker 在需要时通过 Workspace Worker 下载、安装、执行，完成后清理。这是 niq 不依赖文件系统的设计原则的体现——Workspace Worker 是可选的，Script Program 才是核心。

两者走同一条流水线：

```
源码 → 编译/解释 → tool call → 执行 → 副作用
```

差异只是编译器的差异，不是程序的差异。

Program 有三层结构：

```
Program（现实世界中的 skill 包）
  ├── ContentType: instruction | playbook
  ├── Name: "code-review"
  ├── Path: "builtin://code-review"     ← 可寻址，支持渐进式加载
  │
  └── EntryContent: ProgramContent      ← 入口，总是一开始加载
        ├── FormType: prompt | script
        ├── Content: "这个 skill 帮助你做代码审查。它包含以下内容..."
        └── Path: "README.md"
              │
              └── 渐进式加载其他 ProgramContent
                    ├── {FormType: prompt, Path: "rules/go.md", Content: "..."}
                    ├── {FormType: script, Path: "lint/run.sh", Content: "..."}
                    └── {FormType: prompt, Path: "rules/python.md", Content: "..."}
```

类比现实世界的 skill 打包：

```
skill/code-review/           ← Program
├── README.md                ← EntryContent (prompt, 描述这个 skill)
├── rules/                   ← 其他 ProgramContent
│   ├── go.md                ← prompt
│   └── python.md            ← prompt
└── lint/
    └── run.sh               ← script (DSL 描述的工具调用序列)
```

### Event

Event 是 Worker 之间唯一的通信语言。Worker 之间不直接调用函数，不持有对方的引用，
不知道对方是否在线，不知道对方在哪里运行。它们只发布事件，也只响应事件。

```go
type Event struct {
    ID             string
    Type           string
    Payload        map[string]any
    WorkerId       string
    TargetWorkerID string   // 定向投递，可选
    TraceID        string   // 追踪链路
}
```

这让系统彻底解耦：一个 Worker 被替换了，其他 Worker 不需要改代码。
一个 Worker 崩溃了，其他 Worker 不受影响。远程 Worker 和本地 Worker 在通信上没有区别。

### 关系

```
Worker 是能做事的单元
  → 加载 Program（领域知识 / 可执行逻辑）
    → 消费 Event（接收外部信号）
      → 处理：推理 + 工具调用
        → 生成新的 tool call（Program 的可执行文件）
          → 发布 Event（通知其他 Worker）
```

三个概念，一个闭环。

---

## 关键洞察

### 1. Agent Loop 不是智能的根本属性

主流 Agent 框架都建立在一个共同的假设之上：agent 就是一个循环。

```
while not done:
    think()
    act()
    observe()
```

这个循环是原子化的，不可分割的。它拥有 agent 的全部状态，控制整个执行流程，
并屏蔽所有外部输入。工具调用是同步的——`await tool.execute()` 阻塞整个循环直到结果返回。

niq 从一个不同的观察出发：**循环是同步执行模型的实现副产品，不是智能的根本属性。**

niq 将"循环"拆解到事件总线上：

| 循环组件 | niq 等价物 |
|---|---|
| `await get_next_message()` | `watch()` —— 从 channel 接收事件，channel 由总线填充 |
| `llm.think()` | `reason()` —— 单次 LLM 调用，没有包裹它的循环 |
| `await tool.execute()` | `callTracker.Request()` —— 发布 `tool.requested`，立即返回 |
| `append_to_history(result)` | `callTracker.handleResponse()` → `updatePlaceholder()` |
| `while not done` | **不存在。** 循环从事件在总线上的流转中涌现 |

"循环"不在任何单个函数或任何单个 Worker 内部——它是拓扑的属性，不是任何组件的属性。

**一个具体的例子：**

用户发了一条消息"帮我检查这个 PR"，整个系统的协作路径是这样的：

```
1. hiw 发布 worker.input("帮我检查这个 PR")
2. Reason Worker 收到，推理，决定需要 workspace.read_file
3. 发布 tool.requested → Workspace Worker 收到，读取文件
4. 发布 tool.completed → Reason Worker 收到，更新占位符
5. 再次推理，决定需要 workspace.bash("go vet")
6. 发布 tool.requested → Workspace Worker 执行
7. 发布 tool.completed → Reason Worker 收到
8. 再次推理，生成文本响应
9. 发布 reason.response → hiw 收到，展示给用户
```

每一步都在"接收 → 思考 → 发布 → 返回"。没有 while 循环，没有同步等待。
持续运转是拓扑的属性，不是任何组件的属性。

### 2. 自然 = Reason Worker 也能理解

niq 的设计原则之一是"简单才能可靠"。但"简单"还有一个更实际的意义：

**niq 的机制必须简单到让 Reason Worker（LLM）也能理解。**

如果一个系统的机制不自然，有太多特例、太多隐藏规则，Reason Worker 就没法在推理中
准确地理解和使用它们。它可能想创建一个验收 Worker，但不知道新 Worker 该订阅什么事件；
它可能想用 timer worker 来做超时控制，但不知道 timer 是事件而不是回调。

"自然"的真正意义是：Reason Worker 不需要额外学习就能理解系统的运作方式，
因此它可以自主地重现、组合、扩展这些模式。这是元能力的基础。

```
自然 → Reason Worker 可以理解 → Reason Worker 可以自主使用 → 元能力
```

**一个具体的例子：** 在 niq 中，如果你想要"feature 开发完成后自动验收"这个模式，
你不需要学习任何新概念：

1. 创建一个验收 Reason Worker，订阅 `feature.completed` 事件
2. 在 feature 开发 Reason Worker 中，完成时发布 `feature.completed` 事件
3. 验收 Worker 收到事件，开始验收推理
4. 如果需要，验收 Worker 可以设置 timer 来检查部署状态

每一步都是"创建一个 Worker，订阅一个事件，发布一个事件"。
没有框架 API，没有编排配置，没有新概念。Reason Worker 可以理解这个模式，
然后自主地创建类似的模式——这就是元能力。

### 3. Worker 是平等的，没有特权

niq 最容易被误解的设计是"一切皆 Worker"。这不是一句口号——它是一个在执行的设计约束。

niq 中所有"听起来像核心"的能力，都通过事件总线以 Worker 行为表达：

- **生命周期管理**：HostWorker 是一个 Worker，暴露 `spawn`/`list`/`destroy` 工具，
  通过 `tool.requested` 事件被调用，和 `workspace.read_file` 走完全相同的路径。
- **Program 管理**：Program Worker 是一个 Worker，暴露 `search`/`load`/`execute` 工具，
  和 GitHub Worker 暴露 `create_pr` 工具没有区别。
- **用户界面**：hiw 是一个 Worker，订阅 `reason.response`，发布 `worker.input`。
- **定时任务**：Timer Worker 是一个 Worker，订阅 `timer.set`，发布 `timer.elapsed`。

没有特权 API，没有隐藏通道，没有"只有核心才能做的事"。

**"控制面在数据面中"的精确含义：**

控制面的能力可以被 Worker 以事件的形式暴露给其他 Worker，前提是这个 Worker 被信任持有控制面凭证。
控制面和数据面是两个正交的领域，但控制面的能力可以通过数据面表达。

HostWorker 持有 `*svcbus.Bus` 引用不是特权——它是被部署时注入了信任，
和 GitHub Worker 持有 GitHub Token 是同一类事情：

```
GitHub Worker
  ├─ 持有的凭证：GitHub Token（离线授权）
  ├─ 暴露的工具：create_pr、list_issues（通过 worker.ready 发布）
  └─ 其他 Worker 通过 tool.requested 调用这些工具

HostWorker
  ├─ 持有的凭证：*Bus 引用（离线授权）
  ├─ 暴露的工具：spawn、list、destroy（通过 worker.ready 发布）
  └─ 其他 Worker 通过 tool.requested 调用这些工具
```

**HostWorker 没有特权——它只是持有一个特定的凭证。** 就像 GitHub Worker 持有 GitHub Token，
HostWorker 持有 Bus 控制面 Token。只是恰好这个凭证的名字叫"控制面"。

### 4. 人不是必须的

主流 Agent 的"会话"形态，本质上是一个默认人在场的假设：

```
用户输入 → Agent 思考 → 用户输入 → Agent 思考 → ...
```

人不在场的时候，系统就休眠了。会话是"用户和系统之间的通道"，系统没有独立的生命。

niq 的假设是相反的：

```
系统在运行 → 系统在运行 → 系统需要人 → hiw 通知人 → 人接入 → 人离开 → 系统继续运行
```

**人不在场是默认状态，人在场是例外。**

所以 hiw 的寓意不是"人机交互界面"，而是**"人不是必须的，但当他需要时，他有一个 Worker"**。
这个定位让 niq 的所有其他设计都变得合理了——定时任务、自动审批、夜间"做梦"——
这些在主流系统中需要额外解释的特性，在 niq 中是自然的，因为它们都建立在"人不一定在"这个假设之上。

**hiw 和上层应用的关系：**

niq 坦诚地承认：hiw 作为数据面上的一个 Worker，它的视角是有限的。
它只能看到事件，只能发布事件。它不能管理 Worker 生命周期，不能修改总线配置。

如果一个桌面客户端需要同时提供"用户交互界面"和"系统管理界面"，
它应该同时持有两个权限：

```
桌面客户端
  ├─ 持有的凭证：Bus 的 API key（控制面）
  │     └─ 用途：凭证管理、启停 Worker、配置总线
  │
  └─ 持有的身份：hiw（数据面，一个 Worker）
        └─ 用途：发 worker.input 事件，收 reason.response 事件
```

两个权限分开获取，分开使用，只是恰好被同一个进程持有。
这不是"一个特权身份的两种表现"——它们是两个独立的权限。

### 5. 总线协议是唯一的不变

niq 的核心不是代码，不是组件——niq 的核心是**协议**。

```
Event 结构 + EventPattern 匹配 + Subscribe/Publish/Receive
```

这是 niq 不可再简化的核心。所有其他东西——总线的实现、Worker 的实现、LLM 的接入——
都是可替换的。当前的内存总线可以替换为分布式总线，以适应企业级部署。
Worker 不需要知道总线是怎么实现的——它们只知道 `Subscribe`、`Publish`、`Receive`。

协议比实现长寿。

---

## 架构

### Worker Swarm（不是 Agent）

niq 不是"一个 Agent"。niq 是一群 Worker 的集合——一个 Worker Swarm。
这群 Worker 放在一起，才是一个完整的 Agent。

系统自带的 Worker：

| Worker | 职责 | 关键订阅 | 关键发布 |
|---|---|---|---|
| Reason Worker | LLM 推理节点，产生 tool call | `worker.input`, `tool.completed` | `tool.requested`, `reason.response` |
| Workspace Worker | 文件系统和命令执行 | `tool.requested` | `tool.completed` |
| Timer Worker | 时间事件 | `timer.set` | `timer.elapsed` |
| hiw | 人机交互界面 | `reason.response`, `event.delivered` | `worker.input` |
| Host Worker | Worker 生命周期管理 | `tool.requested`, `worker.discover` | `worker.ready`, `worker.gone` |
| Program Worker | Program 注册、查找、编译 | `tool.requested` | `worker.ready` |
| HTTP Worker | 网络工具 | `tool.requested` | `worker.ready` |
| niw | 跨 niq 总线桥接 | 双向事件转发 | 双向事件转发 |

每一个 Worker 只做一件事，但它们通过事件总线协作，形成一个比任何单一 Agent 更强大的整体。

### Reason Worker 的事件循环

Reason Worker 是 niq 中最核心的 Worker。它的内部结构值得深入理解：

```
watch()  ← 单 goroutine，阻塞在 busCh
  │
  ├─ process(evt) → 五路分派（微秒级，纯状态路由）
  │     ├─ worker.abort     → 取消推理 + 清空所有 pending 工具
  │     ├─ timer.elapsed    → 超时标记 pending 工具，或唤醒提醒
  │     ├─ capability       → worker.ready/gone 更新工具集
  │     ├─ tool_result      → 更新占位符 + 检查是否全部 resolved
  │     └─ input            → 追加 messages + 设 needReason
  │
  └─ tryReason() → 决策门
        └─ needReason && !isReasoning → reason()
```

关键设计点：

- **process 不调 LLM**：事件处理是微秒级的，只做路由和状态更新。推理从 tryReason 发起。
- **reason 不递归**：一次推理只产生一个结果。链式反应通过 tryReason 的自然重入形成。
- **没有内部队列**：busCh 64 缓冲在微秒级消费下永远空着。

**工具生命周期：**

```
ToolCallTracker 管理同批工具调用

ToolPending
  ├─ ToolCompleted   ← 正常返回
  ├─ ToolFailed      ← 执行错误
  ├─ ToolRejected    ← 被拦截/取消
  ├─ ToolTimedOut    ← 超时（set_tool_timeout 触发）
  └─ ToolInterrupted ← 高优消息打断（input_mode: interrupt）
```

**占位符方案（Transcript 保护）：**

LLM API 要求 `assistant(tool_calls)` 和后续 `tool_result` 之间不能插入非 tool_result 消息。
niq 采用占位符方案：

1. 产出工具调用时，立即在 messages 中插入 `tool_result(pending)` 占位符
2. 结果到达时，`updatePlaceholder` 原地更新
3. 迟到结果（超时/打断后到达），`appendLateResult` 追加为 user 消息

**超时保护机制：**

```
reasonEpoch         ← 每次 reason() 递增
activeTickafters    ← map[timerID]epoch，当前批次的超时定时器

保护机制：
- timer.elapsed 到达时，检查 epoch 是否匹配
- 所有工具 resolved 后，cancelActiveTickers() 发送 timer.cancel
- 旧批次的 stale 定时器被静默丢弃
```

### 事件总线

总线是 niq 的脊柱。它做的事非常少：匹配订阅模式、投递事件、持久化事件。
它不做的事非常多：不持有 Worker 状态、不管理生命周期、不做负载均衡、不执行安全策略。

路由有三个维度：

1. **类型匹配**：精确匹配、通配符（`*`）、前缀匹配（`prefix.*`）
2. **定向投递**：如果设置了 TargetWorkerID，只投递给指定 Worker
3. **来源匹配**：可选的 SourceID 过滤

总线在数据面上有三个检查点：

| 操作 | 检查内容 |
|---|---|
| **Publish** | 发布者是否冒充他人？事件类型是否在 PublishAllow 中？ |
| **Subscribe** | 订阅模式是否在 SubscribeAllow 中？ |
| **Route** | 纯元数据匹配，不做身份验证 |

### 系统自省

总线持久化所有事件。这意味着系统自身的运行历史是可以被查询、被分析的。

- **实时视角**：`event.delivered` 事件，总线在投递后自动发布，hiw 订阅以获得实时可见性
- **历史查询**：`MemoryBus.List` API，按 workerID、traceID、时间范围过滤

这两个机制让系统自身成为可观察的。未来的"做梦 Worker"可以在夜间审计事件日志，
发现低效的环节、缺失的能力、冗余的流程，并自动改进系统。

---

## 信任模型

niq 有两层凭证：

| 层 | 名称 | 用途 | 验证方式 |
|---|---|---|---|
| 数据面 | Token | 连接时的身份凭证 | 在线验证，绑定到 WorkerID |
| 控制面 | API key | 总线管理 API 的离线凭证 | 离线授权，用于签发 Token 和写入注册表 |

三种连接方式：

| 连接方式 | 凭证 | 信任级别 |
|---|---|---|
| 进程内（InProcessClient） | 无 | 同进程即信任 |
| HTTP loopback | 无 | 本地地址即信任 |
| HTTPS 远程 | Token / API key | 需要验证凭证 |

**子集规则：**

Worker A 通过 spawn 创建 Worker B 时，Worker B 的初始注册表必须是 Worker A 注册表的子集。
A 不能授予自己都没有的权限。这是最小权限原则的体现——权限不会通过创建操作放大。

对于持有 API key 的管理程序，此规则不适用——API key 本身赋予了对注册表的完全控制权。

---

## 元能力

niq 最有力量的能力是：**Worker 可以创建 Worker**。

Reason Worker 在推理过程中发现需要一个新的 Worker 来处理子任务，
它通过 HostWorker 的 `spawn` 工具创建一个新的 Worker，赋予它目标和 Program。
新 Worker 独立工作，完成后发布结果，父 Worker 收到结果后继续推理。

**具体的 spawn 流程：**

```
Reason Worker 推理 → "这个 bug 分析需要独立的上下文"
  → 调 spawn 工具（通过 tool.requested 事件）
  → HostWorker 收到
  → 调用 RegisterWorker（控制面 API，持有 API key）
  → 创建新 Worker 实例
  → 新 Worker 发布 worker.ready
  → Reason Worker 发现新 Worker 的工具
  → 父 Worker 通过 publish_message 给子 Worker 发送任务
  → 子 Worker 独立工作，完成后发布结果
  → 父 Worker 收到结果，继续推理
```

这个能力是递归的——子 Worker 也可以创建孙 Worker。
系统的拓扑是从内部自组织地生长出来的，不是外部预先设计的。

**递归性来自于平等性，而不是来自于一个"递归"开关。**
spawn 是工具，不是特权 API，所以递归是自然的，不需要额外设计。
工具发现机制不区分"你是父 Worker 还是子 Worker"——它只看"你是不是总线上的一个 Worker"。

同样，Program 也可以被运行时生成和注册。
Reason Worker 可以编写新的 Program，注册到 Program Worker，然后立即使用。
niq 是一个会写自己的工具的系统。

---

## 设计原则

### 简单才能可靠

这是 niq 最根本的架构约束。"简单"不是指功能少——而是指概念少、依赖少、例外少。
每一个新增的概念都会增加系统的认知负担和运行时的不确定性。

niq 的选择是：在增加一个概念之前，先问能不能用已有的概念表达这个需求。
大多数时候，答案是能。

这个约束体现在 niq 的每一层：

- **一个扩展概念**：Worker。没有 Plugin、Hook、Middleware、Controller、Supervisor。
- **一个通信方式**：事件。没有 RPC、没有直接函数调用、没有共享内存。
- **一个集成方式**：接入总线。没有 SDK 绑定、没有语言限制、没有版本对齐。
- **一个可替换单元**：Worker。总线的实现可以换，LLM 可以换，所有系统组件都可以换。

简单不是起点，简单是持续抵抗复杂化的结果。

### 一切皆 Worker

niq 只有一个扩展概念：Worker。没有特例，没有特权通道。
控制面在数据面中——HostWorker 通过同一个事件总线管理 Worker 生命周期，
Program Worker 通过同一个事件总线管理 Program 注册和编译。

### 所有组件可替换

总线的实现可以替换，Worker 的实现可以替换，底层 LLM 可以替换。
niq 的核心不是代码，不是组件——niq 的核心是协议。

### 概念不扩张

当新需求出现时，niq 的答案是：用已有的概念表达它。
一个新需求不是一个新概念，它只是一个新的 Worker，
或者一个已有的 Worker 加载了一个新的 Program。

---

## 自然涌现的模式

niq 的架构让一些在主流系统中需要"额外设计"的模式变得自然。

### Timer 的三个模式

Timer Worker 在 niq 中不只是"定时任务"。它让时间成为总线上的一种事件，
和 `tool.completed`、`worker.input` 没有任何区别。这带来了三个模式：

**1. 超时控制：** Reason Worker 发起工具调用时，同时设置一个超时 timer。
如果工具在超时前返回，cancelActiveTickers 取消 timer。
如果超时先到，timer.elapsed 触发，Reason Worker 标记 pending 工具为超时，继续推进。
迟到结果不会被丢弃——appendLateResult 追加到上下文，LLM 在下一轮会看到。

**2. 定时任务：** Reason Worker 在推理结束时设置一个 10 分钟的 timer，然后进入完全休眠。
10 分钟后 timer.elapsed 到达，被唤醒，检查状态，继续推理。
等待期间不消耗 CPU，不持有锁，不占用任何资源。

**3. 主动探索：** Reason Worker 在空闲时设置一个"探索 timer"。
触发时，review 今天的推理记录，发现模式，更新自己的 Program。
这不是"定时任务"——这是 Agent 主动发起自我改进。

这三个模式在主流系统中需要三个不同的基础设施（timeout 参数、cron、没有现成机制），
在 niq 中都是同一个 Timer Worker 提供的同一个机制：`timer.set` → `timer.elapsed`。
概念不扩张的又一个体现。

### 事件驱动的协作模式

**Loop Engineering：** 外层循环触发 agent 开始工作，替代用户的输入。
在 niq 中，这只是一个 Worker 订阅了特定事件。

**交付把关：** 每次 feature 开发完成，agent 退出到外层循环，外层循环校验交付质量，
可能触发进一步的任务完善。在 niq 中，这是一个专门的验收 Reason Worker，
订阅 `feature.completed` 事件——和任何其他 Worker 一样。

**审批流程：** 在主流系统中，"审批"是一个需要额外设计的流程。
在 niq 中，审批就是一个 Reason Worker，它的 instruction 说"只有符合安全规范的操作才批准"，
它订阅 `approval.requested` 事件，发布 `approval.granted` 或 `approval.denied`。
没有新概念，没有新机制。

### 元能力模式

**Worktree 管理：** Reason Worker 创建一个 workspace worker 来处理一个编码任务，
任务完成后 workspace worker 进入休眠。三个月后用户问"当时那个任务是怎么处理的"，
Reason Worker 被定向唤醒，回忆细节。

**动态专家网络：** Reason Worker 遇到一个需要特殊领域知识的子任务，
创建一个新的 Reason Worker，加载对应的 Program，子 Worker 独立工作，
完成后发布结果，销毁。

**系统自愈：** 一个 Worker 发现自己的状态不对，创建一个新的 Worker 替换自己，
迁移状态，销毁旧的 Worker。所有操作通过总线完成，不需要外部编排。

---

## 渐进式路径

niq 的渐进式路径让用户从"使用一个工具"自然过渡到"运营一个系统"：

```
阶段一：单人聊天
  └─ 一个 Reason Worker + hiw Worker
  └─ 看起来和任何 chatbot 没有区别，零门槛

阶段二：添加工具 Worker
  ├─ Workspace Worker → 文件操作、命令执行
  ├─ Timer Worker → 超时、定时任务
  └─ 用户开始觉得"这个 chatbot 能做点事"

阶段三：多 Reason Worker 分化
  ├─ 编码 + 审批 + 定时任务各一个 Reason Worker
  ├─ niq 不再是一个"对话"，而是一个"系统"
  └─ ✨ 第一个质变点

阶段四：元能力激活
  ├─ Worker 创建 Worker，系统开始"自然生长"
  └─ ✨ 第二个质变点

阶段五：团队协作
  ├─ 同事的 niq 通过 niw 接入
  ├─ 从"我的数字代理"变成"我们的数字代理"
  └─ ✨ 第三个质变点
```

每个阶段用户都能停留在当前阶段正常使用，没有强制升级。

---

## 与其他系统的区别

| 维度 | 主流 Agent | niq |
|---|---|---|
| 核心抽象 | Session / Conversation | Worker Swarm |
| 运行时 | Agent Loop（while 循环） | 事件驱动（watch 循环） |
| 推理节点 | 单一，串行 | 多个 Reason Worker，不同目标 |
| 上下文 | 消息列表线性累积 | 可插拔策略，按目标构建 |
| 扩展方式 | 新增概念（Plugin、Hook、Tool、Memory...） | 新增 Worker |
| 概念数量 | 随时间增长 | 固定为 3（Worker、Program、Event） |
| 系统存续 | 依赖会话持续 | 定时器、文件变更、外部事件均可驱动 |
| 人的地位 | 必须（驱动者） | 可选（参与者） |
| 元能力 | 无（Agent 不能创建 Agent） | 有（Worker 创建 Worker） |
| 组件可替换 | 核心不可替换 | 所有组件可替换，包括总线 |
| 热更新 | 需要框架支持 | 天然（Worker 是独立总线节点） |
| 语言支持 | 绑定宿主语言 | 协议层面语言无关 |

---

## 如何进一步理解 niq

1. 读 `core/` 下的代码——总线协议、Worker 接口、Event 结构、LLM 接口
2. 读 `pkg/worker/reason/`——Reason Worker 的实现，理解事件循环和工具生命周期
3. 读 `pkg/worker/host/`——HostWorker 的实现，理解"控制面以 Worker 形态存在"
4. 读 `pkg/service/bus/bus.go`——总线的实现，理解身份注册、ACL、路由
5. 读 `doc/design/` 下的设计文档——理解每个设计决策背后的推理链
6. 读 `doc/dev_notes/thoughts/concept.md`——理解"Reason Worker 不是会话"的洞见

读完这些，你应该能理解 niq 为什么长成它现在的样子。

---

**niq runs programs that haven't been written yet.**
