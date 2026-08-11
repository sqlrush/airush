package repo

import "time"

// 领域行模型（spec-1.1 §2.1）。json tag 即 API 响应形态，与 proto/openapi/console.yaml 对齐。
// 凭据密文永不出现在任何模型——Credential 相关只经 credentials.go 定向读写。

// Datasource 数据源主档。
type Datasource struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	EngineFamily  string    `json:"engine_family"`
	Engine        string    `json:"engine"`
	EngineVersion string    `json:"engine_version"`
	ConnectMode   string    `json:"connect_mode"`
	ConnectorID   *string   `json:"connector_id"`
	HasCredential bool      `json:"has_credential"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	DatabaseName  string    `json:"database_name"`
	GroupID       *string   `json:"group_id"`
	GroupRole     *string   `json:"group_role"`
	AgentID       *string   `json:"agent_id"`
	HealthStatus  string    `json:"health_status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Agent 智能体注册项。
type Agent struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Kind               string    `json:"kind"`
	Status             string    `json:"status"`
	InstructionDoc     string    `json:"instruction_doc"`
	InstructionVersion int       `json:"instruction_version"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Group 数据源编组。
type Group struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Alias 数据源别名。
type Alias struct {
	ID           string    `json:"id"`
	DatasourceID string    `json:"datasource_id"`
	Alias        string    `json:"alias"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
}

// Connector 客户侧代理（本 spec 只读，写路径归 spec-1.2）。
type Connector struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Location        string     `json:"location"`
	Version         string     `json:"version"`
	Status          string     `json:"status"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	CertFingerprint string     `json:"cert_fingerprint"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// PageCursor 是 keyset 分页游标（(created_at, id) 严格递增序）。
type PageCursor struct {
	CreatedAt time.Time
	ID        string
}

// tenantExpr 在 INSERT 中引用事务 GUC 生成 tenant_id——值与 RLS 判定同源，
// 结构上排除"参数与上下文不一致"一类缺陷。
const tenantExpr = "NULLIF(current_setting('app.tenant_id', true), '')::uuid"
