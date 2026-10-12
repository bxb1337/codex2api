package proxy

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"
	"unsafe"

	json "github.com/goccy/go-json"
)

const (
	requestCacheSize     = 256
	requestCacheMaxBytes = 16 << 20
	requestCacheIdleTTL  = 5 * time.Minute
)

type requestCacheEntry struct {
	key      [sha256.Size]byte
	req      openAIRequest
	bytes    int
	lastUsed time.Time
}

type requestCache struct {
	mu    sync.Mutex
	order *list.List
	items map[[sha256.Size]byte]*list.Element
	bytes int
}

var globalRequestCache = &requestCache{
	order: list.New(), items: make(map[[sha256.Size]byte]*list.Element, requestCacheSize),
}

func (c *requestCache) get(key [sha256.Size]byte) (openAIRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.removeIdle(now)
	element, ok := c.items[key]
	if !ok {
		return openAIRequest{}, false
	}
	entry := element.Value.(*requestCacheEntry)
	entry.lastUsed = now
	c.order.MoveToFront(element)
	return entry.req, true
}

func (c *requestCache) put(key [sha256.Size]byte, req openAIRequest) {
	bytes := requestCacheOwnedBytes(req)
	if bytes > requestCacheMaxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.removeIdle(now)
	if element, ok := c.items[key]; ok {
		c.remove(element)
	}
	entry := &requestCacheEntry{key: key, req: req, bytes: bytes, lastUsed: now}
	c.items[key] = c.order.PushFront(entry)
	c.bytes += bytes
	for c.order.Len() > requestCacheSize || c.bytes > requestCacheMaxBytes {
		c.remove(c.order.Back())
	}
}

func (c *requestCache) removeIdle(now time.Time) {
	for tail := c.order.Back(); tail != nil; tail = c.order.Back() {
		if now.Sub(tail.Value.(*requestCacheEntry).lastUsed) < requestCacheIdleTTL {
			return
		}
		c.remove(tail)
	}
}

func (c *requestCache) remove(element *list.Element) {
	entry := element.Value.(*requestCacheEntry)
	delete(c.items, entry.key)
	c.bytes -= entry.bytes
	c.order.Remove(element)
}

// 核算缓存拥有的载荷和切片容量，不把原始正文长度当作解析结果的大小。
func requestCacheOwnedBytes(req openAIRequest) int {
	bytes := int(unsafe.Sizeof(requestCacheEntry{}) + unsafe.Sizeof(list.Element{}))
	bytes += len(req.Model) + len(req.ReasoningEffort) + len(req.ServiceTier) + len(req.ServiceTierAlt)
	bytes += cap(req.Messages) * int(unsafe.Sizeof(openAIMessage{}))
	bytes += cap(req.Tools) * int(unsafe.Sizeof(json.RawMessage{}))
	for _, raw := range req.Tools {
		bytes += cap(raw)
	}
	for _, raw := range []json.RawMessage{req.ResponseFormat, req.Temperature, req.TopP,
		req.MaxTokens, req.MaxCompletionTokens, req.Stop, req.Seed, req.PresencePenalty,
		req.FrequencyPenalty, req.ParallelToolCalls, req.ToolChoice} {
		bytes += cap(raw)
	}
	for _, message := range req.Messages {
		bytes += cap(message.Content) + len(message.Role) + len(message.ToolCallID)
		bytes += cap(message.ToolCalls) * int(unsafe.Sizeof(openAIToolCall{}))
		for _, call := range message.ToolCalls {
			bytes += len(call.Type) + len(call.ID) + len(call.Namespace) + len(call.Function.Name) + len(call.Function.Arguments)
			if call.Custom != nil {
				bytes += int(unsafe.Sizeof(customToolCallData{})) + len(call.Custom.Name) + len(call.Custom.Input)
			}
		}
	}
	return bytes
}

// cachedOrParse 的全局缓存受字节预算约束；调用者应在请求内复用解析结果。
func cachedOrParse(rawJSON []byte) openAIRequest {
	if len(rawJSON) == 0 {
		return openAIRequest{}
	}
	key := sha256.Sum256(rawJSON)
	if req, ok := globalRequestCache.get(key); ok {
		return req
	}
	var req openAIRequest
	_ = json.Unmarshal(rawJSON, &req)
	globalRequestCache.put(key, req)
	return req
}
