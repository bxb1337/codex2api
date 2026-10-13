package proxy

import (
	"bytes"
	"encoding/json"

	"github.com/codex2api/api"
	"github.com/tidwall/gjson"
)

func validateResponsesWSCreateEnvelope(payload []byte) *api.APIError {
	if !uniqueResponsesWSFields(payload) {
		return responsesWSInterruptError("WebSocket create must be an object with unique fields", "type")
	}
	if gjson.GetBytes(payload, "stream_id").Exists() {
		return responsesWSInterruptError("stream_id is not supported by the Codex CLI WebSocket protocol", "stream_id")
	}
	generate := gjson.GetBytes(payload, "generate")
	if generate.Exists() && generate.Type != gjson.True && generate.Type != gjson.False {
		return responsesWSInterruptError("generate must be a boolean", "generate")
	}
	return nil
}

func uniqueResponsesWSFields(payload []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return false
	}
	fields := make(map[string]struct{})
	for decoder.More() {
		key, err := decoder.Token()
		name, valid := key.(string)
		if err != nil || !valid {
			return false
		}
		if _, exists := fields[name]; exists {
			return false
		}
		fields[name] = struct{}{}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	end, err := decoder.Token()
	return err == nil && end == json.Delim('}')
}
