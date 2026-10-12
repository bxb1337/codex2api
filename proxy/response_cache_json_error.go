package proxy

import (
	stdjson "encoding/json"
	"errors"

	json "github.com/goccy/go-json"
)

// 缓存后端接口也可能返回标准库错误，统一识别为损坏的 JSON。
func isResponseCacheJSONError(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var stdSyntaxErr *stdjson.SyntaxError
	var stdTypeErr *stdjson.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr) ||
		errors.As(err, &stdSyntaxErr) || errors.As(err, &stdTypeErr)
}
