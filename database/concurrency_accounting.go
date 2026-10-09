package database

import (
	"context"
	"errors"
)

const (
	ConcurrencyAccountingLegacy    = "legacy"
	ConcurrencyAccountingInference = "inference"
)

func ValidConcurrencyAccountingMode(mode string) bool {
	return mode == ConcurrencyAccountingLegacy || mode == ConcurrencyAccountingInference
}

func NormalizeConcurrencyAccountingMode(mode string) string {
	if mode == ConcurrencyAccountingInference {
		return mode
	}
	return ConcurrencyAccountingLegacy
}

// UpdateConcurrencyAccountingMode 单独更新，避免无关设置覆盖正在使用的模式。
func (db *DB) UpdateConcurrencyAccountingMode(ctx context.Context, mode string) error {
	if !ValidConcurrencyAccountingMode(mode) {
		return errors.New("concurrency_accounting_mode 必须为 legacy 或 inference")
	}
	_, err := db.conn.ExecContext(ctx, `INSERT INTO system_settings (id, concurrency_accounting_mode)
		VALUES (1, $1) ON CONFLICT (id) DO UPDATE
		SET concurrency_accounting_mode = EXCLUDED.concurrency_accounting_mode`, mode)
	return err
}
