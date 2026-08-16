// Command mockllm 是 spec-1.7 起草期的 OpenAI 兼容假供应商：只认 /v1/models 与
// /v1/chat/completions，回固定内容并**带 usage**（流式时按 stream_options 在末帧带）。
// 用途：验证 LiteLLM 无状态形态的透传/转换/日志行为，不接真实供应商、不花钱。
// 它记录收到的每个请求（方法/路径/是否流式/模型名）打到 stdout，供 probe 脚本比对。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := ":18099"
	if v := os.Getenv("MOCKLLM_ADDR"); v != "" {
		addr = v
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("REQ %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization") != "")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mock-model","object":"model","owned_by":"mock"}]}`))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model         string           `json:"model"`
			Stream        bool             `json:"stream"`
			StreamOptions *json.RawMessage `json:"stream_options"`
			Messages      []map[string]any `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		log.Printf("REQ %s %s model=%q stream=%v stream_options=%s msgs=%d auth=%v",
			r.Method, r.URL.Path, req.Model, req.Stream, string(rawOr(req.StreamOptions)),
			len(req.Messages), r.Header.Get("Authorization") != "")
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"chatcmpl-mock","object":"chat.completion","created":%d,"model":%q,
				"choices":[{"index":0,"message":{"role":"assistant","content":"mock reply"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`,
				time.Now().Unix(), req.Model)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, tok := range []string{"mock", " reply"} {
			fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", req.Model, tok)
			fl.Flush()
		}
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", req.Model)
		// 末帧 usage：OpenAI 语义下仅当 stream_options.include_usage=true 才带；这里无条件带，
		// 由 probe 观察 LiteLLM 是否原样透传给调用方。
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"total_tokens\":14}}\n\n", req.Model)
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	})
	log.Printf("mockllm listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func rawOr(r *json.RawMessage) []byte {
	if r == nil {
		return []byte("null")
	}
	return *r
}
