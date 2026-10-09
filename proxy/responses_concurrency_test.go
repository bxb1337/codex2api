package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

type inferenceFixture struct {
	handler *Handler
	client  *gin.Context
	account *auth.Account
}

func newInferenceFixture(t *testing.T) inferenceFixture {
	t.Helper()
	old := CurrentRuntimeSettings()
	UpdateRuntimeSettings(func(current RuntimeSettings) RuntimeSettings {
		current.ConcurrencyAccountingMode = database.ConcurrencyAccountingInference
		return current
	})
	t.Cleanup(func() { ApplyRuntimeSettings(old) })
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1})
	t.Cleanup(store.Stop)
	account := &auth.Account{DBID: 1, AccessToken: "test", PlanType: "plus"}
	store.AddAccount(account)
	handler := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
	client, _ := testAPIKeyConcurrencyContext(&database.APIKeyRow{ID: 7, Limits: database.APIKeyLimits{MaxConcurrency: 1}})
	t.Cleanup(handler.beginResponsesConcurrency(client))
	state := responsesInferenceFromContext(client.Request.Context())
	state.configure(auth.InferenceCandidateOptions{APIKeyID: 7, Policy: auth.DispatchPolicyStandard})
	handler.AcquireAPIKeyScopeConcurrency(client, account)
	return inferenceFixture{handler: handler, client: client, account: account}
}

func TestInferenceLifetimeEndsBeforeLocalProcessing(t *testing.T) {
	fixture := newInferenceFixture(t)
	lease, err := BeginInferenceRequest(fixture.client.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	if int64(1) != fixture.account.GetActiveRequests() || int64(1) != fixture.handler.APIKeyConcurrencySnapshot()[7] {
		t.Fatal("upstream request did not occupy account and key")
	}
	observeInferenceTerminal(fixture.client.Request.Context(), "response.completed")
	if int64(0) != fixture.account.GetActiveRequests() || int64(0) != fixture.handler.APIKeyConcurrencySnapshot()[7] {
		t.Fatal("terminal retained capacity during local processing")
	}
	next, err := BeginInferenceRequest(fixture.client.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	lease.Finish()
	if int64(1) != fixture.account.GetActiveRequests() {
		t.Fatal("old attempt released a newer attempt")
	}
	next.Finish()
}

func TestInferenceModeSnapshotSurvivesRuntimeSwitch(t *testing.T) {
	fixture := newInferenceFixture(t)
	UpdateRuntimeSettings(func(current RuntimeSettings) RuntimeSettings {
		current.ConcurrencyAccountingMode = database.ConcurrencyAccountingLegacy
		return current
	})
	if database.ConcurrencyAccountingInference != ResponsesConcurrencyMode(fixture.client.Request.Context()) {
		t.Fatal("request changed mode while running")
	}
	lease, err := BeginInferenceRequest(fixture.client.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	lease.Finish()
	if int64(0) != fixture.account.GetActiveRequests() {
		t.Fatal("mode switch prevented release")
	}
}

func TestInferenceEOFReleasesNonStreamingResponse(t *testing.T) {
	fixture := newInferenceFixture(t)
	lease, err := BeginInferenceRequest(fixture.client.Request.Context())
	if err != nil {
		t.Fatal(err)
	}
	body := &inferenceResponseBody{ReadCloser: io.NopCloser(strings.NewReader(`{"id":"response"}`)), lease: lease}
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	if int64(0) != fixture.account.GetActiveRequests() {
		t.Fatal("EOF retained inference capacity")
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInferenceRejectedKeyDoesNotConsumeAccountDispatch(t *testing.T) {
	fixture := newInferenceFixture(t)
	release, _, ok := fixture.handler.apiKeyConcurrencyLimiter().acquireTracked(7, 1)
	if !ok {
		t.Fatal("expected initial key admission")
	}
	defer release()
	_, err := BeginInferenceRequest(fixture.client.Request.Context())
	if _, matched := inferenceAdmissionFailure(err); !matched {
		t.Fatalf("want local admission rejection, got %v", err)
	}
	if int64(0) != fixture.account.GetActiveRequests() || int64(0) != fixture.account.GetTotalRequests() {
		t.Fatal("key rejection occupied or consumed an account request")
	}
}

func TestInferenceResponsesHTTPCountsActualUpstream(t *testing.T) {
	fixture := newInferenceFixture(t)
	oldResin := resinCfg.Load()
	t.Cleanup(func() { resinCfg.Store(oldResin) })
	observed := make(chan [2]int64, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		observed <- [2]int64{fixture.account.GetActiveRequests(), fixture.handler.APIKeyConcurrencySnapshot()[7]}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"test-complete\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	t.Cleanup(upstream.Close)
	SetResinConfig(&ResinConfig{BaseURL: upstream.URL, PlatformName: "test"})
	body := []byte(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`)
	fixture.client.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(fixture.client.Request.Context())
	fixture.client.Request.Header.Set("Content-Type", "application/json")
	fixture.handler.Responses(fixture.client)
	select {
	case got := <-observed:
		if [2]int64{1, 1} != got {
			t.Fatalf("want occupied account/key during actual upstream, got %v", got)
		}
	default:
		t.Fatal("request did not reach the upstream")
	}
	if int64(0) != fixture.account.GetActiveRequests() || int64(0) != fixture.handler.APIKeyConcurrencySnapshot()[7] {
		t.Fatal("HTTP response leaked inference capacity")
	}
}
