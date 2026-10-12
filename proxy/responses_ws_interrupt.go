package proxy

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/codex2api/api"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const responsesWSControlIDMaxLength = 256

func normalizeResponsesWSInterrupt(payload []byte) (string, *api.APIError) {
	if !gjson.ValidBytes(payload) || !gjson.ParseBytes(payload).IsObject() {
		return "", responsesWSInterruptError("Invalid response.interrupt payload", "type")
	}
	root := gjson.ParseBytes(payload)
	if eventType := root.Get("type"); eventType.Type != gjson.String || eventType.String() != "response.interrupt" {
		return "", responsesWSInterruptError("Invalid response.interrupt type", "type")
	}
	responseID := root.Get("response_id")
	if !validResponsesWSControlID(responseID) {
		return "", responsesWSInterruptError("response_id is required in response.interrupt", "response_id")
	}
	if mode := root.Get("mode"); mode.Exists() && (mode.Type != gjson.String || mode.String() != "discard_partial_items") {
		return "", responsesWSInterruptError("response.interrupt mode must be discard_partial_items", "mode")
	}
	var fieldError *api.APIError
	root.ForEach(func(key, value gjson.Result) bool {
		fieldError = validateResponsesWSInterruptField(key, value)
		return fieldError == nil
	})
	return responseID.String(), fieldError
}

func validResponsesWSControlID(value gjson.Result) bool {
	return value.Type == gjson.String && strings.TrimSpace(value.String()) != "" && len(value.String()) <= responsesWSControlIDMaxLength
}

func validateResponsesWSInterruptField(key, value gjson.Result) *api.APIError {
	switch key.String() {
	case "type", "response_id", "mode":
	case "event_id":
		if value.Type != gjson.String || len(value.String()) > responsesWSControlIDMaxLength {
			return responsesWSInterruptError("Invalid response.interrupt event_id", "event_id")
		}
	default:
		return responsesWSInterruptError("Unsupported response.interrupt field", key.String())
	}
	return nil
}

func responsesWSInterruptError(message, field string) *api.APIError {
	return api.NewAPIErrorWithDetails(api.ErrCodeInvalidParameter, message, api.ErrorTypeInvalidRequest, api.ErrorDetail{Field: field, Message: message})
}

func responsesWSInactiveResponseError() *api.APIError {
	return responsesWSInterruptError("response_id does not identify an active response on this connection", "response_id")
}

// 重建已校验的控制帧，避免重复 JSON 字段在网关和上游之间产生不同解释。
func encodeResponsesWSInterrupt(payload []byte, responseID string) ([]byte, error) {
	return json.Marshal(struct {
		Type       string `json:"type"`
		ResponseID string `json:"response_id"`
		Mode       string `json:"mode,omitempty"`
		EventID    string `json:"event_id,omitempty"`
	}{
		Type: "response.interrupt", ResponseID: responseID,
		Mode: gjson.GetBytes(payload, "mode").String(), EventID: gjson.GetBytes(payload, "event_id").String(),
	})
}

func (s *responsesWSControlSession) handle(ctx context.Context, message responsesWSInboundMessage) {
	defer message.releaseQueueBudget()
	if ctx.Err() != nil {
		return
	}
	payload, _ := stripNewAPIPolicyWebSocketEventID(message.payload)
	if gjson.GetBytes(payload, "type").String() == "response.steer" {
		s.handleSteer(payload)
		return
	}
	responseID, apiErr := normalizeResponsesWSInterrupt(payload)
	if apiErr == nil {
		encoded, err := encodeResponsesWSInterrupt(payload, responseID)
		if err != nil {
			apiErr = api.NewAPIError(api.ErrCodeServerError, "Unable to encode response.interrupt", api.ErrorTypeServer)
		} else {
			apiErr = s.interrupt(encoded, responseID)
		}
	}
	if apiErr == nil || ctx.Err() != nil {
		return
	}
	if err := s.writeInterruptError(message.payload, apiErr); err != nil {
		_ = s.conn.Close()
	}
}

func (s *responsesWSControlSession) writeInterruptError(request []byte, apiErr *api.APIError) error {
	lane := s.responseLane(gjson.GetBytes(request, "response_id").String())
	if lane == nil {
		return writeResponsesWSInterruptError(s.conn, request, apiErr)
	}
	payload, err := encodeResponsesWSError(apiErr)
	if err != nil {
		return err
	}
	if eventID := gjson.GetBytes(request, "event_id").String(); len(eventID) <= responsesWSControlIDMaxLength && eventID != "" {
		payload, _ = sjson.SetBytes(payload, "event_id", eventID)
	}
	return lane.writeRaw(payload, responsesWSWriteTimeout)
}

func writeResponsesWSInterruptError(conn *websocket.Conn, request []byte, apiErr *api.APIError) error {
	eventID := gjson.GetBytes(request, "event_id").String()
	if len(eventID) > responsesWSControlIDMaxLength {
		eventID = ""
	}
	payload, err := json.Marshal(struct {
		Type    string        `json:"type"`
		Status  int           `json:"status"`
		Error   *api.APIError `json:"error"`
		EventID string        `json:"event_id,omitempty"`
	}{Type: "error", Status: responsesWSErrorStatus(apiErr), Error: apiErr, EventID: eventID})
	if err != nil {
		return err
	}
	return writeResponsesWSMessage(conn, payload)
}
