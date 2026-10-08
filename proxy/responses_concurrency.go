package proxy

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/codex2api/api"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

type responsesConcurrencyModeKey struct{}
type responsesInferenceKey struct{}

type responsesInference struct {
	mu       sync.Mutex
	handler  *Handler
	client   *gin.Context
	options  auth.InferenceCandidateOptions
	selected *auth.Account
	current  *InferenceRequestLease
}

// InferenceRequestLease 属于一次实际发送，旧读流结束不能释放后一次请求的名额。
type InferenceRequestLease struct {
	once         sync.Once
	done         atomic.Bool
	finishing    atomic.Bool
	finished     chan struct{}
	account      *auth.Account
	store        *auth.Store
	releaseKey   func()
	releaseScope func()
}

func (lease *InferenceRequestLease) Finish() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		lease.finishing.Store(true)
		if lease.releaseScope != nil {
			lease.releaseScope()
		}
		if lease.releaseKey != nil {
			lease.releaseKey()
		}
		// 最后释放账号并唤醒队列，此时 Key 和 scope 已可重新准入。
		lease.store.Release(lease.account)
		lease.done.Store(true)
		close(lease.finished)
	})
}

func ResponsesConcurrencyMode(ctx context.Context) string {
	if ctx != nil {
		if mode, ok := ctx.Value(responsesConcurrencyModeKey{}).(string); ok {
			return mode
		}
	}
	return database.ConcurrencyAccountingLegacy
}

func responsesInferenceFromContext(ctx context.Context) *responsesInference {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(responsesInferenceKey{}).(*responsesInference)
	return state
}

func responsesInferenceForClient(c *gin.Context) *responsesInference {
	if c == nil || c.Request == nil {
		return nil
	}
	return responsesInferenceFromContext(c.Request.Context())
}

func (h *Handler) beginResponsesConcurrency(c *gin.Context) func() {
	original := c.Request
	if _, exists := original.Context().Value(responsesConcurrencyModeKey{}).(string); exists {
		return func() {}
	}
	mode := CurrentRuntimeSettings().ConcurrencyAccountingMode
	ctx := context.WithValue(original.Context(), responsesConcurrencyModeKey{}, mode)
	var state *responsesInference
	if mode == database.ConcurrencyAccountingInference {
		state = &responsesInference{handler: h, client: c}
		ctx = context.WithValue(ctx, responsesInferenceKey{}, state)
	}
	c.Request = original.WithContext(ctx)
	return func() {
		if state != nil {
			state.finish()
		}
		c.Request = original
	}
}

func (state *responsesInference) configure(options auth.InferenceCandidateOptions) {
	state.mu.Lock()
	state.options = options
	state.mu.Unlock()
}

func (state *responsesInference) selectAccount(account *auth.Account) {
	state.mu.Lock()
	state.selected = account
	state.mu.Unlock()
}

func (state *responsesInference) finish() {
	state.mu.Lock()
	lease := state.current
	state.mu.Unlock()
	lease.Finish()
}

func (state *responsesInference) begin() (*InferenceRequestLease, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.current != nil && !state.current.done.Load() {
		if !state.current.finishing.Load() {
			return state.current, nil
		}
		<-state.current.finished
	}
	if state.selected == nil {
		return nil, ErrNoAvailableAccount()
	}
	releaseKey, err := state.acquireKey()
	if err != nil {
		return nil, err
	}
	if !state.handler.store.AcquireInferenceAccount(state.selected, state.options) {
		if releaseKey != nil {
			releaseKey()
		}
		return nil, &inferenceAdmissionError{accountBusy: true, rejection: &Error{Code: ErrorCodeAccountPoolConcurrencySaturated, Type: ErrorTypeServerError, HTTPStatus: http.StatusServiceUnavailable, Message: "Upstream account inference concurrency is full, please retry later"}}
	}
	state.handler.acquireAPIKeyScopeConcurrency(state.client, state.selected)
	state.current = &InferenceRequestLease{
		account: state.selected, store: state.handler.store, releaseKey: releaseKey, finished: make(chan struct{}),
		releaseScope: func() { state.handler.ReleaseAPIKeyScopeConcurrency(state.client) },
	}
	return state.current, nil
}

func (state *responsesInference) acquireKey() (func(), error) {
	row := apiKeyRowFromContext(state.client)
	if row == nil || row.ID <= 0 {
		return nil, nil
	}
	release, current, ok := state.handler.apiKeyConcurrencyLimiter().acquireTracked(row.ID, row.Limits.MaxConcurrency)
	if ok {
		return release, nil
	}
	return nil, &inferenceAdmissionError{rejection: &Error{
		Code: string(api.ErrCodeRateLimitReached), Type: ErrorTypeRateLimitError,
		HTTPStatus: http.StatusTooManyRequests,
		Message:    fmt.Sprintf("API key concurrency limit exceeded: %d inflight requests (max %d)", current, row.Limits.MaxConcurrency),
	}}
}

type inferenceAdmissionError struct {
	rejection   *Error
	accountBusy bool
}

func (err *inferenceAdmissionError) Error() string { return err.rejection.Error() }
func (err *inferenceAdmissionError) Unwrap() error { return err.rejection }

func BeginInferenceRequest(ctx context.Context) (*InferenceRequestLease, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if state := responsesInferenceFromContext(ctx); state != nil {
		return state.begin()
	}
	return nil, nil
}

func FinishInferenceRequest(ctx context.Context) {
	if state := responsesInferenceFromContext(ctx); state != nil {
		state.finish()
	}
}

func (h *Handler) releaseResponsesAccount(c *gin.Context, account *auth.Account) {
	if state := responsesInferenceFromContext(c.Request.Context()); state != nil {
		state.finish()
		return
	}
	h.store.Release(account)
}

func (h *Handler) releaseResponsesSession(c *gin.Context, account *auth.Account, options auth.InferenceSessionBuffer) {
	if state := responsesInferenceFromContext(c.Request.Context()); state != nil {
		state.finish()
		h.store.BufferInferenceSession(account, options)
		return
	}
	h.store.ReleaseForSessionWithGuard(account, options.SessionKey, options.Guard)
}

func CurrentInferenceRequest(ctx context.Context) *InferenceRequestLease {
	if state := responsesInferenceFromContext(ctx); state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.current
	}
	return nil
}
