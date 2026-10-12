package api

import (
	"github.com/codex2api/internal/jsonbody"
	"github.com/tidwall/gjson"
)

// ValidateRequestReadOnly 借用正文执行同步校验，避免复制整份请求。
// 规则不得改写正文或保留解析结果；自定义规则继续使用 ValidateRequest。
func (v *Validator) ValidateRequestReadOnly(rules map[string][]ValidationRule) *ValidationResult {
	return v.validateRequest(gjson.Parse(jsonbody.ReadOnlyText(v.Body)), rules)
}
