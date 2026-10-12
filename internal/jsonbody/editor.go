// Package jsonbody 在只读正文上收集字段修改，合并输出以减少整段 JSON 的复制。
package jsonbody

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type change struct {
	path   string
	raw    string
	remove bool
}

const simplePathCharacters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-.$"

// Editor 的读取始终来自原始快照；修改在 Bytes 中统一应用。
type Editor struct {
	body      []byte
	text      string
	changes   []change
	validated bool
	objects   map[string]objectView
}

func New(body []byte) *Editor { return &Editor{body: body} }

func (e *Editor) source() string {
	if e.text == "" {
		e.text = ReadOnlyText(e.body)
	}
	return e.text
}

func (e *Editor) Get(path string) gjson.Result {
	if value, indexed := e.rootValue(path); indexed {
		return value
	}
	return gjson.Get(e.source(), path)
}

// RawBytes 返回原始正文的只读视图，调用方不得修改其内容。
func (e *Editor) RawBytes(path string) []byte {
	value := e.Get(path)
	end := value.Index + len(value.Raw)
	if !value.Exists() || value.Index <= 0 || end > len(e.body) {
		return nil
	}
	return e.body[value.Index:end:end]
}

func (e *Editor) Set(path string, value any) {
	encoded, err := sjson.Set(`{"v":null}`, "v", value)
	if err == nil {
		e.changes = append(e.changes, change{path: path, raw: gjson.Get(encoded, "v").Raw})
	}
}

// SetRaw 借用已经编码的只读 JSON 值；调用方在 Bytes 完成前不得修改 raw。
func (e *Editor) SetRaw(path string, raw []byte) {
	e.changes = append(e.changes, change{path: path, raw: ReadOnlyText(raw)})
}

func (e *Editor) Delete(path string) {
	e.changes = append(e.changes, change{path: path, remove: true})
}

// HasChanges 用于按需建立位置索引，跳过没有改写的大正文扫描。
func (e *Editor) HasChanges() bool { return len(e.changes) > 0 }

func (e *Editor) Bytes() []byte {
	if len(e.changes) == 0 {
		return e.body
	}
	patches, ok := e.patches()
	if !ok {
		return e.sequential()
	}
	return applyPatches(e.body, patches)
}

func (e *Editor) sequential() []byte {
	out := e.body
	for _, edit := range e.changes {
		var next []byte
		var err error
		if edit.remove {
			next, err = sjson.DeleteBytes(out, edit.path)
		} else {
			next, err = sjson.SetRawBytes(out, edit.path, []byte(edit.raw))
		}
		if err == nil {
			out = next
		}
	}
	return out
}

func simplePath(path string) bool {
	if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
		return false
	}
	for _, char := range path {
		if !strings.ContainsRune(simplePathCharacters, char) {
			return false
		}
	}
	return true
}

func (e *Editor) normalizedChanges() ([]change, bool) {
	result := make([]change, 0, len(e.changes))
	seen := make(map[string]int)
	for _, edit := range e.changes {
		if !simplePath(edit.path) {
			return nil, false
		}
		index, exists := seen[edit.path]
		if exists {
			if edit.remove || result[index].remove {
				return nil, false
			}
			result[index] = edit
			continue
		}
		seen[edit.path] = len(result)
		result = append(result, edit)
	}
	for _, edit := range result {
		path := edit.path
		for index := strings.LastIndexByte(path, '.'); index >= 0; index = strings.LastIndexByte(path, '.') {
			path = path[:index]
			if _, exists := seen[path]; exists {
				return nil, false
			}
		}
	}
	return result, true
}
