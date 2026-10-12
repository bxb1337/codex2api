package proxy

import (
	"bytes"

	"github.com/codex2api/internal/jsonbody"
	json "github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

func stripResponseItemID(raw json.RawMessage) (json.RawMessage, bool) {
	// 缺少 id 时直接保留原始项，避免解码整段消息或工具输出。
	if !responseItemMayHaveID(raw) {
		return raw, true
	}
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil || item == nil {
		return raw, true
	}
	if _, exists := item["id"]; !exists {
		return raw, true
	}
	delete(item, "id")
	stripped, err := json.Marshal(item)
	if err != nil {
		return nil, false
	}
	return stripped, true
}

func responseItemMayHaveID(raw []byte) bool {
	if bytes.Contains(raw, []byte(`"id"`)) {
		return true
	}
	// 转义后的键名也可能表示 id；普通正文直接走字节扫描。
	return bytes.IndexByte(raw, '\\') >= 0 && gjson.Get(jsonbody.ReadOnlyText(raw), "id").Exists()
}
