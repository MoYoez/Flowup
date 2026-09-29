# Flowup

一个用 YAML 编排固定流程的 AI 工作流 CLI。你写好步骤，Flowup 负责按顺序执行：读取数据、调用模型、等待确认、发送结果。运行进度保存在本地 SQLite，流程文件可以和项目代码一起放进 Git。

比如，把“读取 GitHub issue → AI 判断优先级 → 人工确认 → 通知 Slack”写成一个文件，以后传入 issue 地址就能运行。

## 能做什么

- 用 YAML 定义输入、步骤和条件。每一步做什么、用到哪些数据，都写在文件里。
- 给 AI 指定提示词和输入，用 JSON Schema 检查返回结果。模型不会自行调用工具或读取其他步骤。
- 在需要确认的地方暂停。审批状态会保存下来，关掉终端也能稍后批准或拒绝。
- 用 `status` 查看进度，用 `trace` 查看执行事件。进程意外退出后，可以从已保存的位置尝试恢复。
- 内置 HTTP 请求、JSON 提取与校验，以及 GitHub、Slack 的读写动作。
- 支持安装本地插件，把 Python、Node 脚本或可执行文件作为工作流动作使用。

目前通过命令行手动启动，步骤顺序执行。审批、配置和恢复的具体行为见[使用指南](docs/usage.md)。

## 安装

需要 Go 1.26 或更新版本：

```sh
git clone https://github.com/moyoez/flowup.git
cd flowup
go install ./cmd/flowup
```

下面的命令都在仓库根目录执行。如果终端找不到 `flowup`，可以把命令开头换成 `go run ./cmd/flowup`。

## 快速上手

先跑一个不用 API Key 的本地例子。

**1. 检查并运行工作流。**

```sh
flowup validate examples/v1/local-triage.yaml
flowup run examples/v1/local-triage.yaml --inputs examples/v1/local-triage.inputs.json
```

[这个工作流](examples/v1/local-triage.yaml)读取输入里的标题和优先级。优先级是 `high` 时输出 `notify`，其他值输出 `ignore`。它只计算结果，不会发送消息。

```text
run_id: run_...
status: succeeded
output: {"route":"notify","title":"Build is failing"}
```

打开[输入文件](examples/v1/local-triage.inputs.json)，把 `high` 改成 `low` 再运行一次，就能看到分支结果变化。

**2. 查看这次运行。**

把 `RUN_ID` 换成刚才输出的实际 ID：

```sh
flowup status RUN_ID
flowup trace RUN_ID
```

记录保存在当前目录的 `.flowup/flowup.db`。`trace` 每行输出一个 JSON 事件，方便继续用脚本处理。

**3. 试试人工确认。**

```sh
flowup run examples/v1/local-approval.yaml --inputs examples/v1/local-approval.inputs.json
```

这次会停在 `waiting_approval`，终端给出批准和拒绝命令。先用 `flowup trace RUN_ID` 查看审批说明，再按需执行其中一条；批准会直接继续后面的步骤。这个例子也不需要联网。

接下来可以试[文字分类](examples/v1/ai-classify.yaml)，只需配置模型服务；或者接上 GitHub 和 Slack，运行 [issue 分流](examples/v1/triage-issue.yaml)。配置方法和外部写入行为见[使用指南](docs/usage.md)。

## 文档

- [使用指南](docs/usage.md)：工作流语法、AI 配置、动作列表、审批、失败处理和命令速查。
- [本地插件](docs/plugins.md)：安装和卸载插件，以及把自己的脚本接入流程。
- [设计取舍](docs/decisions.md)：为什么使用顺序步骤、内置动作和本地 SQLite。

## 开发

```sh
go test ./...
go vet ./...
go build ./cmd/flowup
```

默认测试不需要 API Key 或外部服务，部分测试会在本机启动 HTTP 模拟服务。
