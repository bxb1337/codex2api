package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
)

func TestConcurrencyAccountingSettingsPartialUpdate(t *testing.T) {
	handler, db, _ := newResponseCacheSettingsAdminHandler(t)
	old := proxy.CurrentRuntimeSettings()
	t.Cleanup(func() {
		proxy.UpdateRuntimeSettings(func(proxy.RuntimeSettings) proxy.RuntimeSettings { return old })
	})
	for _, patch := range []map[string]any{
		{"concurrency_accounting_mode": "inference"},
		{"site_name": "other setting"},
	} {
		response := invokeResponseCacheSettingsAdmin(t, handler, http.MethodPut, patch)
		if http.StatusOK != response.Code {
			t.Fatalf("want 200, got %d: %s", response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if database.ConcurrencyAccountingInference != payload["concurrency_accounting_mode"] {
			t.Fatalf("want inference response, got %+v", payload["concurrency_accounting_mode"])
		}
		persisted, err := db.GetSystemSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if database.ConcurrencyAccountingInference != persisted.ConcurrencyAccountingMode {
			t.Fatalf("want persisted inference, got %q", persisted.ConcurrencyAccountingMode)
		}
	}
}

func TestConcurrencyAccountingRejectsInvalidBeforeSideEffects(t *testing.T) {
	response := invokeResponseCacheSettingsAdmin(t, &Handler{}, http.MethodPut,
		map[string]any{"concurrency_accounting_mode": "invalid"})
	if http.StatusBadRequest != response.Code {
		t.Fatalf("want 400, got %d", response.Code)
	}
}
