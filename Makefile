# AIRush 根构建入口（spec-0.1 定版）
# 约定：本文件是唯一构建入口；CI 与本地一律经 make 调用（spec-0.3 §2.3）。
# target 语义一经定版只增不改（spec-0.1 §2.2）。

SHELL := /bin/bash
GO_MODULES := console connector gateway agent-runtime

# 本仓库使用 go.work 工作区；强制 -mod=readonly，避免用户全局 -mod=mod 与
# workspace 模式冲突（workspace 下 -mod 仅允许 readonly/vendor）。
export GOFLAGS := -mod=readonly

# Go 入口可被环境覆盖（共享 home 场景下 PATH 可能混入异架构工具链，
# mac-run.sh 会显式注入行内 env 前缀）。TOOL_ENV 供需要子进程调 go 的工具
#（golangci 等）在执行时刻钉住 PATH/GOROOT；默认为空（CI/常规环境无需）。
GO ?= go
TOOL_ENV ?=

.PHONY: build build-go build-py build-fe test test-go test-py lint fmt clean doctor \
        $(GO_MODULES:%=%/build) $(GO_MODULES:%=%/test)

## build: 构建全部组件（Go 编译 + Python 同步检查 + 前端 build）
build: build-go build-py build-fe

build-go:
	@mkdir -p bin
	@for m in $(GO_MODULES); do \
		echo "==> build $$m"; \
		(cd $$m && $(GO) build -o ../bin/$$m ./cmd/$$m) || exit 1; \
	done

build-py:
	@echo "==> build skills (uv sync + import check)"
	@uv sync --quiet
	@uv run python -c "import airush_skills"

build-fe:
	@echo "==> build frontend"
	@cd frontend && pnpm install --silent && pnpm build

## test: 全部单元测试
test: test-go test-py
	@echo "SKIP: frontend unit tests defined in spec-0.4"

test-go:
	@for m in $(GO_MODULES); do \
		echo "==> test $$m"; \
		(cd $$m && $(GO) test ./...) || exit 1; \
	done

test-py:
	@echo "==> test skills"
	@uv run pytest -q skills/tests

# golangci-lint 版本唯一钉点（spec-0.2 Q1）：go install 到带版本号的本地路径，
# 本地与 CI 同版；bootstrap 阶段清空 GOFLAGS（工具安装不受 workspace/-mod 影响）。
GOLANGCI_VERSION := v2.6.2
TOOLS_BIN := $(CURDIR)/bin/tools
GOLANGCI := $(TOOLS_BIN)/golangci-lint-$(GOLANGCI_VERSION)

$(GOLANGCI):
	@mkdir -p $(TOOLS_BIN)
	GOFLAGS= GOBIN=$(TOOLS_BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	@mv $(TOOLS_BIN)/golangci-lint $(GOLANGCI)

## lint: 三语言全量（spec-0.2 实装）
lint: lint-go lint-py lint-fe

lint-go: $(GOLANGCI)
	@for m in $(GO_MODULES); do \
		echo "==> lint $$m"; \
		(cd $$m && $(TOOL_ENV) $(GOLANGCI) run ./...) || exit 1; \
	done

lint-py:
	@echo "==> lint python (ruff + mypy)"
	@uv run ruff check .
	@uv run ruff format --check .
	@uv run mypy

lint-fe:
	@echo "==> lint frontend (eslint + prettier)"
	@cd frontend && pnpm run lint && pnpm run format:check

## fmt: 分域格式化（Go=gofumpt+gci 经 golangci fmt；Py=ruff；FE=prettier），幂等
fmt: $(GOLANGCI)
	@for m in $(GO_MODULES); do (cd $$m && $(TOOL_ENV) $(GOLANGCI) fmt ./...); done
	@uv run ruff format .
	@cd frontend && pnpm run format

clean:
	@rm -rf bin frontend/dist
	@echo "cleaned: bin/ frontend/dist"

## 单组件粒度：make console/build、make gateway/test 等
$(GO_MODULES:%=%/build):
	@mkdir -p bin
	cd $(@D) && $(GO) build -o ../bin/$(@D) ./cmd/$(@D)

$(GO_MODULES:%=%/test):
	cd $(@D) && $(GO) test ./...

## doctor: 检查工具链存在与版本下限（spec-0.1 §6 缓解项）
doctor:
	@ok=1; \
	check() { \
		if command -v $$1 >/dev/null 2>&1; then \
			printf "  %-8s %s\n" "$$1" "$$($$2 2>&1 | head -1)"; \
		else \
			printf "  %-8s MISSING — %s\n" "$$1" "$$3"; ok=0; \
		fi; \
	}; \
	echo "toolchain:"; \
	check go       "go version"        "install Go 1.23+ (https://go.dev/dl)"; \
	check uv       "uv --version"      "curl -LsSf https://astral.sh/uv/install.sh | sh"; \
	check node     "node --version"    "install Node 22 LTS"; \
	check pnpm     "pnpm --version"    "corepack enable pnpm"; \
	[ $$ok -eq 1 ] && echo "doctor: OK" || { echo "doctor: missing tools"; exit 1; }
