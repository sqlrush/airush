# spec-1.7 D1（仅 dev）：LiteLLM 的 sidecar 假供应商镜像。与 go.Dockerfile 同基线
# （distroless/static + nonroot + 无 shell），只是构建入口固定为 testkit/cmd/mockllm。
FROM golang:1.26 AS builder
WORKDIR /src
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
RUN CGO_ENABLED=0 GOFLAGS=-mod=readonly go build -trimpath -ldflags "-s -w" \
      -o /out/app ./testkit/cmd/mockllm

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/app /app
USER 65532:65532
ENTRYPOINT ["/app"]
