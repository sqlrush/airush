package session

import "time"

// backoff 指数退避 + jitter（spec-1.2 §6：上限 5min，jitter 防重连风暴）。
// 无 math/rand 依赖——jitter 由重连计数派生的确定性偏移提供（避免共享 home 场景
// 引入随机源，且测试可断言序列）。
type backoff struct {
	base    time.Duration
	max     time.Duration
	attempt int
}

func newBackoff(max time.Duration) *backoff {
	return &backoff{base: time.Second, max: max}
}

// next 返回下次重连延迟并递增尝试计数。序列：1s,2s,4s,…封顶 max，
// 叠加 attempt 派生的 [0, base) 抖动。
func (b *backoff) next() time.Duration {
	d := b.base << b.attempt
	if d <= 0 || d > b.max {
		d = b.max
	} else {
		b.attempt++
	}
	jitter := time.Duration(b.attempt*137) % b.base
	return d + jitter
}

// reset 握手成功后清零。
func (b *backoff) reset() { b.attempt = 0 }
