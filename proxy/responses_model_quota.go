package proxy

import (
	"context"

	"github.com/tidwall/gjson"
)

// ConsumeResponsesModelRequestQuota 仅对实际生成消耗模型请求次数。
func ConsumeResponsesModelRequestQuota(ctx context.Context, body []byte) error {
	if gjson.GetBytes(body, "generate").Type == gjson.False {
		return nil
	}
	return ConsumeAPIKeyModelRequestQuota(ctx, gjson.GetBytes(body, "model").String())
}
