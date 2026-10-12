package proxy

import (
	"strings"

	"github.com/codex2api/internal/jsonbody"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func strippedResponsesInputImageTools(body []byte) []byte {
	text := jsonbody.ReadOnlyText(body)
	// 只匹配载体类型，普通历史项保持不透明；兼容空白和 JSON 转义。
	if !gjson.Get(text, `input.#(type%"*additional_tools*")`).Exists() {
		return nil
	}
	input := gjson.Get(text, "input")
	if !input.IsArray() {
		return nil
	}
	items := input.Array()
	replacements := strippedResponsesImageCarriers(items)
	if len(replacements) == 0 {
		return nil
	}
	return encodeStrippedResponsesImageInput(items, replacements)
}

func strippedResponsesImageCarriers(items []gjson.Result) map[int][]byte {
	var replacements map[int][]byte
	for index, item := range items {
		replacement, changed := strippedResponsesImageCarrier(item)
		if !changed {
			continue
		}
		if replacements == nil {
			replacements = make(map[int][]byte)
		}
		replacements[index] = replacement
	}
	return replacements
}

func strippedResponsesImageCarrier(item gjson.Result) ([]byte, bool) {
	if strings.TrimSpace(item.Get("type").String()) != "additional_tools" {
		return nil, false
	}
	nested := item.Get("tools")
	if !nested.IsArray() {
		return nil, false
	}
	kept, removed := stripImageGenerationToolsFromArray(nested.Array())
	if !removed {
		return nil, false
	}
	if len(kept) == 0 {
		return nil, true
	}
	replacement, err := sjson.SetBytes([]byte(item.Raw), "tools", kept)
	return replacement, err == nil
}

func encodeStrippedResponsesImageInput(items []gjson.Result, replacements map[int][]byte) []byte {
	out := make([]byte, 0, strippedResponsesImageInputSize(items, replacements))
	out = append(out, '[')
	for index, item := range items {
		replacement, changed := replacements[index]
		if changed && replacement == nil {
			continue
		}
		if len(out) > 1 {
			out = append(out, ',')
		}
		if changed {
			out = append(out, replacement...)
		} else {
			out = append(out, item.Raw...)
		}
	}
	return append(out, ']')
}

func strippedResponsesImageInputSize(items []gjson.Result, replacements map[int][]byte) int {
	size, count := 2, 0
	for index, item := range items {
		replacement, changed := replacements[index]
		if changed && replacement == nil {
			continue
		}
		if changed {
			size += len(replacement)
		} else {
			size += len(item.Raw)
		}
		count++
	}
	return size + max(count-1, 0)
}
