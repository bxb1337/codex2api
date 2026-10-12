package jsonbody

import (
	"strings"

	json "github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

// rawCursor 只定位已经通过校验的 JSON token，不解码字符串值。
// 这样收集修改位置时不会再次分配大型文本或工具输出。
type rawCursor struct {
	source   gjson.Result
	position int
	object   bool
}

func newRawCursor(source gjson.Result, object bool) (rawCursor, bool) {
	position := rawSkipSpace(source.Raw, 0)
	opening := byte('[')
	if object {
		opening = '{'
	}
	if position >= len(source.Raw) || source.Raw[position] != opening {
		return rawCursor{}, false
	}
	return rawCursor{source: source, position: position + 1, object: object}, true
}

func (c *rawCursor) next() (member, bool) {
	raw := c.source.Raw
	position := rawSkipSpace(raw, c.position)
	if position < len(raw) && raw[position] == ',' {
		position = rawSkipSpace(raw, position+1)
	}
	if position >= len(raw) || raw[position] == '}' || raw[position] == ']' {
		return member{}, false
	}
	field := member{start: c.source.Index + position}
	if c.object {
		key, next, valid := c.objectField(position)
		if !valid {
			return member{}, false
		}
		field, position = key, next
	}
	end := rawValueEnd(raw, position)
	if end <= position {
		return member{}, false
	}
	field.value = gjson.Result{Type: rawValueType(raw[position]), Raw: raw[position:end], Index: c.source.Index + position}
	c.position = end
	return field, true
}

func (c *rawCursor) objectField(position int) (member, int, bool) {
	raw := c.source.Raw
	field := member{start: c.source.Index + position}
	end := rawStringEnd(raw, position)
	if end <= position || json.Unmarshal([]byte(raw[position:end]), &field.name) != nil {
		return member{}, 0, false
	}
	position = rawSkipSpace(raw, end)
	if position >= len(raw) || raw[position] != ':' {
		return member{}, 0, false
	}
	return field, rawSkipSpace(raw, position+1), true
}

func rawObjectMembers(parent gjson.Result) ([]member, bool) {
	cursor, ok := newRawCursor(parent, true)
	if !ok {
		return nil, false
	}
	var members []member
	seen := make(map[string]bool)
	for {
		field, exists := cursor.next()
		if !exists {
			return members, true
		}
		if seen[field.name] {
			return nil, false
		}
		seen[field.name] = true
		members = append(members, field)
	}
}

func rawSkipSpace(raw string, position int) int {
	for position < len(raw) && raw[position] <= ' ' {
		position++
	}
	return position
}

func rawStringEnd(raw string, position int) int {
	if position >= len(raw) || raw[position] != '"' {
		return -1
	}
	for position++; position < len(raw); position++ {
		index := strings.IndexByte(raw[position:], '"')
		if index < 0 {
			return -1
		}
		position += index
		escapes := 0
		for index := position - 1; index >= 0 && raw[index] == '\\'; index-- {
			escapes++
		}
		if escapes%2 == 0 {
			return position + 1
		}
	}
	return -1
}

func rawValueEnd(raw string, position int) int {
	if position >= len(raw) {
		return -1
	}
	if raw[position] == '"' {
		return rawStringEnd(raw, position)
	}
	if raw[position] == '{' || raw[position] == '[' {
		return rawContainerEnd(raw, position)
	}
	for position < len(raw) && raw[position] > ' ' && !strings.ContainsRune(",}]", rune(raw[position])) {
		position++
	}
	return position
}

func rawContainerEnd(raw string, position int) int {
	depth := 0
	for ; position < len(raw); position++ {
		switch raw[position] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return position + 1
			}
		case '"':
			position = rawStringEnd(raw, position) - 1
			if position < 0 {
				return -1
			}
		}
	}
	return -1
}

func rawValueType(first byte) gjson.Type {
	switch first {
	case '{', '[':
		return gjson.JSON
	case '"':
		return gjson.String
	case 't':
		return gjson.True
	case 'f':
		return gjson.False
	case 'n':
		return gjson.Null
	default:
		return gjson.Number
	}
}
