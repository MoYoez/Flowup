# Flowup — 便捷入口(供 CI / 有 make 的环境)。
# 本机若没有 make,直接用等价 go 命令即可(见根 README)。
# 用法:make -C deploy <target>
ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)

.PHONY: build test example mermaid lint tidy up down

## build：编译全部包
build:
	cd "$(ROOT)" && go build ./...

## test：跑全部测试(含 G1 验收)
test:
	cd "$(ROOT)" && go test ./...

## example：跑最小运行示例(重试 / 滑备用模型 / 幂等 / 三态)
example:
	cd "$(ROOT)" && go run ./examples/durable

## mermaid: derive a Mermaid view from a pipeline definition
mermaid:
	cd "$(ROOT)" && go run ./cmd/flowup mermaid examples/deploy_repo_to_paas.pipeline.yaml

## lint：格式检查 + vet
lint:
	cd "$(ROOT)" && gofmt -l . && go vet ./...

## tidy：整理依赖
tidy:
	cd "$(ROOT)" && go mod tidy

# 生产路线本地基建(Postgres + OTel/Jaeger,需 Docker)。
# 本地开发跑在纯 Go SQLite 上,完全不需要下面这些。
## up：起 Postgres + Jaeger(需 Docker)
up:
	docker compose -f "$(ROOT)/deploy/docker-compose.yml" up -d

## down：停掉本地基建
down:
	docker compose -f "$(ROOT)/deploy/docker-compose.yml" down
