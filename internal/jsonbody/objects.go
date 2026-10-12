package jsonbody

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

type member struct {
	name  string
	start int
	value gjson.Result
}

type objectState struct {
	parent    gjson.Result
	members   []member
	deleted   []bool
	remaining map[string]change
}

func objectMembers(parent gjson.Result) ([]member, bool) {
	if !parent.IsObject() {
		return nil, false
	}
	var members []member
	seen := make(map[string]bool)
	valid := true
	parent.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		if seen[name] {
			valid = false
			return false
		}
		seen[name] = true
		members = append(members, member{name: name, start: key.Index, value: value})
		return true
	})
	return members, valid
}

func (e *Editor) objectPatches(parent gjson.Result, edits []change) ([]patch, bool) {
	members, ok := e.patchObjectMembers(parent, edits)
	if !ok {
		return nil, false
	}
	byName := make(map[string]change)
	for _, edit := range edits {
		name := edit.path[strings.LastIndexByte(edit.path, '.')+1:]
		byName[name] = edit
	}
	deleted := make([]bool, len(members))
	var result []patch
	for index, field := range members {
		edit, exists := byName[field.name]
		if !exists {
			continue
		}
		deleted[index] = edit.remove
		if !edit.remove && field.value.Raw != edit.raw {
			result = append(result, patch{start: field.value.Index, end: field.value.Index + len(field.value.Raw), raw: edit.raw})
		}
		delete(byName, field.name)
	}
	result = append(result, deletionPatches(members, deleted)...)
	state := objectState{parent: parent, members: members, deleted: deleted, remaining: byName}
	if inserted := state.insertionPatch(edits); inserted.raw != "" {
		result = append(result, inserted)
	}
	return result, true
}

func (e *Editor) patchObjectMembers(parent gjson.Result, edits []change) ([]member, bool) {
	path := edits[0].path
	if index := strings.LastIndexByte(path, '.'); index >= 0 {
		path = path[:index]
	} else {
		path = ""
	}
	if cached, exists := e.objects[path]; exists {
		return cached.members, true
	}
	return objectMembers(parent)
}

func deletionPatches(members []member, deleted []bool) []patch {
	var result []patch
	for index := 0; index < len(members); index++ {
		if !deleted[index] {
			continue
		}
		first := index
		for index+1 < len(members) && deleted[index+1] {
			index++
		}
		item := patch{start: members[first].start, end: members[index].value.Index + len(members[index].value.Raw)}
		if first > 0 {
			previous := members[first-1].value
			item.start = previous.Index + len(previous.Raw)
		} else if index+1 < len(members) {
			item.end = members[index+1].start
		}
		result = append(result, item)
	}
	return result
}

func (s objectState) insertionPatch(edits []change) patch {
	var fields []string
	for _, edit := range edits {
		name := edit.path[strings.LastIndexByte(edit.path, '.')+1:]
		if _, missing := s.remaining[name]; missing && !edit.remove {
			fields = append(fields, strconv.Quote(name)+":"+edit.raw)
		}
	}
	if len(fields) == 0 {
		return patch{}
	}
	raw := strings.Join(fields, ",")
	for index := range s.members {
		if !s.deleted[index] {
			raw = "," + raw
			break
		}
	}
	position := s.parent.Index + len(strings.TrimRight(s.parent.Raw, " \t\r\n")) - 1
	return patch{start: position, end: position, raw: raw}
}
