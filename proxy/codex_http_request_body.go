package proxy

import (
	"strings"

	"github.com/codex2api/internal/jsonbody"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexHTTPBatchBodyMinBytes = 16 << 10

// prepareCodexHTTPRequestBody 将 HTTP 专用的字段增删合并为一次正文复制。
// 正文仍保持只读，供重试、签名检查和响应上下文缓存复用。
func prepareCodexHTTPRequestBody(body []byte, sessionID string) ([]byte, string) {
	if len(body) < codexHTTPBatchBodyMinBytes {
		return prepareSmallCodexHTTPRequestBody(body, sessionID)
	}
	editor := jsonbody.New(body)
	editor.IndexRoot()
	if !editor.Get("instructions").Exists() {
		editor.Set("instructions", "")
	}
	// HTTP 不接受 WS 信封和以下存储控制字段；嵌套的 type 不受影响。
	for _, field := range []string{
		"previous_response_id", "prompt_cache_retention", "safety_identifier",
		"disable_response_storage", "type",
	} {
		editor.Delete(field)
	}
	cacheKey := strings.TrimSpace(editor.Get("prompt_cache_key").String())
	if sessionID != "" {
		cacheKey = sessionID
		editor.Set("prompt_cache_key", cacheKey)
	}
	return editor.Bytes(), strings.Clone(cacheKey)
}

func prepareSmallCodexHTTPRequestBody(body []byte, sessionID string) ([]byte, string) {
	if !gjson.GetBytes(body, "instructions").Exists() {
		body, _ = sjson.SetBytes(body, "instructions", "")
	}
	for _, field := range []string{
		"previous_response_id", "prompt_cache_retention", "safety_identifier",
		"disable_response_storage", "type",
	} {
		body, _ = sjson.DeleteBytes(body, field)
	}
	cacheKey := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	if sessionID != "" {
		cacheKey = sessionID
		body, _ = sjson.SetBytes(body, "prompt_cache_key", cacheKey)
	}
	return body, cacheKey
}
