package auth

import (
	"context"
	"time"
)

type InferenceCandidateOptions struct {
	SessionKey         string
	APIKeyID           int64
	Exclude            map[int64]bool
	Filter             AccountFilter
	Policy             DispatchPolicy
	PreserveBinding    bool
	PreferredAccountID int64
	Heartbeat          SchedulerWaitHeartbeat
}

type InferenceCandidate struct {
	Account  *Account
	ProxyURL string
	Guard    SessionAffinityGuard
}

// NextInferenceCandidate 只检查候选，不占并发、不消费派发次数。
func (s *Store) NextInferenceCandidate(options InferenceCandidateOptions) InferenceCandidate {
	options.Filter = s.withUsableEgressFilter(options.Filter)
	binding, bound := s.inferenceBinding(options)
	var guard SessionAffinityGuard
	if bound {
		account := s.inferenceAccountByID(binding.accountID)
		if s.inferenceAccountEligible(account, options) {
			if accountAdmissionLoad(account) < s.inferenceAccountLimit(account, options) {
				return InferenceCandidate{Account: account, ProxyURL: binding.proxyURL}
			}
			guard = SessionAffinityGuard{preserveAccountID: binding.accountID}
		}
		if options.PreserveBinding || options.PreferredAccountID != 0 {
			return InferenceCandidate{}
		}
	}
	filter := func(account *Account) bool { return s.inferenceAccountEligible(account, options) }
	if scheduler := s.getFastScheduler(); scheduler != nil {
		account := scheduler.acquireExcludingWithDispatch(options.APIKeyID, options.Exclude, filter, options.Policy, nil, true)
		if account != nil {
			return InferenceCandidate{Account: account, Guard: guard}
		}
	}
	return InferenceCandidate{Account: s.scanInferenceCandidate(options), Guard: guard}
}

func (s *Store) inferenceAccountByID(id int64) *Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lookupByIDLocked(id)
}

func (s *Store) inferenceBinding(options InferenceCandidateOptions) (sessionAffinity, bool) {
	if options.PreferredAccountID != 0 {
		return sessionAffinity{accountID: options.PreferredAccountID}, true
	}
	if options.SessionKey == "" || s.GetAffinityMode() == AffinityModeOff && !options.PreserveBinding {
		return sessionAffinity{}, false
	}
	s.sessionMu.RLock()
	binding, found := s.sessionBindings[options.SessionKey]
	s.sessionMu.RUnlock()
	if !found {
		binding, found = s.getCachedSessionAffinity(options.SessionKey)
	}
	if !found || !binding.expiresAt.After(time.Now()) {
		return sessionAffinity{}, false
	}
	if !options.PreserveBinding && s.GetAffinityMode() == AffinityModeBounded {
		if !s.affinityAccountStillHealthy(binding.accountID) || (!binding.lastUsedAt.IsZero() && time.Since(binding.lastUsedAt) >= sessionAffinityIdleEscape()) {
			return sessionAffinity{}, false
		}
	}
	if !s.affinityProxyStillValid(binding.accountID, binding.proxyURL) {
		binding.proxyURL = ""
	}
	return binding, true
}

func (s *Store) inferenceAccountLimit(account *Account, options InferenceCandidateOptions) int64 {
	base := s.maxConcurrency.Load()
	if options.PreserveBinding && account.UsageLimitContinuationEligible() {
		_, _, limit, _, _ := account.fastSchedulerSnapshotForContinuation(base, time.Now())
		return limit
	}
	_, _, limit, _, _ := account.fastSchedulerSnapshotForPolicy(base, time.Now(), options.Policy)
	return limit
}

func (s *Store) inferenceAccountEligible(account *Account, options InferenceCandidateOptions) bool {
	if accountDispatchBlocked(account) || options.Exclude[account.DBID] ||
		!s.accountAllowedForAPIKey(account, options.APIKeyID) || options.Filter != nil && !options.Filter(account) {
		return false
	}
	continuation := options.PreserveBinding && account.UsageLimitContinuationEligible()
	if continuation {
		return s.inferenceAccountLimit(account, options) > 0
	}
	if !account.dispatchableForPolicy(options.Policy) || s.accountHasBlockingCachedCooldown(account, options.Policy) {
		return false
	}
	if s.GetLazyMode() && !s.ensureLazyDispatchReady(account) {
		return false
	}
	_, _, _, _, available := account.fastSchedulerSnapshotForPolicy(s.maxConcurrency.Load(), time.Now(), options.Policy)
	return available && s.inferenceAccountLimit(account, options) > 0
}

func (s *Store) scanInferenceCandidate(options InferenceCandidateOptions) *Account {
	var best *Account
	for _, account := range s.accountSnapshotAccounts() {
		if !s.inferenceAccountEligible(account, options) || accountAdmissionLoad(account) >= s.inferenceAccountLimit(account, options) {
			continue
		}
		if best == nil || s.inferenceCandidateBetter(account, best) {
			best = account
		}
	}
	return best
}

func (s *Store) inferenceCandidateBetter(candidate, current *Account) bool {
	if priority, old := candidate.schedulerPriority(), current.schedulerPriority(); priority != old {
		return priority > old
	}
	base := s.maxConcurrency.Load()
	tier, _, score, _ := candidate.schedulerSnapshot(base)
	oldTier, _, oldScore, _ := current.schedulerSnapshot(base)
	if tierPriority(tier) != tierPriority(oldTier) {
		return tierPriority(tier) > tierPriority(oldTier)
	}
	if score != oldScore {
		return score > oldScore
	}
	return accountAdmissionLoad(candidate) < accountAdmissionLoad(current)
}

// AcquireInferenceAccount 在实际发送前复验候选及容量，最终准入仍使用原子计数。
func (s *Store) AcquireInferenceAccount(account *Account, options InferenceCandidateOptions) bool {
	if !s.inferenceAccountEligible(account, options) {
		return false
	}
	limit := s.inferenceAccountLimit(account, options)
	if account.GetActiveRequests() >= limit {
		return false
	}
	return s.tryAcquireAccount(account, limit, true)
}

func (s *Store) WaitInferenceCandidate(ctx context.Context, options InferenceCandidateOptions) (InferenceCandidate, error) {
	if candidate := s.NextInferenceCandidate(options); candidate.Account != nil {
		return candidate, nil
	}
	if !s.HasDispatchCandidate(options.SessionKey, options.APIKeyID, options.Exclude, options.Filter, options.PreserveBinding, options.Policy) {
		return InferenceCandidate{}, nil
	}
	hub := s.schedulerAvailabilityHub()
	waiter, err := hub.join(options.APIKeyID, options.PreferredAccountID, options.Exclude)
	if err != nil {
		return InferenceCandidate{}, err
	}
	defer hub.finish(waiter, false, true, 0)
	ticker := time.NewTicker(availabilityRecheckInterval)
	defer ticker.Stop()
	for {
		if candidate := s.NextInferenceCandidate(options); candidate.Account != nil {
			hub.finish(waiter, true, true, 0)
			return candidate, nil
		}
		hub.finish(waiter, false, false, options.PreferredAccountID)
		if err := waitInferenceCandidateChange(ctx, inferenceWaitState{hub: hub, waiter: waiter, tick: ticker.C, heartbeat: options.Heartbeat}); err != nil {
			return InferenceCandidate{}, err
		}
	}
}

type inferenceWaitState struct {
	hub       *availabilityHub
	waiter    *availabilityWaiter
	tick      <-chan time.Time
	heartbeat SchedulerWaitHeartbeat
}

func waitInferenceCandidateChange(ctx context.Context, wait inferenceWaitState) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wait.hub.done:
		return context.Canceled
	case <-wait.waiter.ready:
		return nil
	case <-wait.tick:
		if wait.heartbeat != nil {
			_, err := wait.heartbeat()
			return err
		}
		return nil
	}
}
