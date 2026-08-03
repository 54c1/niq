# Program — the source code of tool calls

> niq runs programs that haven't been written yet.

This tagline has two readings.

**Surface reading:** tool calls are programs generated on the fly by the LLM.
They did not exist a moment ago; the LLM writes them and the worker executes
them immediately.

**Deep reading:** the LLM Worker can also produce new Program source code
(Prompt or Script), refine existing Programs based on experience, and persist
them for future runs. It doesn't just compile and execute — it writes its own
tools. This is the meta capability: a system that produces, evaluates, and
improves its own programs over time.

A Program is source code. Its compiled or interpreted output is **tool calls**.

There are two languages a Program can be written in:

| Language | Kind | Compiler / Interpreter | Output |
|---|---|---|---|
| **Natural language** | Prompt Program | The LLM reads it, "compiles" it by reasoning, and produces tool calls as output. | Tool calls |
| **Formal DSL** | Script Program | A built-in interpreter executes it directly. The interpreter's output is tool calls. | Tool calls |

Both produce the same thing: tool calls. The worker then executes those tool
calls, producing side effects in the world. In this sense, a tool call is the
**executable form** of a Program — it has an entry point (the tool name),
arguments (the input), and when executed, it produces side effects.

## Relationship to "Skill"

Program is niq's equivalent of what ecosystems like LangChain, OpenAI GPTs, and
Claude call a "skill". The key difference: a traditional skill is a package of
instructions + executable files, stored in the workspace filesystem and loaded
by path. A Program is decoupled from the workspace — it can be stored in config,
memory, or loaded from any address via a Resolver.

Program reimagines "skill" not as a packaged executable with uncontrolled side
effects, but as source code whose single, well-defined output is tool calls.

## Two Types

| Type | How it's consumed | Output |
|------|------------------|--------|
| **Prompt** | Injected into the LLM context. The LLM reads and "compiles" it. | Tool calls |
| **Script** | Registered as a `script__{Name}` tool, callable by the LLM. An interpreter "compiles" it. | Tool calls |

Prompt and Script are the same kind of thing — a Program — expressed in
different languages for different consumers.

### Prompt Program

A natural-language program. It describes domain knowledge, rules, procedures, or
examples. The LLM "compiles" it on the fly — reading the text, integrating it
into its reasoning, and producing tool calls as the compiled output.

### Script Program

A formal-language program. It contains deterministic logic encoded in a DSL
(domain-specific language). A built-in interpreter executes it, and the
interpreter's output is tool calls.

Unlike a traditional skill's executable, a Script Program does not have
uncontrolled side effects. It does not write files, start processes, or make
network calls directly. Its sole purpose is to inspect the current context and
compose the appropriate sequence of tool calls. The worker then executes those
tool calls, which is where side effects happen.

This convergence gives Script Program a clean boundary: it lives inside the LLM
Worker, outputs only tool calls, and its lifecycle is fully managed by the
worker's tool call loop.

### Unification

Both types follow the same pipeline:

```
source code (natural or formal) → compilation/interpretation → tool calls → execution → side effects
```

The "compilation" step differs — LLM for Prompt, interpreter for Script — but
the output format is identical. This is why both are called "Program": they are
the same abstraction at different points on the formality spectrum.

## Relationship to Lisp

niq shares a deep kinship with Lisp. Both are built on the same insight: **code
is data**, and therefore programs can write programs.

| Lisp | niq |
|------|-----|
| **S-expression** — the fundamental unit, can be data or code | **Program (logical unit)** — the fundamental unit, can be data or code |
| `eval` evaluates an expression as code | The LLM (for Prompt) or interpreter (for Script) "evaluates" a Program into tool calls |
| **Macro** generates new code at compile time | The LLM Worker can generate new Programs at runtime (meta capability) |
| **REPL** — read, eval, print, loop | **LLM Worker loop** — read event, compile program, execute tool calls, collect results, loop |
| Homoiconicity — the language manipulates its own code | Program is data — it can be loaded, passed, stored, and later executed as code |

In Lisp, there is no fundamental distinction between code and data — they share
the same representation (the S-expression). In niq, there is no fundamental
distinction between a Prompt and a Script — they share the same abstraction
(the Program). The difference is only which language they are written in and
which evaluator processes them.

Program is to niq what the S-expression is to Lisp.

## Instruction

*Note: Instruction is defined in the Worker, not in this package. It is
mentioned here because it pairs with Program conceptually.*

Instruction and Program are a deliberate pairing:

```
Instruction: the worker's job description — static, defines boundaries,
             set at construction, always present.
Program:     the worker's knowledge and scripts — dynamic, loaded on demand,
             swappable at runtime via update_worker.
```

Instruction says "who you are". Program says "what you know and can do".
Both end up in the LLM's context window, but through different paths and for
different purposes.

## Progressive Loading

Program supports progressive loading via the Path field:

| Path value | Meaning | Content source |
|---------|------|-------------|
| `""` or `"."` | Inline | Set directly in Config |
| `"builtin://code-review"` | Builtin registry | Resolver looks up a map |
| `"config://safety-rules"` | Config store | Resolver fetches by key |

The address scheme is defined by the injected Resolver implementation. The core
package makes no assumptions about path formats.

## Consumption in the LLM Worker

```
PromptType:
  instruction + prompt programs → buildInstruction() → LLM context

ScriptType:
  each program → registered as script__{Name} tool → LLM can call it
  → handleScriptCall() → executor → tool call loop continues
```

## Design Principles

- **Program is data, not code.** Worker is code; Program is the logical body a worker loads.
- **Program is not bound to a workspace.** Skills depend on filesystem paths; Programs depend on abstract addresses (Path + Resolver).
- **Program enables meta-capabilities.** A parent worker can generate new Programs at runtime and use them to create child workers.
- **Tool calls are executable programs.** A tool call — name + arguments — is the executable form of a Program. niq produces and runs these programs in real time.

## Relationship to Existing Skills

niq does not reject the existing skill ecosystem. Traditional skills coexist with
Programs through the **Workspace Daemon**:

- Skills are installed in the workspace filesystem, as they always have been,
  and the Workspace Daemon helps workers load and invoke them as workspace tools.

The distinction is **where the logic lives** and **what it outputs**:

- **Program**: lives inside the LLM Worker. Its output is tool calls. Fast,
  predictable, and tightly integrated with the worker's reasoning loop.
- **Workspace Skill**: lives in the workspace filesystem. Its output is
  arbitrary (files, side effects). Accessed through the Workspace Daemon.

Both can coexist in the same system. A worker can load Program(s) for its
core reasoning patterns and also call workspace tools via the daemon when
arbitrary execution is needed.
