# spec-0.10 D1：Go 组件参数化镜像（四组件同构，差异经 COMPONENT 表达）。
# 安全基线（§2.1）：distroless/static + nonroot + 无 shell；-debug 排障走
# `kubectl debug` 临时容器或 debug tag 变体，禁入 values 默认。
ARG COMPONENT=gateway

FROM golang:1.26 AS builder
ARG COMPONENT
WORKDIR /src
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
