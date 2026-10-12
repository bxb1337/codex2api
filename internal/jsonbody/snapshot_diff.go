package jsonbody

import (
	"reflect"
	"strconv"

	json "github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

type objectView struct {
	parent  gjson.Result
	members []member
}

type snapshotObject struct {
	before map[string]any
	after  map[string]any
	source gjson.Result
}

type snapshotDiffState struct {
	editor *Editor
	edits  int
}

func (s *snapshotDiffState) object(object snapshotObject, path string) bool {
	members, ok := rawObjectMembers(object.source)
	if !ok || len(members) != len(object.before) {
		return false
	}
	s.editor.objects[path] = objectView{parent: object.source, members: members}
	for _, field := range members {
		if !s.objectField(field, object, path) {
			return false
		}
	}
	for key, value := range object.after {
		if _, exists := object.before[key]; !exists {
			if !snapshotSimpleKey(key) || !s.set(snapshotPath(path, key), value) {
				return false
			}
		}
	}
	return true
}

func (s *snapshotDiffState) objectField(field member, object snapshotObject, parent string) bool {
	path := snapshotPath(parent, field.name)
	current, exists := object.after[field.name]
	if !exists {
		return s.remove(path)
	}
	original, exists := object.before[field.name]
	location := snapshotValue{path: path, source: field.value, objectParent: true}
	return exists && s.value(original, current, location)
}

type snapshotValue struct {
	path         string
	source       gjson.Result
	objectParent bool
}

func (s *snapshotDiffState) value(before, after any, location snapshotValue) bool {
	switch before := before.(type) {
	case map[string]any:
		return s.objectValue(before, after, location)
	case []any:
		if after, ok := after.([]any); ok {
			return s.array(before, after, location)
		}
	case float64:
		// 默认解码为 float64，保留完整 Marshal 的数值归一化。
		return location.objectParent && s.set(location.path, after)
	default:
		if reflect.DeepEqual(before, after) {
			return true
		}
	}
	return location.objectParent && s.set(location.path, after)
}

func (s *snapshotDiffState) objectValue(before map[string]any, after any, location snapshotValue) bool {
	current, ok := after.(map[string]any)
	if !ok || before == nil || current == nil {
		return location.objectParent && s.set(location.path, after)
	}
	return s.object(snapshotObject{before: before, after: current, source: location.source}, location.path)
}

func (s *snapshotDiffState) array(before, after []any, location snapshotValue) bool {
	if len(before) != len(after) || (before == nil) != (after == nil) {
		return location.objectParent && s.set(location.path, after)
	}
	cursor, ok := newRawCursor(location.source, false)
	if !ok {
		return false
	}
	for index := range before {
		field, exists := cursor.next()
		item := snapshotValue{path: snapshotPath(location.path, strconv.Itoa(index)), source: field.value}
		if !exists || !s.value(before[index], after[index], item) {
			return false
		}
	}
	_, excess := cursor.next()
	return !excess
}

func (s *snapshotDiffState) remove(path string) bool {
	s.edits++
	if s.edits > snapshotMaxEdits {
		return false
	}
	s.editor.Delete(path)
	return true
}

func (s *snapshotDiffState) set(path string, value any) bool {
	s.edits++
	budget := snapshotValueBudget{remaining: snapshotMaxEditBytes}
	if s.edits > snapshotMaxEdits || !snapshotSmallValue(value, &budget) {
		return false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	s.editor.changes = append(s.editor.changes, change{path: path, raw: ReadOnlyText(raw)})
	return true
}

type snapshotValueBudget struct {
	remaining int
	depth     int
}

func snapshotSmallValue(value any, budget *snapshotValueBudget) bool {
	budget.remaining--
	budget.depth++
	defer func() { budget.depth-- }()
	if budget.remaining < 0 || budget.depth > snapshotMaxDepth {
		return false
	}
	switch value := value.(type) {
	case string:
		budget.remaining -= len(value)
	case map[string]any:
		return snapshotSmallObject(value, budget)
	case []any:
		return snapshotSmallArray(value, budget)
	case []string:
		for _, item := range value {
			budget.remaining -= len(item) + 1
		}
	case nil, bool, float64, float32, int, int64, uint, uint64, json.Number:
	default:
		return false
	}
	return budget.remaining >= 0
}

func snapshotSmallArray(value []any, budget *snapshotValueBudget) bool {
	for _, item := range value {
		if !snapshotSmallValue(item, budget) {
			return false
		}
	}
	return budget.remaining >= 0
}

func snapshotSmallObject(value map[string]any, budget *snapshotValueBudget) bool {
	for key, item := range value {
		budget.remaining -= len(key)
		if !snapshotSmallValue(item, budget) {
			return false
		}
	}
	return budget.remaining >= 0
}

func snapshotPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}
