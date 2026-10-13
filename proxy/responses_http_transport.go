package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func prepareResponsesHTTPBody(body []byte) []byte {
	for _, field := range []string{"type", "generate", "stream_id"} {
		body, _ = sjson.DeleteBytes(body, field)
	}
	return body
}

// HTTP 没有预热协议，返回可由网关快照恢复的本地响应，避免误发生成。
func responsesHTTPPrewarm(ctx context.Context, body []byte) (*http.Response, error) {
	if gjson.GetBytes(body, "generate").Type != gjson.False {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	response := map[string]any{
		"id":     "resp_" + strings.ReplaceAll(NewUpstreamSessionUUID(), "-", ""),
		"object": "response", "created_at": time.Now().Unix(),
		"status": "completed", "model": gjson.GetBytes(body, "model").String(),
		"output": []any{}, "error": nil, "incomplete_details": nil,
		"usage": map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	if gjson.GetBytes(body, "stream").Bool() {
		encoded = responsesPrewarmSSE(response)
		header.Set("Content-Type", "text/event-stream")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: header,
		Body: io.NopCloser(bytes.NewReader(encoded)), ContentLength: int64(len(encoded))}, nil
}

func responsesPrewarmSSE(response map[string]any) []byte {
	var stream bytes.Buffer
	for sequence, event := range []string{"response.created", "response.completed"} {
		frame, _ := json.Marshal(map[string]any{"type": event, "sequence_number": sequence, "response": response})
		fmt.Fprintf(&stream, "data: %s\n\n", frame)
	}
	return stream.Bytes()
}

func responsesWSRequestIsPrewarm(c *gin.Context) bool {
	raw, _ := c.Get("raw_body")
	body, _ := raw.([]byte)
	return gjson.GetBytes(body, "generate").Type == gjson.False
}
