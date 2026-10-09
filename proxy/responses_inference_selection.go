package proxy

import (
	"context"
	"errors"

	"github.com/codex2api/auth"
	"github.com/gin-gonic/gin"
)

func (h *Handler) nextResponsesCandidate(c *gin.Context, options auth.InferenceCandidateOptions) (candidate auth.InferenceCandidate) {
	if state := responsesInferenceFromContext(c.Request.Context()); state != nil {
		state.configure(options)
		return h.store.NextInferenceCandidate(options)
	}
	candidate.Account, candidate.ProxyURL, candidate.Guard = h.nextAccountForSessionWithDispatchGuard(
		options.SessionKey, options.APIKeyID, options.Exclude, options.Filter, options.Policy)
	return candidate
}

func (h *Handler) preferredResponsesCandidate(c *gin.Context, options auth.InferenceCandidateOptions) *auth.Account {
	if state := responsesInferenceFromContext(c.Request.Context()); state != nil {
		state.configure(options)
		return h.store.NextInferenceCandidate(options).Account
	}
	return h.store.TakePreferredAccountWithDispatch(options.PreferredAccountID, options.APIKeyID, options.Exclude, options.Filter, options.Policy)
}

func (h *Handler) selectInferenceCandidate(ctx context.Context, options auth.InferenceCandidateOptions) auth.InferenceCandidate {
	state := responsesInferenceFromContext(ctx)
	state.configure(options)
	return h.store.NextInferenceCandidate(options)
}

func (h *Handler) waitInferenceCandidate(ctx context.Context, options auth.InferenceCandidateOptions) (auth.InferenceCandidate, error) {
	state := responsesInferenceFromContext(ctx)
	state.configure(options)
	waitCtx, cancel := context.WithTimeout(ctx, dispatchAccountWaitTimeout)
	defer cancel()
	candidate, err := h.store.WaitInferenceCandidate(waitCtx, options)
	if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		err = nil
	}
	return candidate, err
}

func releaseSelectedAccount(ctx context.Context, store *auth.Store, account *auth.Account) {
	if responsesInferenceFromContext(ctx) == nil {
		store.Release(account)
	}
}

func inferenceAdmissionFailure(err error) (accountBusy, matched bool) {
	var admission *inferenceAdmissionError
	if errors.As(err, &admission) {
		return admission.accountBusy, true
	}
	return false, false
}
