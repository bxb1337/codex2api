package proxy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/codex2api/api"
	"github.com/codex2api/cache"
	"github.com/google/uuid"
)

const (
	inferenceSharedLeaseTTL     = 15 * time.Second
	inferenceSharedLeaseRefresh = 5 * time.Second
	inferenceSharedCacheTimeout = 500 * time.Millisecond
	inferenceSharedLeasePoll    = 100 * time.Millisecond
)

type sharedInferenceLease struct {
	backend      cache.ConcurrencyLeaseStore
	request      cache.ConcurrencyLeaseRequest
	ctx          context.Context
	cancel       context.CancelFunc
	lease        *InferenceRequestLease
	accountLimit func() int64
}

func (state *responsesInference) sharedConcurrencySlots() []cache.ConcurrencyLeaseSlot {
	prefix := state.handler.db.RuntimeCacheScope() + ":"
	var slots []cache.ConcurrencyLeaseSlot
	if row := apiKeyRowFromContext(state.client); row != nil && row.ID > 0 && row.Limits.MaxConcurrency > 0 {
		slots = append(slots, cache.ConcurrencyLeaseSlot{Key: fmt.Sprintf("%skey:%d", prefix, row.ID), Limit: int64(row.Limits.MaxConcurrency)})
	}
	if gate := scopeBudgetGateFromContext(state.client); gate != nil {
		for _, scope := range gate.concurrencyScopes {
			if scopeMatchesAccount(scope, state.selected) {
				slots = append(slots, cache.ConcurrencyLeaseSlot{Key: fmt.Sprintf("%sscope:%d:%s:%d", prefix, gate.apiKeyID, scope.ResolveScopeType(), scope.ScopeID), Limit: int64(scope.MaxConcurrency)})
			}
		}
	}
	slots = append(slots, cache.ConcurrencyLeaseSlot{Key: fmt.Sprintf("%saccount:%d", prefix, state.selected.ID()),
		Limit: state.handler.store.InferenceAccountLimit(state.selected, state.options)})
	return slots
}

func (state *responsesInference) acquireSharedConcurrency(ctx context.Context) (*sharedInferenceLease, error) {
	if state.handler.cache == nil || !state.handler.cache.SharedAcrossInstances() {
		return nil, nil
	}
	backend, ok := state.handler.cache.(cache.ConcurrencyLeaseStore)
	if !ok {
		return nil, inferenceSharedBackendError(errors.New("shared cache has no concurrency lease capability"))
	}
	shared := &sharedInferenceLease{backend: backend, request: cache.ConcurrencyLeaseRequest{
		Owner: uuid.NewString(), Slots: state.sharedConcurrencySlots(), TTL: inferenceSharedLeaseTTL}}
	account, options := state.selected, state.options
	shared.accountLimit = func() int64 { return state.handler.store.InferenceAccountLimit(account, options) }
	result, err := waitSharedInferenceCapacity(ctx, shared)
	if err != nil {
		shared.release()
		return nil, inferenceSharedBackendError(err)
	}
	if !result.Acquired {
		return nil, state.sharedConcurrencyRejection(result, shared.request.Slots)
	}
	return shared, nil
}

func waitSharedInferenceCapacity(ctx context.Context, shared *sharedInferenceLease) (cache.ConcurrencyLeaseResult, error) {
	waitCtx, cancel := context.WithTimeout(ctx, dispatchAccountWaitTimeout)
	defer cancel()
	ticker := time.NewTicker(inferenceSharedLeasePoll)
	defer ticker.Stop()
	for {
		limit := shared.accountLimit()
		if limit <= 0 {
			return cache.ConcurrencyLeaseResult{Rejected: len(shared.request.Slots) - 1}, nil
		}
		shared.request.Slots[len(shared.request.Slots)-1].Limit = limit
		cacheCtx, cacheCancel := context.WithTimeout(waitCtx, inferenceSharedCacheTimeout)
		result, err := shared.backend.AcquireConcurrencyLease(cacheCtx, shared.request)
		cacheCancel()
		if err != nil || result.Acquired || result.Rejected != len(shared.request.Slots)-1 {
			return result, err
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-waitCtx.Done():
			return result, nil
		case <-ticker.C:
		}
	}
}

func inferenceSharedBackendError(err error) error {
	log.Printf("Redis 推理并发协调失败: %v", err)
	return &inferenceAdmissionError{rejection: &Error{Code: string(api.ErrCodeServiceUnavailable),
		Type: ErrorTypeServerError, HTTPStatus: http.StatusServiceUnavailable, Message: "Shared inference concurrency backend is unavailable"}}
}

func (state *responsesInference) sharedConcurrencyRejection(result cache.ConcurrencyLeaseResult, slots []cache.ConcurrencyLeaseSlot) error {
	if result.Rejected == len(slots)-1 {
		return &inferenceAdmissionError{rejection: &Error{Code: ErrorCodeAccountPoolConcurrencySaturated,
			Type: ErrorTypeServerError, HTTPStatus: http.StatusServiceUnavailable, Message: "Shared upstream account inference concurrency is full"}}
	}
	message := fmt.Sprintf("Shared API key concurrency limit exceeded: %d inflight requests (max %d)", result.Inflight, slots[result.Rejected].Limit)
	if strings.Contains(slots[result.Rejected].Key, ":scope:") {
		message = fmt.Sprintf("Shared API key scope concurrency limit reached: %d inflight requests (max %d)", result.Inflight, slots[result.Rejected].Limit)
	}
	return &inferenceAdmissionError{rejection: &Error{Code: string(api.ErrCodeRateLimitReached),
		Type: ErrorTypeRateLimitError, HTTPStatus: http.StatusTooManyRequests,
		Message: message}}
}

func (shared *sharedInferenceLease) start(lease *InferenceRequestLease) {
	if shared == nil {
		return
	}
	shared.lease = lease
	shared.ctx, shared.cancel = context.WithCancel(context.Background())
	go shared.watch()
}

func (shared *sharedInferenceLease) watch() {
	ticker := time.NewTicker(inferenceSharedLeaseRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-shared.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(shared.ctx, inferenceSharedCacheTimeout)
			owned, err := shared.backend.RefreshConcurrencyLease(ctx, shared.request)
			cancel()
			if !owned || err != nil {
				if !shared.lease.finishing.Load() {
					log.Printf("Redis 推理并发租约丢失，停止上游 account=%d: %v", shared.lease.account.ID(), err)
					shared.cancel()
				}
				return
			}
		}
	}
}

func (shared *sharedInferenceLease) release() {
	if shared == nil {
		return
	}
	if shared.cancel != nil {
		shared.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), inferenceSharedCacheTimeout)
	defer cancel()
	if err := shared.backend.ReleaseConcurrencyLease(ctx, shared.request); err != nil {
		log.Printf("释放 Redis 推理并发租约失败，等待过期: %v", err)
	}
}

// OnSharedLoss 在租约无法续期时终止实际上游；正常 Finish 不触发取消。
func (lease *InferenceRequestLease) OnSharedLoss(cancel func()) {
	if lease == nil || lease.shared == nil {
		return
	}
	context.AfterFunc(lease.shared.ctx, func() {
		if !lease.finishing.Load() {
			cancel()
		}
	})
}

func (lease *InferenceRequestLease) upstreamContext(ctx context.Context) context.Context {
	if lease == nil || lease.shared == nil {
		return ctx
	}
	upstream, cancel := context.WithCancel(ctx)
	lease.OnSharedLoss(cancel)
	return upstream
}
