package jsonbody

import "github.com/tidwall/gjson"

// IndexRoot 为顶层字段改写建立一次只读位置索引，避免反复扫描大 input。
// 校验失败时继续使用 Editor 原有的顺序处理路径。
func (e *Editor) IndexRoot() {
	if !gjson.Valid(e.source()) {
		return
	}
	parent := gjson.Parse(e.source())
	members, ok := rawObjectMembers(parent)
	if !ok {
		return
	}
	e.validated = true
	if e.objects == nil {
		e.objects = make(map[string]objectView)
	}
	e.objects[""] = objectView{parent: parent, members: members}
}

func (e *Editor) rootValue(path string) (gjson.Result, bool) {
	root, indexed := e.objects[""]
	if !indexed || !snapshotSimpleKey(path) {
		return gjson.Result{}, false
	}
	for _, field := range root.members {
		if field.name == path {
			value := gjson.Parse(field.value.Raw)
			value.Index = field.value.Index
			return value, true
		}
	}
	return gjson.Result{}, true
}
