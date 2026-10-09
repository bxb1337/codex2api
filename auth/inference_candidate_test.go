package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestInferenceCandidateDoesNotReserveOrConsumeDispatch(t *testing.T) {
	account := &Account{DBID: 1, AccessToken: "test"}
	store := newSessionSlotBufferTestStore(1, account)
	options := InferenceCandidateOptions{Policy: DispatchPolicyStandard}
	for i := 0; i < 3; i++ {
		if account != store.NextInferenceCandidate(options).Account {
			t.Fatal("expected the eligible account")
		}
	}
	if int64(0) != account.GetActiveRequests() || int64(0) != account.GetOccupiedRequests() || int64(0) != account.GetTotalRequests() {
		t.Fatal("candidate inspection occupied or consumed a request")
	}
	if !store.AcquireInferenceAccount(account, options) {
		t.Fatal("expected first final admission")
	}
	if store.AcquireInferenceAccount(account, options) {
		t.Fatal("second final admission exceeded the account limit")
	}
	store.Release(account)
}

func TestInferenceBufferedCapacityYieldsWithoutTimerDoubleRelease(t *testing.T) {
	account := &Account{DBID: 1, AccessToken: "test"}
	store := newSessionSlotBufferTestStore(1, account)
	options := InferenceCandidateOptions{Policy: DispatchPolicyStandard}
	if !store.AcquireInferenceAccount(account, options) {
		t.Fatal("expected initial admission")
	}
	store.Release(account)
	store.BufferInferenceSession(account, InferenceSessionBuffer{SessionKey: "owner"})
	store.sessionMu.RLock()
	id := store.sessionSlotReservations[account.DBID]["owner"][0]
	store.sessionMu.RUnlock()
	if int64(0) != accountAdmissionLoad(account) || int64(1) != account.GetReclaimableSlots() {
		t.Fatal("buffer incorrectly occupies actual capacity")
	}
	if fresh := store.Next(); fresh != account {
		t.Fatal("new session was blocked by a reclaimable buffer")
	}
	store.expireSessionSlot(account, "owner", id)
	if int64(1) != account.GetActiveRequests() || int64(1) != account.GetOccupiedRequests() || int64(0) != account.GetReclaimableSlots() {
		t.Fatal("expired reclaimed reservation released the new request")
	}
	store.Release(account)
}

func TestInferenceFinalAdmissionRemainsAtomic(t *testing.T) {
	account := &Account{DBID: 1, AccessToken: "test"}
	store := newSessionSlotBufferTestStore(1, account)
	options := InferenceCandidateOptions{Policy: DispatchPolicyStandard}
	var grants atomic.Int64
	var wait sync.WaitGroup
	for i := 0; i < 24; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if store.AcquireInferenceAccount(account, options) {
				grants.Add(1)
			}
		}()
	}
	wait.Wait()
	if int64(1) != grants.Load() {
		t.Fatalf("want one grant, got %d", grants.Load())
	}
	store.Release(account)
}

func TestInferenceWaitCancellationLeavesNoQueueEntry(t *testing.T) {
	account := &Account{DBID: 1, AccessToken: "test"}
	store := newSessionSlotBufferTestStore(1, account)
	options := InferenceCandidateOptions{Policy: DispatchPolicyStandard}
	if !store.AcquireInferenceAccount(account, options) {
		t.Fatal("expected initial admission")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.WaitInferenceCandidate(ctx, options)
	if context.Canceled != err {
		t.Fatalf("want cancellation, got %v", err)
	}
	if int64(0) != store.schedulerAvailabilityHub().waiters.Load() {
		t.Fatal("canceled request leaked a queue entry")
	}
	store.Release(account)
}
