package proxy

import (
	"sync/atomic"

	"github.com/codex2api/security"
)

// 每条连接保留的 steering 状态共享同一份字节上限。
type responsesWSRetainedMemory struct {
	mux         *responsesWSMultiplexer
	reservation *security.RequestMemoryReservation
	bytes       int64
	released    atomic.Bool
}

func (m *responsesWSMultiplexer) retain(size int64) (*responsesWSRetainedMemory, bool) {
	limit := int64(security.MaxRequestBodySize)
	if limit <= 0 {
		limit = int64(security.DefaultMaxRequestBodySize)
	}
	for {
		used := m.retained.Load()
		if size < 0 || size > limit-used {
			return nil, false
		}
		if m.retained.CompareAndSwap(used, used+size) {
			break
		}
	}
	reservation, admitted := security.TryAcquireRequestMemory(size)
	if !admitted {
		m.retained.Add(-size)
		return nil, false
	}
	return &responsesWSRetainedMemory{mux: m, reservation: reservation, bytes: size}, true
}

func (r *responsesWSRetainedMemory) Release() {
	if r == nil || !r.released.CompareAndSwap(false, true) {
		return
	}
	r.reservation.Release()
	r.mux.retained.Add(-r.bytes)
}
