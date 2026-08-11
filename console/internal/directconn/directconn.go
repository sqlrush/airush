// Package directconn 是 AD-2② 平台直连接入器（spec-1.17 D2）：从 credcrypto 解密
// 直连凭据 → pgx 连接池 → 对数据库执行只读探测/指令。凭据明文仅在建连的单函数
// 栈帧内存在，DSN（含密码）绝不进日志/错误/响应（AD-4 第三道防线）。
//
// openGauss 走 PG 协议族（MVP 蓝本），engine_family=postgres 优先。
package directconn

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sqlrush/airush/console/internal/credcrypto"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/apierror"
)

// Config 是连接池行为参数（.env 可配）。
type Config struct {
	// IdleTTL 空闲连接池回收窗口。
	IdleTTL time.Duration
	// ConnectTimeout 单次建连/测试超时。
	ConnectTimeout time.Duration
	// MaxConns 每 datasource 池上限。
	MaxConns int32
}

// DefaultConfig spec-1.17 §2.2 默认。
func DefaultConfig() Config {
	return Config{IdleTTL: 10 * time.Minute, ConnectTimeout: 8 * time.Second, MaxConns: 4}
}

// Manager 管理每 datasource 一个的直连连接池（懒建、空闲 TTL 回收，spec-1.17 §8 Q2）。
type Manager struct {
	store  *repo.Store
	sealer *credcrypto.Sealer
	cfg    Config

	mu    sync.Mutex
	pools map[string]*pooledConn
	now   func() time.Time
}

type pooledConn struct {
	pool     *pgxpool.Pool
	lastUsed time.Time
}

// New 构造 Manager。
func New(store *repo.Store, sealer *credcrypto.Sealer, cfg Config) *Manager {
	return &Manager{
		store: store, sealer: sealer, cfg: cfg,
		pools: make(map[string]*pooledConn),
		now:   time.Now,
	}
}

// poolFor 取或懒建 datasource 的连接池。读直连信息（含凭据密文）在租户事务内完成，
// 解密 + DSN 组装 + 建连全在本函数栈帧——密码不逃逸到日志/错误。
func (m *Manager) poolFor(ctx context.Context, datasourceID string) (*pgxpool.Pool, error) {
	m.mu.Lock()
	if pc, ok := m.pools[datasourceID]; ok {
		pc.lastUsed = m.now()
		pool := pc.pool
		m.mu.Unlock()
		return pool, nil
	}
	m.mu.Unlock()

	var info repo.DirectConnInfo
	if err := m.store.InTenantTx(ctx, func(ctx context.Context, tx repo.Tx) error {
		var err error
		info, err = repo.GetDirectConnInfo(ctx, tx, datasourceID)
		return err
	}); err != nil {
		return nil, err
	}

	pool, err := m.buildPool(ctx, info)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// 并发双建：若他人已建，用其结果、关掉本次多建的池
	if pc, ok := m.pools[datasourceID]; ok {
		pool.Close()
		pc.lastUsed = m.now()
		return pc.pool, nil
	}
	m.pools[datasourceID] = &pooledConn{pool: pool, lastUsed: m.now()}
	return pool, nil
}

// buildPool 解密凭据并建池。password 只活在本栈帧；pgxpool 配置以字段设置密码，
// 不经含密码的 DSN 字符串（杜绝任何字符串化泄漏面）。
func (m *Manager) buildPool(ctx context.Context, info repo.DirectConnInfo) (*pgxpool.Pool, error) {
	password, err := m.sealer.Open(info.Ciphertext)
	if err != nil {
		return nil, apierror.Wrap(apierror.CodeInternalError, err)
	}
	defer zero(password)

	// 无密码的基串组装连接参数；密码经 config 字段注入，不进字符串。
	base := fmt.Sprintf("host=%s port=%d dbname=%s user=%s sslmode=prefer",
		info.Host, info.Port, info.DatabaseName, info.Username)
	poolCfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		return nil, connectFailed(err)
	}
	poolCfg.ConnConfig.Password = string(password)
	poolCfg.MaxConns = m.cfg.MaxConns
	poolCfg.ConnConfig.ConnectTimeout = m.effectiveTimeout()

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, connectFailed(err)
	}
	return pool, nil
}

// Destroy 关闭并移除某 datasource 的池（datasource 删除/凭据轮换时调用）。
func (m *Manager) Destroy(datasourceID string) {
	m.mu.Lock()
	pc, ok := m.pools[datasourceID]
	delete(m.pools, datasourceID)
	m.mu.Unlock()
	if ok {
		pc.pool.Close()
	}
}

// ReapIdle 回收超过 IdleTTL 未用的池（后台定时调用）。
func (m *Manager) ReapIdle() {
	m.mu.Lock()
	var stale []*pooledConn
	for id, pc := range m.pools {
		if m.now().Sub(pc.lastUsed) > m.cfg.IdleTTL {
			stale = append(stale, pc)
			delete(m.pools, id)
		}
	}
	m.mu.Unlock()
	for _, pc := range stale {
		pc.pool.Close()
	}
}

// Close 关闭全部池（服务退出）。
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, pc := range m.pools {
		pc.pool.Close()
		delete(m.pools, id)
	}
}

// connectFailed 把底层连接错误归一为 AR_DATASOURCE_CONNECT_FAILED，
// 只带错误类别、绝不带 DSN/凭据（AD-4）。
func connectFailed(err error) error {
	return apierror.Wrap(apierror.CodeDatasourceConnectFailed, err).WithDetails(
		apierror.Detail{Field: "connection", Reason: "无法建立数据库连接（网络/认证/协议）"})
}

// zero 抹除敏感字节切片。
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
