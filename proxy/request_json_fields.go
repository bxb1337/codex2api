package proxy

import (
	"github.com/codex2api/internal/jsonbody"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 缺失字段无需改写，避免 SJSON 在返回未修改正文前分配整段副本。
func deleteJSONFieldIfPresent(body []byte, path string) []byte {
	if !gjson.Get(jsonbody.ReadOnlyText(body), path).Exists() {
		return body
	}
	updated, _ := sjson.DeleteBytes(body, path)
	return updated
}
