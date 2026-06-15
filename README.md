# Flowup

Flowup 是一个手动执行、顺序运行的 Go 工作流 CLI。它借鉴 GitHub Actions
的声明式模型，但不兼容 GitHub Actions 语法。

工作流与 Flowup 运行时属于可信边界；AI 输出和外部服务响应均不可信，必须
通过动作声明的 JSON Schema。AI 只能看到 `ai.generate.with.input` 中显式
投影的数据，不能调用工具、读取秘密、修改工作流或决定重试。

## 安装

```bash
go install github.com/moyoez/flowup/cmd/flowup@latest
```

也可以直接从源码运行：

```bash
go run ./cmd/flowup validate examples/v1/local-triage.yaml
```

## 工作流

```yaml
name: notify
version: 1

inputs:
  priority:
    type: string
    required: true

steps:
  - id: route
    uses: switch
    with:
      value: ${{ inputs.priority }}
      cases:
        high: approval
        default: notify

  - id: approve
    uses: approval
    if: ${{ steps.route.output == "approval" }}
    with:
      message: Send high-priority alert.

outputs:
  route: ${{ steps.route.output }}
```

步骤严格按声明顺序执行。`if` 只支持引用、字面量、`==`、`!=`、`&&`、`||`
和括号，没有任意代码执行。

## CLI

```text
flowup validate <workflow.yaml>
flowup run <workflow.yaml> --inputs <inputs.json> [--db <path>]
flowup status <run-id> [--db <path>]
flowup approve <approval-id> [--db <path>]
flowup reject <approval-id> [--reason <text>] [--db <path>]
flowup resume <run-id> [--db <path>]
flowup trace <run-id> [--db <path>]
```

`run` 遇到审批时会持久化暂停，并打印完整的批准和拒绝命令。SQLite 数据库默认
位于 `.flowup/flowup.db`。

## 秘密

秘密从同名环境变量读取，并且必须占据动作声明的秘密字段完整值：

```yaml
token: ${{ secrets.GITHUB_TOKEN }}
```

秘密不能用于字符串插值、条件、工作流输出、非秘密字段或 AI 输入。步骤记录和
事件只持久化脱敏值。

AI 配置：

```text
OPENAI_API_KEY
OPENAI_BASE_URL   # 默认 https://api.openai.com
FLOWUP_AI_MODEL   # 使用 model: default 时必填
```

`ai.generate` 使用非流式结构化输出，不提供任何工具。

## 动作

核心动作：

```text
http.request
json.select
json.validate
switch
ai.generate
approval
```

官方连接器：

```text
github.issue.get
github.issue.comment
github.pull_request.get
github.pull_request.comment
slack.message.get
slack.message.send
```

写动作使用持久化副作用记录。崩溃后如果无法确认写操作是否已经发生，Flowup
返回 `effect_indeterminate`，不会盲目重复请求。

## 非目标

v1 不提供自动触发器、定时任务、Webhook、Web UI、HTTP API、分布式 Worker、
并行 Job、任意 Shell/Git/文件系统执行、用户代码、插件协议、MCP 或旧 pipeline
格式兼容。

## 测试

```bash
go test ./...
go vet ./...
go build ./cmd/flowup
```

默认测试不需要 Docker、Postgres、外部网络或 API Key。
