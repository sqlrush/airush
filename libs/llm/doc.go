// Package llm 是平台侧的 LLM 网关客户端库（spec-1.7 D3）。
//
// 核心是 Meter：一个 http.RoundTripper 中间件，挂在任意 OpenAI 兼容客户端（codexgo 的
// api client、测试用的最小 Client）的 Transport 上：
//   - 请求前：注入租户/agent/会话/trace 头、给流式请求补 stream_options.include_usage、
//     过配额门（QuotaGate）；
//   - 响应后：从 chat/responses × 流式/非流式四种形态里提取 usage，交给 Recorder 记账。
//
// 它自身不存任何状态（AD-1）；LiteLLM 见不到租户与用量，控制面见不到 prompt——
// 本包所在的进程是唯一同时见到两者的地方（spec-1.7 §2.1）。
package llm
