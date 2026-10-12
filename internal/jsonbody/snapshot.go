package jsonbody

import (
	"strings"
	"unicode/utf8"

	json "github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

const (
	snapshotMinBodyBytes     = 256 << 10
	snapshotMaxNodes         = 4096
	snapshotMaxDepth         = 64
	snapshotMaxEdits         = 128
	snapshotMaxEditBytes     = 64 << 10
	snapshotDensityNodeBytes = 64
	snapshotMaxBodyExpansion = 2
)

const snapshotKeyCharacters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-$"

// Snapshot 记录已解码正文的结构；字符串仍共享解码结果，不复制大文本。
// 归一化完成后，仅重新编码变化的小字段，保留其余 JSON 的原始字节。
type Snapshot struct {
	body     []byte
	original map[string]any
}

// Capture 只用于已经成功解码的正文。
// 小正文、复杂结构和需要缩短或修复 Unicode 转义的输入沿用完整编码。
func Capture(body []byte, value map[string]any) *Snapshot {
	if len(body) < snapshotMinBodyBytes {
		return nil
	}
	state := snapshotCloneState{}
	original, ok := state.clone(value, 0)
	if !ok {
		return nil
	}
	// 空白占据大部分正文时，完整编码能够显著缩小出站载荷。
	approximate := state.stringBytes + state.nodes*snapshotDensityNodeBytes
	if len(body) > approximate*snapshotMaxBodyExpansion {
		return nil
	}
	// 先排除需要缩短的大正文，再扫描可原样输出的 HTML 转义。
	if !snapshotStableEscapes(body) || !utf8.Valid(body) {
		return nil
	}
	return &Snapshot{body: body, original: original.(map[string]any)}
}

// Marshal 返回只读正文；没有实际改写时可以复用捕获的缓冲。
func (s *Snapshot) Marshal(value map[string]any) ([]byte, error) {
	if s == nil {
		return json.Marshal(value)
	}
	editor := New(s.body)
	editor.validated = true
	editor.objects = make(map[string]objectView)
	state := snapshotDiffState{editor: editor}
	if !state.object(snapshotObject{before: s.original, after: value, source: gjson.Parse(editor.source())}, "") {
		return json.Marshal(value)
	}
	patches, ok := editor.patches()
	if !ok {
		return json.Marshal(value)
	}
	return applyPatches(s.body, patches), nil
}

type snapshotCloneState struct {
	nodes       int
	stringBytes int
}

func (s *snapshotCloneState) clone(value any, depth int) (any, bool) {
	s.nodes++
	if s.nodes > snapshotMaxNodes || depth > snapshotMaxDepth {
		return nil, false
	}
	switch value := value.(type) {
	case map[string]any:
		return s.object(value, depth+1)
	case []any:
		return s.array(value, depth+1)
	case string:
		s.stringBytes += len(value)
		return value, true
	default:
		return value, true
	}
}

func (s *snapshotCloneState) object(value map[string]any, depth int) (any, bool) {
	if value == nil {
		return map[string]any(nil), true
	}
	if len(value) > snapshotMaxNodes-s.nodes {
		return nil, false
	}
	out := make(map[string]any, len(value))
	for key, item := range value {
		if !snapshotSimpleKey(key) {
			return nil, false
		}
		copy, ok := s.clone(item, depth)
		if !ok {
			return nil, false
		}
		out[key] = copy
	}
	return out, true
}

func (s *snapshotCloneState) array(value []any, depth int) (any, bool) {
	if value == nil {
		return []any(nil), true
	}
	if len(value) > snapshotMaxNodes-s.nodes {
		return nil, false
	}
	out := make([]any, len(value))
	for index, item := range value {
		copy, ok := s.clone(item, depth)
		if !ok {
			return nil, false
		}
		out[index] = copy
	}
	return out, true
}

func snapshotSimpleKey(key string) bool {
	if key == "" {
		return false
	}
	for _, char := range key {
		if !strings.ContainsRune(snapshotKeyCharacters, char) {
			return false
		}
	}
	return true
}
