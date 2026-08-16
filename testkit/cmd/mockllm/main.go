// Command mockllm 把 testkit/mockllm 跑成独立进程——dev 环境里作 LiteLLM 的 sidecar 假供应商
// （spec-1.7 D1，values-dev `llm.mockProvider.enabled`）。生产 chart 永不启用它。
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/sqlrush/airush/testkit/mockllm"
)

func main() {
	addr := os.Getenv("MOCKLLM_ADDR")
	if addr == "" {
		addr = ":18099"
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	srv := &http.Server{
		Addr:              addr,
		Handler:           mockllm.New(logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("mockllm listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("mockllm exit", "err", err)
		os.Exit(1)
	}
}
