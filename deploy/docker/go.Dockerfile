# spec-0.10 D1：Go 组件参数化镜像（四组件同构，差异经 COMPONENT 表达）。
# 安全基线（§2.1）：distroless/static + nonroot + 无 shell；-debug 排障走
# `kubectl debug` 临时容器或 debug tag 变体，禁入 values 默认。
ARG COMPONENT=gateway

FROM golang:1.26 AS builder
ARG COMPONENT
# spec-1.8 §8 Q1/Q7：agent-runtime 经 go.mod replace 消费 codexgo 抽核分支（../../codexgo 相对
# agent-runtime 目录 = /codexgo）。构建上下文里没有它，按 deploy/codexgo.lock 钉住的 commit 拉取
# （公开仓，无凭据；--depth 1 只取该 commit）。其它组件不需要它（工作区里的缺失 replace 只在
# 真正编译 agent-runtime 时才报错），但 go.work 是共享的，统一在这里准备最省事。
WORKDIR /src
COPY deploy/codexgo.lock deploy/codexgo.lock
RUN set -eux; commit=$(grep -E "^[0-9a-f]{40}$" deploy/codexgo.lock | head -1); \
    git init -q /codexgo && git -C /codexgo remote add origin https://github.com/sqlrush/codexgo \
    && git -C /codexgo fetch -q --depth 1 origin "$commit" && git -C /codexgo checkout -q --detach FETCH_HEAD
# 先拷贝依赖清单利用层缓存
COPY go.work go.work.sum* ./
COPY console/go.mod console/go.sum* console/
COPY connector/go.mod connector/go.sum* connector/
COPY gateway/go.mod gateway/go.sum* gateway/
COPY agent-runtime/go.mod agent-runtime/go.sum* agent-runtime/
COPY testkit/go.mod testkit/go.sum* testkit/
COPY libs/config/go.mod libs/config/go.sum* libs/config/
COPY libs/apierror/go.mod libs/apierror/go.sum* libs/apierror/
COPY libs/obs/go.mod libs/obs/go.sum* libs/obs/
COPY proto/gen/go/go.mod proto/gen/go/go.sum* proto/gen/go/
RUN go mod download all || true
COPY . .
ARG VERSION=0.0.0-dev
RUN CGO_ENABLED=0 GOFLAGS=-mod=readonly go build \
      -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/app "./${COMPONENT}/cmd/${COMPONENT}"

FROM gcr.io/distroless/static-debian12:nonroot
ARG COMPONENT
COPY --from=builder /out/app /app
# 镜像内无配置文件（spec-0.7 §3：配置只经 env）；无 .env
USER 65532:65532
ENTRYPOINT ["/app"]
