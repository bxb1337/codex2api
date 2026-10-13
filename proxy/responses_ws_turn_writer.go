package proxy

import (
	"encoding/json"
	"log"
	"time"

	"github.com/codex2api/api"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/sjson"
)

type responsesWSLaneKey struct{}

func responsesWSLaneForContext(c *gin.Context) *responsesWSLane {
	lane, _ := c.Request.Context().Value(responsesWSLaneKey{}).(*responsesWSLane)
	return lane
}

func writeResponsesWSTurnMessage(c *gin.Context, conn *websocket.Conn, payload []byte) error {
	if lane := responsesWSLaneForContext(c); lane != nil {
		return lane.write(payload, responsesWSWriteTimeout)
	}
	return writeResponsesWSMessage(conn, payload)
}

func writeResponsesWSTurnError(c *gin.Context, conn *websocket.Conn, apiErr *api.APIError) error {
	return writeResponsesWSTurnErrorWithin(c, conn, responsesWSTurnError{apiErr, responsesWSWriteTimeout})
}

type responsesWSTurnError struct {
	err     *api.APIError
	timeout time.Duration
}

func writeResponsesWSTurnErrorWithin(c *gin.Context, conn *websocket.Conn, failure responsesWSTurnError) error {
	payload, err := encodeResponsesWSError(failure.err)
	if err != nil {
		return err
	}
	if lane := responsesWSLaneForContext(c); lane != nil {
		return lane.write(payload, failure.timeout)
	}
	return writeResponsesWSMessageWithin(conn, payload, failure.timeout)
}

func encodeResponsesWSError(apiErr *api.APIError) ([]byte, error) {
	return json.Marshal(struct {
		Type   string        `json:"type"`
		Status int           `json:"status"`
		Error  *api.APIError `json:"error"`
	}{"error", responsesWSErrorStatus(apiErr), apiErr})
}

func responsesWSStreamFrame(payload []byte, streamID string) []byte {
	if streamID == "" {
		return payload
	}
	result, err := sjson.SetBytes(payload, "stream_id", streamID)
	if err != nil {
		return payload
	}
	return result
}

func writeResponsesWSTurnRecoveryError(c *gin.Context, conn *websocket.Conn, apiErr *api.APIError) error {
	recordResponseCacheKnownUnavailableError()
	log.Printf("Responses WebSocket context recovery unavailable: code=%s reason=%s", apiErr.Code, responsesWSContextReason(apiErr))
	return writeResponsesWSTurnError(c, conn, apiErr)
}
