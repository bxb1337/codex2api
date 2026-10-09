package cache

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// ConcurrencyLeaseStore 原子协调一次推理涉及的全部名额，按 owner 续期与释放。
type ConcurrencyLeaseStore interface {
	AcquireConcurrencyLease(context.Context, ConcurrencyLeaseRequest) (ConcurrencyLeaseResult, error)
	RefreshConcurrencyLease(context.Context, ConcurrencyLeaseRequest) (bool, error)
	ReleaseConcurrencyLease(context.Context, ConcurrencyLeaseRequest) error
}

// ConcurrencyLeaseSlot 描述一个共享账号、Key 或 scope 的硬上限。
type ConcurrencyLeaseSlot struct {
	Key   string
	Limit int64
}

// ConcurrencyLeaseRequest 的 owner 在一次实际推理期间保持不变。
type ConcurrencyLeaseRequest struct {
	Owner string
	Slots []ConcurrencyLeaseSlot
	TTL   time.Duration
}

// ConcurrencyLeaseResult 在拒绝时指出最先达到上限的名额。
type ConcurrencyLeaseResult struct {
	Acquired bool
	Rejected int // 从 0 开始的失败名额下标
	Inflight int64
}

var acquireConcurrencyLeaseScript = redis.NewScript(`
local stamp = redis.call('TIME')
local now = tonumber(stamp[1]) * 1000 + math.floor(tonumber(stamp[2]) / 1000)
for i, key in ipairs(KEYS) do
    redis.call('ZREMRANGEBYSCORE', key, '-inf', now)
    local count = redis.call('ZCARD', key)
    if not redis.call('ZSCORE', key, ARGV[1]) and count >= tonumber(ARGV[i + 2]) then
        return {0, i - 1, count}
    end
end
for _, key in ipairs(KEYS) do
    redis.call('ZADD', key, now + tonumber(ARGV[2]), ARGV[1])
    redis.call('PEXPIRE', key, math.max(redis.call('PTTL', key), tonumber(ARGV[2])))
end
return {1, 0, 0}
`)

var refreshConcurrencyLeaseScript = redis.NewScript(`
local stamp = redis.call('TIME')
local now = tonumber(stamp[1]) * 1000 + math.floor(tonumber(stamp[2]) / 1000)
for _, key in ipairs(KEYS) do
    local expires = tonumber(redis.call('ZSCORE', key, ARGV[1]))
    if not expires or expires <= now then return 0 end
end
for _, key in ipairs(KEYS) do
    redis.call('ZADD', key, 'XX', now + tonumber(ARGV[2]), ARGV[1])
    redis.call('PEXPIRE', key, math.max(redis.call('PTTL', key), tonumber(ARGV[2])))
end
return 1
`)

var releaseConcurrencyLeaseScript = redis.NewScript(`
for _, key in ipairs(KEYS) do
    redis.call('ZREM', key, ARGV[1])
    if redis.call('ZCARD', key) == 0 then redis.call('DEL', key) end
end
return 1
`)

func concurrencyLeaseArguments(request ConcurrencyLeaseRequest) ([]string, []any, error) {
	if strings.TrimSpace(request.Owner) == "" || len(request.Slots) == 0 || request.TTL < time.Millisecond {
		return nil, nil, fmt.Errorf("invalid concurrency lease")
	}
	keys := make([]string, 0, len(request.Slots))
	args := []any{request.Owner, request.TTL.Milliseconds()}
	seen := make(map[string]bool, len(request.Slots))
	for _, slot := range request.Slots {
		if slot.Key == "" || slot.Limit <= 0 || seen[slot.Key] {
			return nil, nil, fmt.Errorf("invalid or duplicate concurrency slot")
		}
		seen[slot.Key] = true
		keys = append(keys, fmt.Sprintf("codex:concurrency:{inference}:%x", sha256.Sum256([]byte(slot.Key))))
		args = append(args, slot.Limit)
	}
	return keys, args, nil
}

func (tc *redisTokenCache) AcquireConcurrencyLease(ctx context.Context, request ConcurrencyLeaseRequest) (ConcurrencyLeaseResult, error) {
	keys, args, err := concurrencyLeaseArguments(request)
	if err != nil {
		return ConcurrencyLeaseResult{}, err
	}
	values, err := acquireConcurrencyLeaseScript.Run(ctx, tc.client, keys, args...).Slice()
	if err != nil {
		return ConcurrencyLeaseResult{}, err
	}
	if len(values) != 3 {
		return ConcurrencyLeaseResult{}, fmt.Errorf("invalid concurrency lease result")
	}
	acquired, ok := values[0].(int64)
	rejected, indexOK := values[1].(int64)
	inflight, countOK := values[2].(int64)
	if !ok || !indexOK || !countOK || rejected < 0 || rejected >= int64(len(keys)) {
		return ConcurrencyLeaseResult{}, fmt.Errorf("invalid concurrency lease result")
	}
	return ConcurrencyLeaseResult{Acquired: acquired == 1, Rejected: int(rejected), Inflight: inflight}, nil
}

func (tc *redisTokenCache) RefreshConcurrencyLease(ctx context.Context, request ConcurrencyLeaseRequest) (bool, error) {
	keys, args, err := concurrencyLeaseArguments(request)
	if err != nil {
		return false, err
	}
	result, err := refreshConcurrencyLeaseScript.Run(ctx, tc.client, keys, args[:2]...).Int64()
	return result == 1, err
}

func (tc *redisTokenCache) ReleaseConcurrencyLease(ctx context.Context, request ConcurrencyLeaseRequest) error {
	keys, _, err := concurrencyLeaseArguments(request)
	if err != nil {
		return err
	}
	return releaseConcurrencyLeaseScript.Run(ctx, tc.client, keys, request.Owner).Err()
}

var _ ConcurrencyLeaseStore = (*redisTokenCache)(nil)
