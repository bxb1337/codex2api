package proxy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/codex2api/api"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

const responsesWSActiveLaneLimit = 16
const responsesWSNamedLaneLimit = 32

type responsesWSMultiplexer struct {
	handler  *Handler
	base     *gin.Context
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	lanes    map[string]*responsesWSLane
	slots    chan struct{}
	workers  sync.WaitGroup
	named    atomic.Int64
	retained atomic.Int64
}

func newResponsesWSMultiplexer(h *Handler, c *gin.Context, conn *websocket.Conn) *responsesWSMultiplexer {
	ctx, cancel := context.WithCancel(c.Request.Context())
	return &responsesWSMultiplexer{handler: h, base: c.Copy(), conn: conn, ctx: ctx, cancel: cancel,
		lanes: make(map[string]*responsesWSLane), slots: make(chan struct{}, responsesWSActiveLaneLimit)}
}

func (m *responsesWSMultiplexer) close() {
	m.cancel()
	_ = m.conn.Close()
	m.workers.Wait()
	for _, lane := range m.lanes {
		lane.drain()
	}
}

func (m *responsesWSMultiplexer) serve(messages <-chan responsesWSInboundMessage) {
	for {
		select {
		case <-m.ctx.Done():
			return
		case message, ok := <-messages:
			if !ok {
				return
			}
			if message.err != nil {
				message.releaseQueueBudget()
				return
			}
			m.accept(message)
		}
	}
}

func (m *responsesWSMultiplexer) accept(message responsesWSInboundMessage) {
	if message.messageType != websocket.TextMessage && message.messageType != websocket.BinaryMessage {
		message.releaseQueueBudget()
		closeResponsesWS(m.conn, websocket.CloseUnsupportedData, "unsupported websocket message type")
		m.cancel()
		return
	}
	streamID, apiErr := responsesWSCreateStreamID(message.payload)
	if apiErr == nil {
		_, _, apiErr = normalizeResponsesWebSocketClientPayload(message.payload)
	}
	if apiErr != nil {
		message.releaseQueueBudget()
		m.writeError(streamID, apiErr)
		return
	}
	lane, apiErr := m.lane(streamID)
	if apiErr != nil {
		message.releaseQueueBudget()
		m.writeError(streamID, apiErr)
		return
	}
	select {
	case lane.messages <- message:
	case <-m.ctx.Done():
		message.releaseQueueBudget()
	default:
		message.releaseQueueBudget()
		closeResponsesWSWithin(m.conn, websocket.CloseTryAgainLater, "WebSocket stream queue is full", responsesWSOverloadWriteTimeout)
		m.cancel()
		_ = m.conn.Close()
	}
}

func (m *responsesWSMultiplexer) lane(streamID string) (*responsesWSLane, *api.APIError) {
	if lane := m.lanes[streamID]; lane != nil {
		return lane, nil
	}
	if streamID != "" && m.named.Load() >= responsesWSNamedLaneLimit {
		return nil, api.NewAPIError(api.ErrorCode("websocket_stream_limit_reached"), "WebSocket named stream limit reached", api.ErrorTypeInvalidRequest)
	}
	lane := &responsesWSLane{mux: m, streamID: streamID, messages: make(chan responsesWSInboundMessage, responsesWSInboundQueueCapacity)}
	lane.steering = &responsesWSSteering{lane: lane}
	m.lanes[streamID] = lane
	controls, _ := m.ctx.Value(responsesWSControlSessionKey{}).(*responsesWSControlSession)
	if controls != nil {
		controls.mu.Lock()
		controls.lanes[streamID] = lane
		controls.mu.Unlock()
	}
	if streamID != "" {
		m.named.Add(1)
	}
	m.workers.Add(1)
	go func() { defer m.workers.Done(); lane.run() }()
	return lane, nil
}

func (m *responsesWSMultiplexer) writeError(streamID string, apiErr *api.APIError) {
	payload, err := encodeResponsesWSError(apiErr)
	if err == nil {
		err = writeResponsesWSMessage(m.conn, responsesWSStreamFrame(payload, streamID))
	}
	if err != nil {
		m.cancel()
		_ = m.conn.Close()
	}
}

func responsesWSCreateStreamID(payload []byte) (string, *api.APIError) {
	value := gjson.GetBytes(payload, "stream_id")
	if !value.Exists() {
		return "", nil
	}
	id := value.String()
	if value.Type != gjson.String || !validResponsesWSStreamID(id) {
		return "", api.NewAPIError(api.ErrorCode("invalid_stream_id"), "stream_id must contain 1-256 ASCII letters, digits, underscores, hyphens or periods", api.ErrorTypeInvalidRequest)
	}
	return id, nil
}

func validResponsesWSStreamID(id string) bool {
	if len(id) == 0 || len(id) > responsesWSControlIDMaxLength {
		return false
	}
	for _, char := range id {
		if !validResponsesWSStreamCharacter(char) {
			return false
		}
	}
	return true
}

func validResponsesWSStreamCharacter(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.'
}

func responsesWSConnectionFatal(err error) bool {
	var apiErr *api.APIError
	return errors.As(err, &apiErr) && apiErr.Type == api.ErrorTypeAuthentication
}
