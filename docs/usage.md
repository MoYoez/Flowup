# 使用指南

[返回 README](../README.md)

这里介绍工作流的写法、模型配置，以及审批和失败处理。命令都在仓库根目录执行；安装和第一个本地示例见 README。

## 查看运行记录

每次运行都会输出一个 `run_id`。把下面的 `RUN_ID` 换成它：

```sh
flowup status RUN_ID
flowup trace RUN_ID
```

`status` 显示步骤状态，`trace` 每行输出一个 JSON 事件。记录默认保存在当前目录的 `.flowup/flowup.db`。需要使用其他位置时，通过 `--db` 指定文件，并在后续查询、审批和恢复时使用同一个路径。

## 工作流怎么写

[本地分流示例](../examples/v1/local-triage.yaml)里，主要是这几部分：

| 字段 | 用来做什么 |
| --- | --- |
| `inputs` | 声明运行时需要传入的数据 |
| `steps` | 按顺序列出要做的事 |
| `id` | 给这一步起个名字，后面可以引用它 |
| `uses` | 选择一个内置动作 |
| `with` | 把参数传给这个动作 |
| `if` | 可选条件；不满足时跳过这一步 |
| `outputs` | 指定整个流程最后输出什么 |

步骤里的引用写成 `${{ ... }}`。例如：

```yaml
with:
  value: ${{ inputs.payload }}
  paths:
    title: title
    priority: priority
```

这会把输入的 `payload` 对象交给 `json.select`，取出两个字段。后续步骤用 `${{ steps.selected.output.priority }}` 读取其中的优先级。

完整引用会保留数据类型，也可以把字符串或数字嵌入一段文字。步骤只能引用输入和前面步骤的结果。

`if` 支持引用、字面量、`==`、`!=`、`&&`、`||` 和括号。例如：

```yaml
if: ${{ steps.route.output == "approval" }}
```

这里的语法借鉴了 GitHub Actions，但 Flowup 不能直接运行 GitHub Actions 的工作流。

## 试试暂停和审批

这个例子也不用联网。它先等待确认，批准后输出结果：

```sh
flowup run examples/v1/local-approval.yaml --inputs examples/v1/local-approval.inputs.json
```

终端会显示 `waiting_approval`，并给出 `approval_id` 和批准、拒绝命令。程序此时已经退出，你可以稍后再决定。

当前 CLI 不会在这段输出里展示审批说明或 `preview`。先用 `flowup trace RUN_ID` 查看 `approval.requested` 事件中的 `message`；这个示例把待确认文字写进了说明。`preview` 虽然已存入数据库，目前还没有直接查看它的命令。

批准时，把 `APPROVAL_ID` 换成刚才的实际 ID：

```sh
flowup approve APPROVAL_ID
```

批准会直接继续执行后面的步骤，最后输出：

```text
status: succeeded
output: {"result":{"approved":true,"message":"Ready to continue"}}
```

也可以拒绝：

```sh
flowup reject APPROVAL_ID --reason "需要先修改内容"
```

拒绝后整个运行结束，状态是 `rejected`。已经作出的审批决定不能再次更改；想分别体验批准和拒绝，需要重新 `run` 一次。

审批步骤由工作流作者明确放置。Flowup 不会自动为每个写操作增加审批，`validate` 的缺少审批提示也只是提醒。需要人工确认的动作，应在工作流里安排好审批步骤和执行条件。

## 接上 AI

可以先试 [文字分类示例](../examples/v1/ai-classify.yaml)。它只调用模型，把一条反馈分成 `bug`、`feature` 或 `question`，不需要 GitHub 或 Slack 凭据。

在当前终端设置环境变量。把占位内容换成你的实际配置。

PowerShell：

```powershell
$env:OPENAI_API_KEY = "你的 API Key"
$env:FLOWUP_AI_MODEL = "你的模型名称"
```

Bash / Zsh：

```sh
export OPENAI_API_KEY="你的 API Key"
export FLOWUP_AI_MODEL="你的模型名称"
```

然后运行：

```sh
flowup run examples/v1/ai-classify.yaml --inputs examples/v1/ai-classify.inputs.json
```

模型需要支持 Chat Completions 的 `json_schema` 严格结构化输出，以及请求里的 `temperature` 和 `max_completion_tokens` 参数。Flowup 会按工作流的 `output_schema` 再校验一次结果；分类是否准确仍需要你判断。

| 环境变量 | 说明 |
| --- | --- |
| `OPENAI_API_KEY` | 模型服务的 API Key |
| `FLOWUP_AI_MODEL` | 工作流写 `model: default` 时使用的模型名称 |
| `OPENAI_BASE_URL` | 可选，默认 `https://api.openai.com`；程序会在后面加上 `/v1/chat/completions` |

[model.env.example](../model.env.example)是配置参考。仅复制成 `model.env` 不会生效，当前程序不会自动读取这个文件。

模型收到工作流写明的 `prompt` 和 `with.input`，不会自动获得其他步骤的内容。它没有工具调用权限；请求使用非流式结构化输出。`output_schema` 要符合服务端要求，OpenAI 的严格模式要求每个对象设置 `additionalProperties: false`，详见[官方说明](https://developers.openai.com/api/docs/guides/structured-outputs)。

`ai.generate` 默认只尝试一次。想在输出不合要求或请求发生临时错误时再试，可以在 `with` 中设置 `max_attempts`，范围是 1–5。也可以用 `models` 列表替代 `model`，按尝试次序切换模型；尝试次数仍由 `max_attempts` 决定。

需要同时接 GitHub 和 Slack 时，可以参考 [triage-issue.yaml](../examples/v1/triage-issue.yaml)：提供 `issue_url` 输入，设置 `GITHUB_TOKEN` 和 `SLACK_TOKEN`，并把 `notify` 步骤的 `channel` 改成目标频道。这个例子会真的发送消息，其中只有高优先级分支需要审批。

## 目前有哪些动作

| 动作 | 用途 |
| --- | --- |
| `http.request` | 发起 HTTP 请求，返回状态码、响应头和响应体 |
| `json.select` | 按字段路径提取数据；支持对象的点号路径，不支持数组索引 |
| `json.validate` | 用 JSON Schema 检查数据，通过后原样返回 |
| `switch` | 按值选一个结果，必须提供 `default`；后续步骤用 `if` 决定是否执行 |
| `ai.generate` | 调用模型并校验 JSON 输出 |
| `approval` | 保存进度，等待人工批准或拒绝 |
| `github.issue.get` / `github.pull_request.get` | 读取 issue 或 PR 的基本信息；PR 动作不读取代码 diff |
| `github.issue.comment` / `github.pull_request.comment` | 发布评论 |
| `slack.message.get` / `slack.message.send` | 读取或发送消息 |

`validate` 检查语法、引用、动作和部分参数。凭据是否有效、服务端是否接受请求、运行时数据是否符合要求，还需要实际运行才能确认。

## 运行中断或失败时

先用 `status` 看当前状态，再用 `trace` 查看发生过哪些事件。

| 状态／错误 | 当前版本怎么处理 |
| --- | --- |
| `waiting_approval` | 用 `approve` 或 `reject` 作决定；`resume` 不会跳过审批 |
| `running`，但原进程已经退出 | 可以用 `resume RUN_ID` 从数据库记录的位置继续 |
| `failed` | `resume` 只返回原来的失败状态，不会重跑；修改输入或配置后，需要重新 `run` |
| `effect_indeterminate` | 无法确认外部写操作是否已发生，因此停止；需要先去目标服务核实，当前没有人工确认结果后继续的命令 |
| `succeeded` / `rejected` | 运行已经结束，再 `resume` 不会重新执行 |

如果之前可能已发出评论或消息，重新 `run` 前先确认目标服务的结果。新运行有新的 ID，不会自动继承上一次运行的写入记录。

重试也有区别：`http.request` 的 GET / HEAD 在连接错误、429 或 5xx 时默认最多尝试 3 次；写请求不自动重试。AI 按自己的 `max_attempts` 设置重试；当前 GitHub、Slack 读连接器没有自动重试。

暂停和恢复依赖同一个数据库。换目录执行命令时，记得用 `--db` 指定原来的文件；数据库路径包含空格时要加引号。不要同时对同一运行启动多个执行进程。

## 凭据和本地数据

凭据通过环境变量传入，在动作允许的字段中引用：

```yaml
token: ${{ secrets.GITHUB_TOKEN }}
```

秘密引用必须占据整个字段，不能拼进字符串，也不能用于条件、工作流输出或 AI 输入。通过这种引用传入的值，在已保存的动作输入中会替换成 `[REDACTED]`。

数据库还会保存工作流、运行输入和步骤输出。如果你把敏感内容直接写进输入文件，或者外部服务把它原样返回，这些内容仍可能被保存。现有脱敏不等于自动清除所有敏感数据。

`http.request` 默认阻止访问本机、私网和链路本地地址。调试可信的本地接口时，可以设置 `FLOWUP_ALLOW_PRIVATE_NETWORK=1`；设置 `FLOWUP_ALLOWED_HOSTS` 可以进一步限制它访问的主机，以逗号分隔。保留默认限制，除非当前调试确实需要访问这些地址。

## 命令速查

```text
flowup plugin install <local directory or manifest>
flowup plugin uninstall <name>
flowup validate <workflow.yaml>
flowup run <workflow.yaml> --inputs <inputs.json> [--db <path>]
flowup status <run-id> [--db <path>]
flowup trace <run-id> [--db <path>]
flowup approve <approval-id> [--db <path>]
flowup reject <approval-id> [--reason <text>] [--db <path>]
flowup resume <run-id> [--db <path>]
```

在项目目录安装的本地插件会自动用于 `validate` 和 `run`。开发时可以给这两个命令附加 `--plugins <manifest.yaml>`，直接加载源文件。插件格式、安装范围和运行恢复行为见[本地插件](plugins.md)。

`run` 总是需要 `--inputs`；没有输入的工作流也要提供内容为 `{}` 的 JSON 文件。参数顺序按上面的格式写，选项放在文件路径或 ID 后面。

脚本调用时要同时看退出码和运行状态：正常执行或暂停等待审批通常返回 `0`，执行错误返回 `1`，用法或输入错误通常返回 `2`。`reject` 成功拒绝也返回 `1`；对已失败运行执行 `resume` 会返回 `0`，但打印的状态仍然是 `failed`。

## 当前范围

v1 按声明顺序运行步骤，只能手动启动。暂时没有定时任务、Webhook 接收器、Web UI、HTTP API、并行任务或分布式 Worker。工作流本身不能直接声明 Shell 命令；可以显式安装可信的本地插件扩展动作，无须修改 Go 代码。旧版 pipeline 格式不兼容。

设计上的取舍写在 [docs/decisions.md](decisions.md)。一次实际 CLI 试用的覆盖范围和发现，见 [试用记录](tryout-2026-09-30.md)。
