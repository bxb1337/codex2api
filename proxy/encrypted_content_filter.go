package proxy

import (
	json "github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

func filterRememberedEncryptedItems(input gjson.Result, invalid map[encryptedDigest]struct{}) ([]json.RawMessage, bool) {
	var rejectedIndexes []int
	count := 0
	input.ForEach(func(_, item gjson.Result) bool {
		digest, encrypted := encryptedItemDigest(item)
		if _, rejected := invalid[digest]; encrypted && rejected {
			rejectedIndexes = append(rejectedIndexes, count)
		}
		count++
		return true
	})
	if len(rejectedIndexes) == 0 {
		return nil, false
	}
	// 仅在命中拒绝项时构建列表；第二遍扫描复用索引，不重复计算密文摘要。
	items := make([]json.RawMessage, 0, count-len(rejectedIndexes))
	index, rejected := 0, 0
	input.ForEach(func(_, item gjson.Result) bool {
		if rejected < len(rejectedIndexes) && index == rejectedIndexes[rejected] {
			rejected++
		} else {
			items = append(items, json.RawMessage(item.Raw))
		}
		index++
		return true
	})
	return items, true
}
