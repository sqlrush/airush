# AIRush 根构建入口（spec-0.1 定版）
# 约定：本文件是唯一构建入口；CI 与本地一律经 make 调用（spec-0.3 §2.3）。
# target 语义一经定版只增不改（spec-0.1 §2.2）。

SHELL := /bin/bash
GO_MODULES := console connector gateway agent-runtime

.PHONY: build build-go build-py build-fe test test-go test-py lint fmt clean doctor \
        $(GO_MODULES:%=%/build) $(GO_MODULES:%=%/test)

## build: 构建全部组件（Go 编译 + Python 同步检查 + 前端 build）
build: build-go build-py build-fe

build-go:
	@mkdir -p bin
	@for m in $(GO_MODULES); do \
		echo "==> build $$m"; \
		(cd $$m && go build -o ../bin/$$m ./cmd/$$m) || exit 1; \
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
		(cd $$m && go test ./...) || exit 1; \
	done

test-py:
	@echo "==> test skills"
	@uv run pytest -q skills/tests

## lint: 占位（spec-0.2 实装；禁静默假成功）
lint:
	@echo "SKIP: lint rules defined in spec-0.2"

## fmt: Go 用工具链自带 gofmt；Python/前端格式化 spec-0.2 定版
fmt:
	@for m in $(GO_MODULES); do (cd $$m && go fmt ./...); done
	@echo "SKIP: python/frontend formatters defined in spec-0.2"

clean:
	@rm -rf bin frontend/dist
	@echo "cleaned: bin/ frontend/dist"

## 单组件粒度：make console/build、make gateway/test 等
$(GO_MODULES:%=%/build):
	@mkdir -p bin
	cd $(@D) && go build -o ../bin/$(@D) ./cmd/$(@D)

$(GO_MODULES:%=%/test):
	cd $(@D) && go test ./...

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
