package jsonbody

import "unsafe"

// ReadOnlyText 借用正文供同步解析，不复制大缓冲。使用期间正文必须保持只读；
// 要长期保存解析出来的字符串，必须先复制所需部分。
func ReadOnlyText(body []byte) string {
	return unsafe.String(unsafe.SliceData(body), len(body))
}
