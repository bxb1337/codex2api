package proxy

import "encoding/json"

// 模型映射关闭 Lite 时，同时恢复普通 Responses 的工具声明位置。
func normalizeNonLiteResponsesBody(payload []byte) []byte {
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil {
		return payload
	}
	if reasoning, ok := body["reasoning"].(map[string]any); ok && reasoning["context"] == "all_turns" {
		delete(reasoning, "context")
	}
	liftNonLiteResponsesTools(body)
	result, err := json.Marshal(body)
	if err != nil {
		return payload
	}
	return result
}

func liftNonLiteResponsesTools(body map[string]any) {
	input, ok := body["input"].([]any)
	if !ok {
		return
	}
	tools, _ := body["tools"].([]any)
	kept := make([]any, 0, len(input))
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item["type"] != "additional_tools" {
			kept = append(kept, raw)
			continue
		}
		additional, _ := item["tools"].([]any)
		tools = append(tools, additional...)
	}
	body["input"] = kept
	if len(tools) > maxTools {
		tools = truncateToolsPreservingImageGeneration(tools)
	}
	if len(tools) > 0 {
		body["tools"] = tools
		normalizeResponsesFunctionTools(body)
	}
}
