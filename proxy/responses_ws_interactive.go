package proxy

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// 交互轮次公开生命周期后不再整轮重放，响应 ID 必须保持真实归属。
func responsesWSInteractive(c *gin.Context) bool {
	raw, _ := c.Get("raw_body")
	body, _ := raw.([]byte)
	model := strings.ToLower(gjson.GetBytes(body, "model").String())
	supports, known := responsesLiteSupportForModel(model)
	return gjson.GetBytes(body, "stream_id").String() != "" || strings.HasPrefix(model, "gpt-6") ||
		known && supports || gateResponsesLiteForModel(codexResponsesLiteRequested(body, c.Request.Header), body)
}
