package proxy

import (
	"strings"

	"github.com/codex2api/internal/jsonbody"
)

const responsesImageStripBatchMinBytes = 256 << 10

// stripResponsesImageGenerationCapabilities 合并大正文的图片能力改写，
// 不再为 tools、input、tool_choice、instructions 分别复制整份正文。
func stripLargeResponsesImageGenerationCapabilities(body []byte) []byte {
	editor := jsonbody.New(body)
	stripResponsesTopImageTools(editor)
	indexed := editor.HasChanges()
	if indexed {
		editor.IndexRoot()
	}
	if input := strippedResponsesInputImageTools(body); input != nil {
		editor.SetRaw("input", input)
	}
	stripResponsesImageToolChoice(editor)
	stripResponsesImageBridge(editor)
	if !indexed && editor.HasChanges() {
		editor.IndexRoot()
	}
	return editor.Bytes()
}

func stripResponsesTopImageTools(editor *jsonbody.Editor) {
	tools := editor.Get("tools")
	if !tools.IsArray() {
		return
	}
	kept, removed := stripImageGenerationToolsFromArray(tools.Array())
	if !removed {
		return
	}
	if len(kept) == 0 {
		editor.Delete("tools")
	} else {
		editor.Set("tools", kept)
	}
}

func stripResponsesImageToolChoice(editor *jsonbody.Editor) {
	choice := editor.Get("tool_choice")
	target := strings.TrimSpace(choice.String())
	if choice.IsObject() {
		target = strings.TrimSpace(choice.Get("type").String())
		if target == "namespace" {
			target = strings.TrimSpace(choice.Get("name").String())
		}
	}
	if strings.EqualFold(target, "image_generation") || strings.EqualFold(target, "image_gen") {
		editor.Delete("tool_choice")
	}
}

func stripResponsesImageBridge(editor *jsonbody.Editor) {
	instructions := editor.Get("instructions").String()
	if !strings.Contains(instructions, codexImageGenerationBridgeMarker) {
		return
	}
	cleaned := strings.ReplaceAll(instructions, "\n\n"+codexImageGenerationBridgeText, "")
	cleaned = strings.ReplaceAll(cleaned, codexImageGenerationBridgeText, "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		editor.Delete("instructions")
	} else {
		editor.Set("instructions", cleaned)
	}
}
