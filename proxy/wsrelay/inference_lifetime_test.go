package wsrelay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

const inferenceWSFixtureKey = "sk-inference-ws-fixture"

type inferenceWSFixture struct {
	account *auth.Account
	handler *proxy.Handler
	keyID   int64
	manager *Manager
	url     string
	frames  atomic.Int32
}

func newInferenceWSFixture(t *testing.T) *inferenceWSFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "inference.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	keyID, err := db.InsertAPIKeyWithOptions(context.Background(), database.APIKeyInput{Key: inferenceWSFixtureKey, Limits: database.APIKeyLimits{MaxConcurrency: 1}})
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 1})
	t.Cleanup(store.Stop)
	account := &auth.Account{DBID: 1, AccountID: "fixture-account", AccessToken: "fixture-token", PlanType: "plus"}
	store.AddAccount(account)
	handler := proxy.NewHandler(store, db, &config.Config{}, nil)
	fixture := &inferenceWSFixture{account: account, handler: handler, keyID: keyID, manager: NewManager()}
	t.Cleanup(fixture.manager.Stop)
	fixture.configure(t)
	upstream := httptest.NewServer(http.HandlerFunc(fixture.upstream(t)))
	t.Cleanup(upstream.Close)
	proxy.SetResinConfig(&proxy.ResinConfig{BaseURL: upstream.URL, PlatformName: "test"})
	router := gin.New()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	fixture.url = "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	return fixture
}

func (fixture *inferenceWSFixture) configure(t *testing.T) {
	t.Helper()
	oldMode, oldResin, oldHook := proxy.CurrentRuntimeSettings(), proxy.GetResinConfig(), proxy.WebsocketExecuteFunc
	executor := GetExecutor()
	oldManager := executor.manager
	executor.manager = fixture.manager
	proxy.WebsocketExecuteFunc = ExecuteRequestWebsocket
	t.Cleanup(func() {
		proxy.ApplyRuntimeSettings(oldMode)
		proxy.SetResinConfig(oldResin)
		proxy.WebsocketExecuteFunc = oldHook
		executor.manager = oldManager
	})
	proxy.UpdateRuntimeSettings(func(settings proxy.RuntimeSettings) proxy.RuntimeSettings {
		settings.ConcurrencyAccountingMode = database.ConcurrencyAccountingInference
		return settings
	})
}

func (fixture *inferenceWSFixture) upstream(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if int64(0) != fixture.account.GetActiveRequests() || int64(0) != fixture.handler.APIKeyConcurrencySnapshot()[fixture.keyID] {
			t.Error("handshake occupied inference capacity")
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if int64(1) != fixture.account.GetActiveRequests() || int64(1) != fixture.handler.APIKeyConcurrencySnapshot()[fixture.keyID] {
				t.Error("actual inference was not counted for account and key")
			}
			id := fixture.frames.Add(1)
			terminal := fmt.Sprintf(`{"type":"response.completed","response":{"id":"response_%d","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`, id)
			if err := conn.WriteMessage(websocket.TextMessage, []byte(terminal)); err != nil {
				return
			}
		}
	}
}

func TestInferenceWSIdleConnectionsDoNotExhaustAccountOrKey(t *testing.T) {
	fixture := newInferenceWSFixture(t)
	for index := 0; index < 10; index++ {
		headers := http.Header{"Authorization": {"Bearer " + inferenceWSFixtureKey}, "Session-Id": {fmt.Sprintf("thread-%d", index)}}
		conn, _, err := websocket.DefaultDialer.Dial(fixture.url, headers)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.5","store":false,"input":[{"role":"user","content":"hello"}]}`)); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, terminal, err := conn.ReadMessage()
		if err != nil || "response.completed" != gjson.GetBytes(terminal, "type").String() {
			t.Fatalf("inference rejected while earlier connections idle: %v %s", err, terminal)
		}
		if int64(0) != fixture.account.GetActiveRequests() || int64(0) != fixture.handler.APIKeyConcurrencySnapshot()[fixture.keyID] {
			t.Fatal("terminal retained inference capacity")
		}
		if index == 1 && fixture.manager.ConnectionCount() != 2 {
			t.Fatal("idle upstream continuation was not retained")
		}
		if fixture.manager.ConnectionCount() > 9 {
			t.Fatal("physical connection pool exceeded bounded idle headroom")
		}
	}
	if int32(10) != fixture.frames.Load() {
		t.Fatal("not all idle-connected clients could infer")
	}
}
