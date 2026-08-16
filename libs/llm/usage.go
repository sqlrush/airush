package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// Usage 是四种响应形态（chat/responses × 流式/非流式）统一后的用量。
type Usage struct {
	// Model 是调用方请求的平台逻辑名（chat-default）；UpstreamModel 是网关实际命中的后端
	// （fallback 后可能不同；由响应体 model 字段带回）。
	Model            string
	UpstreamModel    string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	// CostRefMicro 是网关回的参考成本（微美元，x-litellm-response-cost）；无则 nil。不进计费。
	CostRefMicro *int64
	Stream       bool
}

// wireUsage 覆盖 chat（prompt_tokens/completion_tokens）与 responses（input_tokens/output_tokens）
// 两套字段名；两套不会同时出现，取非零者。
type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (w wireUsage) toUsage() (Usage, bool) {
	if w == (wireUsage{}) {
		return Usage{}, false
	}
	u := Usage{
		PromptTokens:     w.PromptTokens + w.InputTokens,
		CompletionTokens: w.CompletionTokens + w.OutputTokens,
		TotalTokens:      w.TotalTokens,
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u, true
}

// extractJSONUsage 从非流式响应体取 usage：chat 与 responses 都在顶层 usage，
// model 字段给出上游实际模型。
func extractJSONUsage(body []byte) (Usage, bool) {
	var env struct {
		Model string    `json:"model"`
		Usage wireUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return Usage{}, false
	}
	u, ok := env.Usage.toUsage()
	u.UpstreamModel = env.Model
	return u, ok
}

// sseUsageScanner 逐帧扫 SSE，从两种流形态里捞 usage：
//   - chat：某个 chunk 顶层带 usage（末帧，choices 为空）；
//   - responses：`response.completed` 事件的 response.usage。
//
// 它不缓存正文——只解析每一帧的 JSON，解析完即丢，内存与流长度无关。
type sseUsageScanner struct {
	usage    Usage
	found    bool
	upstream string
}

// feedLine 处理一行 SSE（已去掉行尾换行）。
func (s *sseUsageScanner) feedLine(line []byte) {
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	var frame struct {
		Model    string    `json:"model"`
		Usage    wireUsage `json:"usage"`
		Type     string    `json:"type"`
		Response struct {
			Model string    `json:"model"`
			Usage wireUsage `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil {
		return
	}
	if frame.Model != "" {
		s.upstream = frame.Model
	}
	// chat 末帧
	if u, ok := frame.Usage.toUsage(); ok {
		s.usage, s.found = u, true
		return
	}
	// responses 完成事件
	if frame.Type == "response.completed" {
		if u, ok := frame.Response.Usage.toUsage(); ok {
			s.usage, s.found = u, true
			if frame.Response.Model != "" {
				s.upstream = frame.Response.Model
			}
		}
	}
}

func (s *sseUsageScanner) result() (Usage, bool) {
	u := s.usage
	u.UpstreamModel = s.upstream
	u.Stream = true
	return u, s.found
}

// teeUsageReader 包住流式响应体：字节原样交给调用方，同时按行喂给扫描器。
// 关闭时（无论调用方读到 EOF 还是提前 Close）调用 done，把"有没有拿到末帧 usage"交出去——
// 提前 Close 且没拿到 usage = 断流（aborted）。
type teeUsageReader struct {
	src     io.ReadCloser
	scanner *sseUsageScanner
	buf     bytes.Buffer // 未成行的尾巴
	done    func(u Usage, complete bool)
	once    bool
	sawEOF  bool
}

func (t *teeUsageReader) Read(p []byte) (int, error) {
	n, err := t.src.Read(p)
	if n > 0 {
		t.buf.Write(p[:n])
		t.drainLines()
	}
	if errors.Is(err, io.EOF) {
		t.sawEOF = true
		t.finish()
	}
	return n, err
}

func (t *teeUsageReader) drainLines() {
	for {
		line, err := t.buf.ReadBytes('\n')
		if err != nil {
			// 不完整的一行放回去等下一次 Read
			t.buf.Reset()
			t.buf.Write(line)
			return
		}
		t.scanner.feedLine(bytes.TrimRight(line, "\r\n"))
	}
}

func (t *teeUsageReader) Close() error {
	err := t.src.Close()
	if !t.sawEOF {
		// 调用方提前关闭：把残尾也扫一遍（末帧可能恰好没换行收尾），再判定
		if t.buf.Len() > 0 {
			t.scanner.feedLine(bytes.TrimRight(t.buf.Bytes(), "\r\n"))
			t.buf.Reset()
		}
	}
	t.finish()
	return err
}

func (t *teeUsageReader) finish() {
	if t.once {
		return
	}
	t.once = true
	u, found := t.scanner.result()
	t.done(u, found)
}

// isSSE 判断响应是否是事件流。
func isSSE(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(contentType), "text/event-stream")
}

// parseCostHeader 解析 x-litellm-response-cost（美元，浮点字符串）为微美元。
func parseCostHeader(v string) *int64 {
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < 0 {
		return nil
	}
	micro := int64(f*1_000_000 + 0.5)
	return &micro
}
