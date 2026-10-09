package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestConcurrencyAccountingPersistsWithoutUnrelatedOverwrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrency.db")
	db, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSystemSettings(ctx, &SystemSettings{MaxConcurrency: 2}); err != nil {
		t.Fatal(err)
	}
	assertConcurrencyMode(t, db, ConcurrencyAccountingLegacy)
	if err := db.UpdateConcurrencyAccountingMode(ctx, ConcurrencyAccountingInference); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSystemSettings(ctx, &SystemSettings{MaxConcurrency: 3}); err != nil {
		t.Fatal(err)
	}
	assertConcurrencyMode(t, db, ConcurrencyAccountingInference)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertConcurrencyMode(t, db, ConcurrencyAccountingInference)
	if err := db.UpdateConcurrencyAccountingMode(ctx, "invalid"); err == nil {
		t.Fatal("invalid accounting mode was accepted")
	}
	assertConcurrencyMode(t, db, ConcurrencyAccountingInference)
}

func assertConcurrencyMode(t *testing.T, db *DB, want string) {
	t.Helper()
	settings, err := db.GetSystemSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want != settings.ConcurrencyAccountingMode {
		t.Fatalf("want mode %q, got %q", want, settings.ConcurrencyAccountingMode)
	}
}
