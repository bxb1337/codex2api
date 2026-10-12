package proxy

import (
	"net/http"
	"strings"

	"github.com/codex2api/internal/jsonbody"
	json "github.com/goccy/go-json"
)

type responsesLiteTransport struct {
	headers   http.Header
	websocket bool
	enabled   bool
}

// apply 统一收集传输标记修改，保留请求头的只读归属。
func (transport responsesLiteTransport) apply(editor *jsonbody.Editor) http.Header {
	headers := transport.headers
	cloned := false
	if headers != nil && headers.Get(codexResponsesLiteHeader) != "" {
		headers = headers.Clone()
		cloned = true
		headers.Del(codexResponsesLiteHeader)
	}
	if transport.websocket && transport.enabled {
		editor.Set(codexResponsesLiteWSMetadataPath, "true")
	} else if editor.Get(codexResponsesLiteWSMetadataPath).Exists() {
		// 门禁剥离信号或切回 HTTP 时清理正文中的残留标记。
		editor.Delete(codexResponsesLiteWSMetadataPath)
	}
	if !transport.websocket && transport.enabled {
		if !cloned {
			headers = headers.Clone()
		}
		if headers == nil {
			headers = make(http.Header)
		}
		headers.Set(codexResponsesLiteHeader, "true")
	}
	return headers
}

// prepareCodexResponsesLiteTransport 保留只收敛传输信号的调用边界。
func prepareCodexResponsesLiteTransport(body []byte, transport responsesLiteTransport) ([]byte, http.Header) {
	editor := jsonbody.New(body)
	forwardHeaders := transport.apply(editor)
	return editor.Bytes(), forwardHeaders
}

// prepareCodexResponsesLiteRequest 将标记与正文约束合并为一次输出。
func prepareCodexResponsesLiteRequest(body []byte, transport responsesLiteTransport) ([]byte, http.Header) {
	editor := jsonbody.New(body)
	headers := transport.apply(editor)
	if transport.enabled {
		normalizeCodexResponsesLiteEditor(editor, !transport.websocket)
	}
	return editor.Bytes(), headers
}

// Lite 要求 context=all_turns、parallel_tool_calls=false；HTTP 还需移除
// hosted image_generation 工具，WS 保留它以支持生图桥接。
func normalizeCodexResponsesLiteBody(body []byte, stripHostedTools bool) []byte {
	editor := jsonbody.New(body)
	normalizeCodexResponsesLiteEditor(editor, stripHostedTools)
	return editor.Bytes()
}

func normalizeCodexResponsesLiteEditor(editor *jsonbody.Editor, stripHostedTools bool) {
	editor.Set("parallel_tool_calls", false)
	editor.Set("reasoning.context", "all_turns")
	if !stripHostedTools {
		return
	}
	if tools := editor.Get("tools"); tools.IsArray() {
		kept := make([]json.RawMessage, 0, len(tools.Array()))
		removed := false
		for _, tool := range tools.Array() {
			if strings.EqualFold(strings.TrimSpace(tool.Get("type").String()), "image_generation") {
				removed = true
				continue
			}
			kept = append(kept, json.RawMessage(tool.Raw))
		}
		if removed {
			if len(kept) == 0 {
				editor.Delete("tools")
			} else if raw, err := json.Marshal(kept); err == nil {
				editor.Set("tools", json.RawMessage(raw))
			}
			if strings.EqualFold(strings.TrimSpace(editor.Get("tool_choice.type").String()), "image_generation") {
				editor.Delete("tool_choice")
			}
		}
	}
	if instructions := editor.Get("instructions").String(); strings.Contains(instructions, codexImageGenerationBridgeMarker) {
		editor.Set("instructions", removeCodexImageGenerationBridgeText(instructions))
	}
}
