# AIRush 根构建入口（spec-0.1 定版）
# 约定：本文件是唯一构建入口；CI 与本地一律经 make 调用（spec-0.3 §2.3）。
# target 语义一经定版只增不改（spec-0.1 §2.2）。

SHELL := /bin/bash
GO_MODULES := console connector gateway agent-runtime
# GO_ALL 含非组件模块（testkit 等）：参与 lint/test/cover，不产二进制
GO_ALL := $(GO_MODULES) testkit libs/config libs/apierror libs/obs libs/accessor libs/metrics libs/tenancy libs/llm proto/gen/go

# 本仓库使用 go.work 工作区；强制 -mod=readonly，避免用户全局 -mod=mod 与
# workspace 模式冲突（workspace 下 -mod 仅允许 readonly/vendor）。
export GOFLAGS := -mod=readonly

# Go 入口可被环境覆盖（共享 home 场景下 PATH 可能混入异架构工具链，
# mac-run.sh 会显式注入行内 env 前缀）。TOOL_ENV 供需要子进程调 go 的工具
#（golangci 等）在执行时刻钉住 PATH/GOROOT；默认为空（CI/常规环境无需）。
GO ?= go
TOOL_ENV ?=
# 覆盖率阻断开关（spec-0.4 Q2）：make cover COVER_ENFORCE=1 生效，spec-1.1 起 CI 默认开
export COVER_ENFORCE

.PHONY: build build-go build-py build-fe test test-go test-py lint fmt clean doctor codexgo-checkout \
        $(GO_MODULES:%=%/build) $(GO_MODULES:%=%/test)

## codexgo-checkout: 把 codexgo 抽核分支按 deploy/codexgo.lock 钉住的 commit 放到 ../codexgo
## （agent-runtime 经 go.mod replace 消费，spec-1.8 §8 Q1/Q7；CI 各 go job 前置调用；本地已有则只校验）
codexgo-checkout:
	@deploy/scripts/codexgo-checkout.sh

## build: 构建全部组件（Go 编译 + Python 同步检查 + 前端 build）
build: build-go build-py build-fe

build-go:
	@mkdir -p bin
	@for m in $(GO_MODULES); do \
		echo "==> build $$m"; \
		(cd $$m && $(TOOL_ENV) $(GO) build -o ../bin/$$m ./cmd/$$m) || exit 1; \
	done
	@echo "==> build testkit (compile check)"
	@cd testkit && $(TOOL_ENV) $(GO) build ./...

build-py:
	@echo "==> build skills (uv sync + import check)"
	@uv sync --quiet
	@uv run python -c "import airush_skills"

build-fe:
	@echo "==> build frontend"
	@cd frontend && pnpm install --silent && pnpm build

## test: 全部单元测试（-race 常开，spec-0.4 Q4）
test: test-go test-py test-fe

test-go:
	@for m in $(GO_ALL); do \
		echo "==> test $$m"; \
		(cd $$m && $(TOOL_ENV) $(GO) test -race ./...) || exit 1; \
	done

test-py:
	@echo "==> test skills"
	@uv run pytest -q

test-fe:
	@echo "==> test frontend"
	@cd frontend && pnpm run test

## cover: 测试 + 覆盖率（报告恒出；阈值阻断由 COVER_ENFORCE=1 激活，spec-1.1 起 CI 默认开）
cover: cover-go cover-py cover-fe

cover-go:
	@mkdir -p bin/cover
	@for m in $(GO_ALL); do \
		echo "==> cover $$m"; \
		(cd $$m && $(TOOL_ENV) $(GO) test -race -covermode=atomic -coverprofile=$(CURDIR)/bin/cover/$${m//\//-}.out ./...) || exit 1; \
	done
	@deploy/scripts/coverage-gate.sh

cover-py:
	@echo "==> cover skills"
	@uv run pytest -q --cov=airush_skills --cov-report=term $${COVER_ENFORCE:+--cov-fail-under=80}

cover-fe:
	@echo "==> cover frontend"
	@cd frontend && pnpm run test:cover

# golangci-lint 版本唯一钉点（spec-0.2 Q1）：go install 到带版本号的本地路径，
# 本地与 CI 同版；bootstrap 阶段清空 GOFLAGS（工具安装不受 workspace/-mod 影响）。
GOLANGCI_VERSION := v2.6.2
TOOLS_BIN := $(CURDIR)/bin/tools
GOLANGCI := $(TOOLS_BIN)/golangci-lint-$(GOLANGCI_VERSION)

$(GOLANGCI):
	@mkdir -p $(TOOLS_BIN)
	GOFLAGS= GOBIN=$(TOOLS_BIN) $(TOOL_ENV) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	@mv $(TOOLS_BIN)/golangci-lint $(GOLANGCI)

## lint: 三语言全量（spec-0.2 实装）+ 迁移编号检查（spec-0.6）
lint: lint-go lint-py lint-fe lint-openapi migrate-check

lint-go: $(GOLANGCI)
	@for m in $(GO_ALL); do \
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

# spectral 钉版经 pnpm dlx（devDependency 同类：构建期工具，规则 8 豁免备案，CI 扫描覆盖）
SPECTRAL_VERSION := 6.15.0
lint-openapi:
	@echo "==> lint openapi (spectral)"
	@cd frontend && pnpm dlx @stoplight/spectral-cli@$(SPECTRAL_VERSION) \
		lint --fail-severity=warn --ruleset ../proto/openapi/.spectral.yaml ../proto/openapi/console.yaml

## fmt: 分域格式化（Go=gofumpt+gci 经 golangci fmt；Py=ruff；FE=prettier），幂等
fmt: $(GOLANGCI)
	@for m in $(GO_ALL); do (cd $$m && $(TOOL_ENV) $(GOLANGCI) fmt ./...); done
	@uv run ruff format .
	@cd frontend && pnpm run format

## integration-test: 集成测试（必须 docker，规则 6 禁静默跳过）
integration-test: integration-test-go integration-test-py

docker-check:
	@docker info >/dev/null 2>&1 || { \
		echo "ERROR: docker 不可用——集成测试必须 docker。"; \
		echo "  Mac: 启动 OrbStack；Linux: 安装 docker 并加入 docker 组。"; \
		exit 2; }

integration-test-go: docker-check
	@mkdir -p bin/cover/integration
	@for m in $(GO_ALL); do \
		echo "==> integration $$m"; \
		(cd $$m && $(TOOL_ENV) $(GO) test -race -tags integration -covermode=atomic \
			-coverpkg=./... -coverprofile=$(CURDIR)/bin/cover/integration/$${m//\//-}.out ./...) || exit 1; \
	done

integration-test-py: docker-check
	@echo "==> integration skills"
	@uv run pytest -q -m integration

## generate: 从 SSOT 生成代码（proto/errors.json → 双语言错误码，spec-0.8 D1）
generate:
	@cd libs/apierror && $(TOOL_ENV) $(GO) run ./gen

# proto 工具链钉版（spec-1.2 D1，沿 golangci 唯一钉点方案）
BUF_VERSION := v1.59.0
PROTOC_GEN_GO_VERSION := v1.36.11
PROTOC_GEN_GO_GRPC_VERSION := v1.6.1
BUF := $(TOOLS_BIN)/buf-$(BUF_VERSION)

$(BUF):
	@mkdir -p $(TOOLS_BIN)
	GOFLAGS= GOBIN=$(TOOLS_BIN) $(TOOL_ENV) $(GO) install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	@mv $(TOOLS_BIN)/buf $(BUF)
	GOFLAGS= GOBIN=$(TOOLS_BIN) $(TOOL_ENV) $(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOFLAGS= GOBIN=$(TOOLS_BIN) $(TOOL_ENV) $(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)

## generate-proto: proto 契约 lint + 代码生成（spec-1.2 D1；CI 幂等守护）
generate-proto: $(BUF)
	@cd proto && $(BUF) lint
	@cd proto && PATH="$(TOOLS_BIN):$$PATH" $(BUF) generate

## proto-breaking: 对 main 的兼容性检查（CI 调用；本地需要完整 git 历史）。
## 基线无 proto 模块时（首个引入 proto 的 PR）跳过——无可对比的契约。
proto-breaking: $(BUF)
	@if git cat-file -e origin/main:proto/buf.yaml 2>/dev/null; then \
		cd proto && $(BUF) breaking --against '../.git#branch=origin/main,subdir=proto' || \
			{ echo "proto 破坏性变更被拒（spec-1.2 §3 兼容性契约）"; exit 1; }; \
	else \
		echo "proto-breaking: origin/main 无 proto 基线，跳过（首个引入 proto 的变更）"; \
	fi

## migrate-new: 生成下一编号迁移文件对（spec-0.6 D2）
migrate-new:
	@test -n "$(name)" || { echo "用法: make migrate-new name=<snake_case>"; exit 2; }
	@deploy/scripts/migrate-new.sh "$(name)"

migrate-check:
	@deploy/scripts/check-migration-seq.sh

## dev-deps: 人肉调试长驻依赖栈（自动化测试禁用，见 spec-0.5 §2.1）
dev-deps-up:
	@docker compose -f deploy/compose/dev-deps.yml up -d --wait
	@echo "dev-deps ready: postgres :5432 / redis :6379"

dev-deps-down:
	@docker compose -f deploy/compose/dev-deps.yml down -v

clean:
	@rm -rf bin frontend/dist
	@echo "cleaned: bin/ frontend/dist"

## 单组件粒度：make console/build、make gateway/test 等
$(GO_MODULES:%=%/build):
	@mkdir -p bin
	cd $(@D) && $(TOOL_ENV) $(GO) build -o ../bin/$(@D) ./cmd/$(@D)

$(GO_MODULES:%=%/test):
	cd $(@D) && $(TOOL_ENV) $(GO) test ./...

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

## obs: 本地观测栈（spec-0.9 D4；grafana/otel-lgtm，Grafana 于 :3000）
obs-up:
	@docker compose -f deploy/compose/obs.yml up -d
	@echo "obs ready: grafana http://localhost:3000 (admin/admin), OTLP :4318"

obs-down:
	@docker compose -f deploy/compose/obs.yml down

## images: 构建全部镜像（spec-0.10 D1）；单个: make image-console 等
## PUSH=true 推送 ghcr（CI main 分支 image job 用）
REGISTRY := ghcr.io/sqlrush/airush
GIT_SHA := $(shell git rev-parse --short HEAD)

images: image-console image-connector image-gateway image-agent-runtime image-skills image-frontend

image-console image-connector image-gateway image-agent-runtime:
	$(eval C := $(patsubst image-%,%,$@))
	docker build -f deploy/docker/go.Dockerfile --build-arg COMPONENT=$(C) \
		--build-arg VERSION=0.0.0-dev+$(GIT_SHA) \
		-t $(REGISTRY)/$(C):$(GIT_SHA) -t $(REGISTRY)/$(C):latest .
	@if [ "$(PUSH)" = "true" ]; then docker push $(REGISTRY)/$(C):$(GIT_SHA) && docker push $(REGISTRY)/$(C):latest; fi

## image-mockllm（仅 dev，spec-1.7 D1）：LiteLLM sidecar 假供应商；不进生产 values
image-mockllm:
	docker build -f deploy/docker/mockllm.Dockerfile \
		-t $(REGISTRY)/mockllm:$(GIT_SHA) -t $(REGISTRY)/mockllm:latest .

image-skills:
	docker build -f deploy/docker/python.Dockerfile \
		-t $(REGISTRY)/skills:$(GIT_SHA) -t $(REGISTRY)/skills:latest .
	@if [ "$(PUSH)" = "true" ]; then docker push $(REGISTRY)/skills:$(GIT_SHA) && docker push $(REGISTRY)/skills:latest; fi

image-frontend:
	docker build -f deploy/docker/frontend.Dockerfile \
		-t $(REGISTRY)/frontend:$(GIT_SHA) -t $(REGISTRY)/frontend:latest .
	@if [ "$(PUSH)" = "true" ]; then docker push $(REGISTRY)/frontend:$(GIT_SHA) && docker push $(REGISTRY)/frontend:latest; fi

## dev-up: 一键本地 k8s 环境（spec-0.10 D4）：kind + 镜像 + Helm 全栈
KIND_CLUSTER := airush-dev

dev-up:
	@kind get clusters 2>/dev/null | grep -qx $(KIND_CLUSTER) || \
		kind create cluster --config deploy/kind/config.yaml
	@$(MAKE) image-gateway image-console image-connector image-agent-runtime image-mockllm
	kind load docker-image $(REGISTRY)/gateway:latest $(REGISTRY)/console:latest $(REGISTRY)/connector:latest $(REGISTRY)/agent-runtime:latest $(REGISTRY)/mockllm:latest --name $(KIND_CLUSTER)
	@# --kube-context 显式绑定：集群已存在时上面不会 kind create，helm 就会打到
	@# kubectl 的当前 context 上——机器上若还有别的集群（orbstack 等），这是往错误
	@# 集群发布。下面所有 kubectl 都带 --context，helm 不能例外。
	helm upgrade --install airush deploy/charts/airush --kube-context kind-$(KIND_CLUSTER) \
		-f deploy/charts/airush/values-dev.yaml --wait --timeout 5m
	@# :latest + pullPolicy:Never 下，镜像内容变但 Deployment spec 不变 → helm 不滚动 pod。
	@# 显式 rollout restart 让已 kind-load 的新镜像生效（dev 环境每次 dev-up 都重建镜像）。
	@for d in console gateway agent-runtime; do \
		kubectl --context kind-$(KIND_CLUSTER) get deploy airush-$$d >/dev/null 2>&1 && \
		kubectl --context kind-$(KIND_CLUSTER) rollout restart deploy/airush-$$d; \
	done
	@kubectl --context kind-$(KIND_CLUSTER) rollout status deploy/airush-console --timeout=120s
	@kubectl --context kind-$(KIND_CLUSTER) get pods
	@echo "dev-up OK：kubectl port-forward svc/airush-gateway 8081:8081 后访问 /healthz"

dev-down:
	kind delete cluster --name $(KIND_CLUSTER)

helm-lint:
	helm lint deploy/charts/airush -f deploy/charts/airush/values-dev.yaml
