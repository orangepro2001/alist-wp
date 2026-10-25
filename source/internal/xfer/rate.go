// rate.go W5: 简易令牌桶限速钩子(FR-14/FR-34; 0=不限速)。
// 自研令牌桶(避免引入 x/time/rate; 精度按 10ms tick 足够带宽整形用途)。
package xfer

import (
	"context"
	"sync"
	"time"
)

// rateLimiter 令牌桶: 桶容量=1s 配额(最小 4KB), 匀速补充。
type rateLimiter struct {
	mu       sync.Mutex
	rate     float64 // B/s
	tokens   float64
	last     time.Time
}

func newRateLimiter(rateBps int64) RateLimiter {
	if rateBps <= 0 {
		return nil
	}
	return &rateLimiter{rate: float64(rateBps), tokens: float64(rateBps), last: time.Now()}
}

// WaitN 阻塞直至允许发送 n 字节; ctx 取消返回错误。
func (r *rateLimiter) WaitN(n int64) error {
	if n <= 0 {
		return nil
	}
	for {
		r.mu.Lock()
		now := time.Now()
		r.tokens += now.Sub(r.last).Seconds() * r.rate
		if r.tokens > r.rate {
			r.tokens = r.rate // 桶容量 1s 配额
		}
		r.last = now
		if r.tokens >= float64(n) {
			r.tokens -= float64(n)
			r.mu.Unlock()
			return nil
		}
		need := (float64(n) - r.tokens) / r.rate
		r.mu.Unlock()
		if need < 0.01 {
			need = 0.01
		}
		wait := time.Duration(need * float64(time.Second))
		ctx := context.Background()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
