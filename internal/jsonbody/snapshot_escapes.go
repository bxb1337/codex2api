package jsonbody

import "bytes"

const snapshotUnicodeEscapeBytes = 6

// snapshotStableEscapes 仅放行不增加标准库出站长度的 HTML 转义。
// 非 ASCII、控制字符、代理项仍回退，保留完整编码的缩短与修复行为。
func snapshotStableEscapes(body []byte) bool {
	for position := 0; position < len(body); {
		index := bytes.Index(body[position:], []byte(`\u`))
		if index < 0 {
			return true
		}
		index += position
		position = index + 2
		if snapshotEscapedBackslash(body, index) {
			continue
		}
		end := index + snapshotUnicodeEscapeBytes
		if end > len(body) || !snapshotHTMLEscape(body[index+2:end]) {
			return false
		}
		position = end
	}
	return true
}

func snapshotEscapedBackslash(body []byte, index int) bool {
	count := 0
	for index--; index >= 0 && body[index] == '\\'; index-- {
		count++
	}
	return count%2 != 0
}

func snapshotHTMLEscape(hex []byte) bool {
	if hex[0] != '0' || hex[1] != '0' {
		return false
	}
	if hex[2] == '2' {
		return hex[3] == '6'
	}
	if hex[2] != '3' {
		return false
	}
	switch hex[3] {
	case 'c', 'C', 'e', 'E':
		return true
	default:
		return false
	}
}
