package proxy

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/codex2api/api"
	"github.com/tidwall/gjson"
)

const responsesWSPendingSteerLimit = 32

type responsesWSSteer struct {
	ID                 string          `json:"id,omitempty"`
	PreviousResponseID string          `json:"previous_response_id"`
	Input              json.RawMessage `json:"input"`
	items              []json.RawMessage
	reservation        *responsesWSRetainedMemory
	pendingNotified    bool
}

func normalizeResponsesWSSteer(payload []byte) (*responsesWSSteer, *api.APIError) {
	var body map[string]any
	if json.Unmarshal(payload, &body) != nil || body == nil {
		return nil, responsesWSSteerError("invalid_input", "Invalid response.steer payload")
	}
	for field := range body {
		if field != "type" && field != "previous_response_id" && field != "input" {
			return nil, responsesWSSteerError("invalid_input", "Unsupported response.steer field: "+field)
		}
	}
	canonical, _ := json.Marshal(body)
	root := gjson.ParseBytes(canonical)
	if root.Get("type").String() != "response.steer" || !validResponsesWSControlID(root.Get("previous_response_id")) {
		return nil, responsesWSSteerError("invalid_input", "response.steer requires a previous_response_id")
	}
	input := root.Get("input")
	if !validResponsesWSSteerInput(input) {
		return nil, responsesWSSteerError("invalid_input", "Steering accepts only non-empty user message input")
	}
	steer := &responsesWSSteer{PreviousResponseID: strings.Clone(root.Get("previous_response_id").String()), Input: bytes.Clone([]byte(input.Raw))}
	steer.items = responsesWSSteerItems(input)
	return steer, nil
}

func validResponsesWSSteerInput(input gjson.Result) bool {
	if input.Type == gjson.String {
		return strings.TrimSpace(input.String()) != ""
	}
	if !input.IsArray() || len(input.Array()) == 0 {
		return false
	}
	for _, item := range input.Array() {
		if !validResponsesWSSteerMessage(item) {
			return false
		}
	}
	return true
}

func validResponsesWSSteerMessage(item gjson.Result) bool {
	if !item.IsObject() || item.Get("role").String() != "user" {
		return false
	}
	if kind := item.Get("type"); kind.Exists() && kind.String() != "message" {
		return false
	}
	valid := true
	item.ForEach(func(key, _ gjson.Result) bool {
		valid = key.String() == "type" || key.String() == "role" || key.String() == "content"
		return valid
	})
	return valid && validResponsesWSSteerContent(item.Get("content"))
}

func validResponsesWSSteerContent(content gjson.Result) bool {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String()) != ""
	}
	if !content.IsArray() || len(content.Array()) == 0 {
		return false
	}
	for _, part := range content.Array() {
		if !validResponsesWSSteerPart(part) {
			return false
		}
	}
	return true
}

func validResponsesWSSteerPart(part gjson.Result) bool {
	if !part.IsObject() {
		return false
	}
	switch part.Get("type").String() {
	case "input_text":
		return part.Get("text").Type == gjson.String
	case "input_image":
		return part.Get("file_id").String() != "" || part.Get("image_url").String() != ""
	case "input_file":
		return part.Get("file_id").String() != "" || part.Get("file_url").String() != "" || part.Get("file_data").String() != ""
	}
	return false
}

func responsesWSSteerItems(input gjson.Result) []json.RawMessage {
	if input.IsArray() {
		var items []json.RawMessage
		_ = json.Unmarshal([]byte(input.Raw), &items)
		return items
	}
	item, _ := json.Marshal(map[string]string{"type": "message", "role": "user", "content": input.String()})
	return []json.RawMessage{item}
}

func responsesWSSteerError(code, message string) *api.APIError {
	return api.NewAPIError(api.ErrorCode(code), message, api.ErrorTypeInvalidRequest)
}

func (steer *responsesWSSteer) release() {
	if steer.reservation != nil {
		steer.reservation.Release()
		steer.reservation = nil
	}
	steer.items = nil
}
