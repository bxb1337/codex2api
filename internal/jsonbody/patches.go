package jsonbody

import (
	"sort"
	"strings"

	"github.com/tidwall/gjson"
)

type patch struct {
	start int
	end   int
	raw   string
}

func (e *Editor) patches() ([]patch, bool) {
	edits, ok := e.normalizedChanges()
	if !ok || (!e.validated && !gjson.Valid(e.source())) {
		return nil, false
	}
	groups := make(map[string][]change)
	for _, edit := range edits {
		parent := edit.path
		if index := strings.LastIndexByte(parent, '.'); index >= 0 {
			parent = parent[:index]
		} else {
			parent = ""
		}
		groups[parent] = append(groups[parent], edit)
	}
	var result []patch
	for path, group := range groups {
		parent := gjson.Parse(e.source())
		if cached, exists := e.objects[path]; exists {
			parent = cached.parent
		} else if path != "" {
			parent = e.Get(path)
		}
		patches, valid := e.objectPatches(parent, group)
		if !valid {
			return nil, false
		}
		result = append(result, patches...)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].start < result[j].start })
	end := 0
	for _, item := range result {
		if item.start < end || item.end < item.start || item.end > len(e.body) {
			return nil, false
		}
		end = item.end
	}
	return result, true
}

func applyPatches(body []byte, patches []patch) []byte {
	if len(patches) == 0 {
		return body
	}
	size := len(body)
	for _, item := range patches {
		size += len(item.raw) - (item.end - item.start)
	}
	out := make([]byte, 0, size)
	position := 0
	for _, item := range patches {
		out = append(out, body[position:item.start]...)
		out = append(out, item.raw...)
		position = item.end
	}
	return append(out, body[position:]...)
}
