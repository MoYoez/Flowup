# Flowup

给 agent 的可靠流水线层。宿主只调用一个接口,拿回三态结果——`ok` / `failed` / `needs_human`——原始接口错误永不外泄。

## 是什么

预定义、参数化的确定性流水线。引擎负责重试、超时、降级、挂起与恢复;节点在沙箱里执行,可以调用真实模型和工具。失败被归类成一组稳定的原因码,调用方永远看不到原始堆栈。

目标是把“agent 调用多步外部任务时不可控的失败”,变成“确定性、可追溯、可恢复的执行”。

## 架构

三层,AI 只出现在首尾两层:

- 触发层:把宿主请求转成 `Invocation`。
- 引擎层:确定性,无 AI。按 DAG 推进、派发节点、持久化状态,负责重试、降级、挂起恢复。
- 执行层:Worker 在沙箱里跑节点,可用模型与工具。

流水线定义是 YAML 唯一真相源,Mermaid 视图由定义派生。状态走事件溯源,落 SQLite(默认)或 Postgres。引擎只通过一个 `durable` 接口访问底座,可替换。

## 快速开始

需要 Go 1.26+,不需要 Docker。

```bash
go test ./...                      # 全部测试
go run ./examples/durable          # 最小示例:重试 / 滑备用模型 / 幂等 / 三态结果
go run ./cmd/flowup new myskill    # 生成一条流水线
go run ./cmd/flowup run myskill.pipeline.yaml
```

`flowup` 子命令:`new` `validate` `mermaid` `run` `trace` `replay` `invoke` `mcp`。

## 接真实模型

复制 `model.env.example` 到 `model.env`,填一个 OpenAI 兼容端点:

```bash
set -a; source model.env; set +a
go run ./cmd/flowup run examples/weather_ai.pipeline.yaml --params '{"city":"Tokyo"}'
```

semantic 节点让模型直接产出 JSON;agent 节点让模型自行调用工具(如 `http_get` 拉取实时数据),再产出结果。输出按节点的 `output_schema` 校验,瞬时失败由节点 `retry` 自愈。也支持 token 流式。

## 分布式运行

引擎服务加一个或多个 worker,共享同一个 store:

```bash
go run ./cmd/engine --db flowup.db --pipelines ./examples   # HTTP :8080,GET / 是网页面板
go run ./cmd/worker --db flowup.db --caps os
```

- 同步:`POST /invoke`。
- 异步:`POST /runs` 立即返回,`GET /run-by-key/{key}` 取 run_id,`GET /runs/{id}/events?follow=1` 看实时 SSE 事件流。
- 宿主集成:`flowup mcp` 把 `run_pipeline` / `list_pipelines` 暴露给任何 MCP 宿主。

## 目录

```
cmd/
  flowup           CLI
  engine worker    分布式守护进程
internal/
  contracts        跨层契约与三态
  pipelines        YAML 加载 / 校验 / 模板 / schema / mermaid
  durable          事件溯源执行接口与自研实现
  store            事件日志 / 状态 / 步骤记忆 / 去重 / 队列 / outbox(SQLite | Postgres)
  engine           确定性编排
  dispatch         拉取式队列与 worker
  sandbox          执行后端(子进程 / 容器)
  model            可插拔模型(scripted / OpenAI 兼容 / Anthropic)
  tools            工具与白名单(http_get / http_post / shell / git / fs)
  agent            模型与工具的循环
  runner           按 kind 路由节点
  ratelimit mcp trace pgtest
docs/              设计文档与决策记录
examples/          示例流水线 + 最小运行示例(durable/)
```

## 构建选项(可选编译)

默认 `go build`:JSON 走 bytedance/sonic、无追踪、仅 SQLite。两个重依赖按需用 build tag 开:

| tag | 开启 | 默认 |
|---|---|---|
| `otel` | OpenTelemetry span 导出 | 无追踪(noop) |
| `postgres` | Postgres 存储后端(pgx) | 仅 SQLite |

```bash
go build ./...                          # sonic + SQLite,无追踪
go build -tags "otel postgres" ./...    # 全量
go run -tags postgres ./cmd/engine --db 'postgres://...'
```

纯 Go 的特性(鉴权、静态加密、限流、outbox、保留期)不靠 tag——它们是装饰器/可选设置,
**默认就不接**,需要时在组装处接上即可,不进核心、不加依赖。

事件溯源与队列已在真实 Postgres 上验证(`go test -tags postgres ./internal/pgtest`,内嵌 Postgres,无需 Docker)。

## 状态

已实现并在本机验证:确定性引擎(重试、超时、降级、挂起恢复、崩溃恢复、幂等)、拉取式队列、沙箱执行、真实模型加工具循环与流式、分布式守护进程、鉴权、静态加密、保留期、限流、事务 outbox、实时事件流与网页面板。

尚未在本机验证(依赖外部环境):Jaeger 全链路追踪(需 Docker,OTel 埋点已就位)、microVM 沙箱(需 Linux + KVM)、Anthropic 适配器(需 key,OpenAI 已验证为同一套接口)。

设计与决策见 [docs/](docs/)。
