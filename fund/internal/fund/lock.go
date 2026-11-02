package fund

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis 锁：SET NX PX + UUID + Lua compare-and-delete，键全部 fund: 前缀，短 TTL。
// 语义区分：Redis 不可用（连接错误）-> 降级为纯 DB 保证并记日志；锁已被持有 -> 返回 busy，调用方回 429，不得静默放行。
var unlockScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("del", KEYS[1])
else
  return 0
end`)

type redisLock struct {
	key   string
	token string
	rdb   *redis.Client
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// tryLock 返回 (lock, degraded, busy)。
// degraded=true 表示 Redis 不可用，本次操作降级为 DB 保证；busy=true 表示锁被他人持有。
func (s *Server) tryLock(ctx context.Context, key string) (*redisLock, bool, bool) {
	if s.rdb == nil {
		return nil, true, false
	}
	token := newToken()
	ok, err := s.rdb.SetNX(ctx, key, token, 10*time.Second).Result()
	if err != nil {
		log.Printf("[fund] WARN redis lock degraded key=%s err=%v", key, err)
		return nil, true, false
	}
	if !ok {
		return nil, false, true
	}
	return &redisLock{key: key, token: token, rdb: s.rdb}, false, false
}

func (l *redisLock) unlock(ctx context.Context) {
	if l == nil || l.rdb == nil {
		return
	}
	_, _ = unlockScript.Run(ctx, l.rdb, []string{l.key}, l.token).Result()
}
