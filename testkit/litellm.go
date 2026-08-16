package testkit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// litellmImage 与 Helm values.yaml llm.image 同一 digest（spec-1.7 R1：钉版；升级两处一起动）。
const litellmImage = "ghcr.io/berriai/litellm@sha256:154e23bb5f31b1f10e16392a8ef299bd2cde08de3a64a6849002cfcc25ce3c63"

// LiteLLM 是一个测试用 LiteLLM 容器句柄（无状态形态：无 DB、无缓存）。
type LiteLLM struct {
	container testcontainers.Container
	// BaseURL 形如 http://127.0.0.1:NNNNN（无尾斜杠）。
	BaseURL string
	// MasterKey 是本容器的 master key（测试随机串，不是密钥）。
	MasterKey string
}

// LiteLLMModel 是一条 model_list 项：逻辑名 → 上游（形如 "deepseek/mock-x"）。
type LiteLLMModel struct {
	Name     string
	Upstream string
}

// StartLiteLLM 起容器，全部模型指向宿主机上的假供应商（mockHostPort，测试进程内 httptest 起）。
// 容器经 host.testcontainers.internal 反向访问宿主机端口（testcontainers WithHostPortAccess）。
// fallbacks：逻辑名 → 备选逻辑名列表（可为 nil）。
func StartLiteLLM(ctx context.Context, mockHostPort int, models []LiteLLMModel, fallbacks map[string][]string) (*LiteLLM, error) {
	const masterKey = "test-master-key-not-a-secret"
	cfg := renderLiteLLMConfig(mockHostPort, models, fallbacks)

	req := testcontainers.ContainerRequest{
		Image:        litellmImage,
		ExposedPorts: []string{"4000/tcp"},
		Cmd:          []string{"--config", "/app/config.yaml", "--port", "4000"},
		Env: map[string]string{
			"LITELLM_MASTER_KEY": masterKey,
			"DISABLE_ADMIN_UI":   "True",
			"LITELLM_LOG":        "INFO",
		},
		Files: []testcontainers.ContainerFile{{
			Reader:            strings.NewReader(cfg),
			ContainerFilePath: "/app/config.yaml",
			FileMode:          0o644,
		}},
		// 冷启动 ~10-20s（本机），CI 更慢；与 Helm 的 startupProbe 同口径给 3 分钟。
		WaitingFor: wait.ForHTTP("/health/liveliness").WithPort("4000/tcp").
			WithStartupTimeout(3 * time.Minute),
	}
	req.HostAccessPorts = []int{mockHostPort} // 容器经 host.testcontainers.internal 反向打宿主机 mock
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("start litellm container: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, fmt.Errorf("litellm host: %w", err)
	}
	port, err := c.MappedPort(ctx, "4000/tcp")
	if err != nil {
		return nil, fmt.Errorf("litellm port: %w", err)
	}
	return &LiteLLM{
		container: c,
		BaseURL:   fmt.Sprintf("http://%s:%s", host, port.Port()),
		MasterKey: masterKey,
	}, nil
}

// renderLiteLLMConfig 生成与 Helm 模板同形态的配置（无 DB、json 日志、prometheus 回调）。
func renderLiteLLMConfig(mockPort int, models []LiteLLMModel, fallbacks map[string][]string) string {
	var b strings.Builder
	b.WriteString("model_list:\n")
	for _, m := range models {
		fmt.Fprintf(&b, "  - model_name: %q\n    litellm_params:\n      model: %q\n      api_base: http://%s:%d/v1\n      api_key: mock\n",
			m.Name, m.Upstream, testcontainers.HostInternal, mockPort)
	}
	b.WriteString("general_settings:\n  master_key: os.environ/LITELLM_MASTER_KEY\n")
	b.WriteString("litellm_settings:\n  telemetry: false\n  json_logs: true\n  drop_params: true\n  callbacks: [\"prometheus\"]\n")
	if len(fallbacks) > 0 {
		b.WriteString("  fallbacks:\n")
		for primary, alts := range fallbacks {
			fmt.Fprintf(&b, "    - %s: [%s]\n", primary, strings.Join(quoteAll(alts), ", "))
		}
	}
	return b.String()
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// Logs 返回容器到目前为止的全部日志（供"日志里不含 prompt"类断言）。
func (l *LiteLLM) Logs(ctx context.Context) (string, error) {
	rc, err := l.container.Logs(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = rc.Close() }()
	var sb strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, rerr := rc.Read(buf)
		sb.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return sb.String(), nil
}

// Terminate 停止并移除容器。
func (l *LiteLLM) Terminate(ctx context.Context) error {
	if err := l.container.Terminate(ctx); err != nil {
		return fmt.Errorf("terminate litellm container: %w", err)
	}
	return nil
}
