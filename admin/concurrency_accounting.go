package admin

import (
	"net/http"

	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

func validateConcurrencyAccountingMode(c *gin.Context, mode *string) bool {
	if mode == nil || database.ValidConcurrencyAccountingMode(*mode) {
		return true
	}
	writeError(c, http.StatusBadRequest, "并发计算方式必须为 legacy 或 inference")
	return false
}

func (h *Handler) commitConcurrencyAccountingMode(c *gin.Context, mode *string) bool {
	if mode == nil {
		return true
	}
	if err := h.db.UpdateConcurrencyAccountingMode(c.Request.Context(), *mode); err != nil {
		writeError(c, http.StatusInternalServerError, "保存并发计算方式失败，设置未生效")
		return false
	}
	proxy.UpdateRuntimeSettings(func(current proxy.RuntimeSettings) proxy.RuntimeSettings {
		current.ConcurrencyAccountingMode = *mode
		return current
	})
	return true
}
