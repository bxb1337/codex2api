package proxy

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/codex2api/api"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

const responsesWSRecentResponseCapacity = 64

var ErrResponsesWebSocketControlInactive = errors.New("responses websocket request is no longer active")

type responsesWSControlSessionKey struct{}
type responsesWSControlTurnKey struct{}

// ResponsesWebSocketControlBinding 只绑定本轮已经发送 create 的上游连接。
type ResponsesWebSocketControlBinding struct {
	Send             func([]byte) error
	ForwardInterrupt bool
}

// ResponsesWebSocketControlLease 隔离重试轮次的观察与清理回调。
type ResponsesWebSocketControlLease struct {
	turn       *responsesWSControlTurn
	generation uint64
}

type responsesWSControlSession struct {
	mu       sync.Mutex
	conn     *websocket.Conn
	active   *responsesWSControlTurn
	recent   map[string]struct{}
	order    []string
	next     int
	messages chan responsesWSInboundMessage
	done     chan struct{}
}

type responsesWSControlTurn struct {
	mu                sync.Mutex
	session           *responsesWSControlSession
	binding           ResponsesWebSocketControlBinding
	generation        uint64
	transportObserves bool
	responseID        string
	ended             bool
	interrupted       bool
}

func newResponsesWSControlSession(conn *websocket.Conn) *responsesWSControlSession {
	return &responsesWSControlSession{
		conn: conn, recent: make(map[string]struct{}),
		messages: make(chan responsesWSInboundMessage, responsesWSInboundQueueCapacity),
		done:     make(chan struct{}),
	}
}

func (s *responsesWSControlSession) start(ctx context.Context) {
	go func() {
		defer close(s.done)
		for {
			select {
			case <-ctx.Done():
				return
			case message := <-s.messages:
				s.handle(ctx, message)
			}
		}
	}()
}

func (s *responsesWSControlSession) drain() {
	for {
		select {
		case message := <-s.messages:
			message.releaseQueueBudget()
		default:
			return
		}
	}
}

func interceptResponsesWSControl(ctx context.Context, message responsesWSInboundMessage) bool {
	s, _ := ctx.Value(responsesWSControlSessionKey{}).(*responsesWSControlSession)
	if s == nil || gjson.GetBytes(message.payload, "type").String() != "response.interrupt" {
		return false
	}
	select {
	case <-ctx.Done():
		message.releaseQueueBudget()
	case s.messages <- message:
	default:
		message.releaseQueueBudget()
		closeResponsesWSWithin(s.conn, websocket.CloseTryAgainLater, "too many websocket control messages", responsesWSOverloadWriteTimeout)
		_ = s.conn.Close()
	}
	return true
}

func beginResponsesWSControlTurn(c *gin.Context) func() {
	s, _ := c.Request.Context().Value(responsesWSControlSessionKey{}).(*responsesWSControlSession)
	if s == nil {
		return func() {}
	}
	turn := &responsesWSControlTurn{session: s}
	s.mu.Lock()
	s.active = turn
	s.mu.Unlock()
	request := c.Request
	c.Request = request.WithContext(context.WithValue(request.Context(), responsesWSControlTurnKey{}, turn))
	return func() {
		turn.mu.Lock()
		turn.ended, turn.binding = true, ResponsesWebSocketControlBinding{}
		turn.mu.Unlock()
		s.mu.Lock()
		if s.active == turn {
			s.active = nil
		}
		s.mu.Unlock()
		c.Request = request
	}
}

// RegisterResponsesWebSocketControl 给传输适配器安装本轮控制帧发送器。
func RegisterResponsesWebSocketControl(ctx context.Context, binding ResponsesWebSocketControlBinding) *ResponsesWebSocketControlLease {
	turn, _ := ctx.Value(responsesWSControlTurnKey{}).(*responsesWSControlTurn)
	if turn == nil {
		return nil
	}
	turn.mu.Lock()
	turn.generation++
	generation := turn.generation
	turn.binding, turn.responseID = binding, ""
	turn.transportObserves = true
	turn.ended, turn.interrupted = false, false
	turn.mu.Unlock()
	return &ResponsesWebSocketControlLease{turn: turn, generation: generation}
}

func (lease *ResponsesWebSocketControlLease) Close() {
	if lease == nil {
		return
	}
	turn := lease.turn
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if turn.generation == lease.generation {
		turn.binding = ResponsesWebSocketControlBinding{}
	}
}

func (lease *ResponsesWebSocketControlLease) Observe(frame []byte) {
	if lease != nil {
		lease.turn.observe(frame, lease.generation)
	}
}

// CodexResponsesLiteEnabled 读取最终出站正文里的 Lite 信号。
func CodexResponsesLiteEnabled(body []byte) bool {
	return codexResponsesLiteRequested(body, nil)
}

// ObserveResponsesWebSocketControl 在向下游交付前更新响应归属，避免迟到中断串轮。
func ObserveResponsesWebSocketControl(ctx context.Context, frame []byte) {
	turn, _ := ctx.Value(responsesWSControlTurnKey{}).(*responsesWSControlTurn)
	if turn == nil {
		return
	}
	turn.observe(frame, 0)
}

func (turn *responsesWSControlTurn) observe(frame []byte, generation uint64) {
	eventType := gjson.GetBytes(frame, "type").String()
	responseID := strings.TrimSpace(gjson.GetBytes(frame, "response.id").String())
	if len(responseID) > responsesWSControlIDMaxLength {
		responseID = ""
	}
	turn.mu.Lock()
	if !turn.observesGeneration(generation) {
		turn.mu.Unlock()
		return
	}
	if eventType == "response.created" {
		turn.responseID = strings.Clone(responseID)
		turn.ended, turn.interrupted = false, false
	}
	terminal := isResponsesTerminalEvent(eventType) || eventType == "error"
	if terminal && (responseID == "" || responseID == turn.responseID) {
		responseID = turn.responseID
		turn.ended = true
	}
	turn.mu.Unlock()
	if terminal {
		turn.session.remember(responseID)
	}
}

func (turn *responsesWSControlTurn) observesGeneration(generation uint64) bool {
	if generation == 0 {
		return !turn.transportObserves
	}
	return generation == turn.generation
}

func prepareResponsesWSControlAttempt(ctx context.Context) {
	turn, _ := ctx.Value(responsesWSControlTurnKey{}).(*responsesWSControlTurn)
	if turn == nil {
		return
	}
	turn.mu.Lock()
	defer turn.mu.Unlock()
	turn.generation++
	turn.binding = ResponsesWebSocketControlBinding{}
	turn.responseID = ""
	turn.transportObserves, turn.ended, turn.interrupted = false, false, false
}

func (s *responsesWSControlSession) remember(responseID string) {
	if responseID == "" || len(responseID) > responsesWSControlIDMaxLength {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.recent[responseID]; exists {
		return
	}
	responseID = strings.Clone(responseID)
	if len(s.order) < responsesWSRecentResponseCapacity {
		s.order = append(s.order, responseID)
	} else {
		delete(s.recent, s.order[s.next])
		s.order[s.next] = responseID
		s.next = (s.next + 1) % responsesWSRecentResponseCapacity
	}
	s.recent[responseID] = struct{}{}
}

func (s *responsesWSControlSession) interrupt(payload []byte, responseID string) *api.APIError {
	s.mu.Lock()
	_, completed := s.recent[responseID]
	turn := s.active
	s.mu.Unlock()
	if completed {
		return nil
	}
	if turn == nil {
		return responsesWSInactiveResponseError()
	}
	return turn.interrupt(payload, responseID)
}

func (turn *responsesWSControlTurn) interrupt(payload []byte, responseID string) *api.APIError {
	turn.mu.Lock()
	defer turn.mu.Unlock()
	if responseID != turn.responseID {
		return responsesWSInactiveResponseError()
	}
	if turn.ended || turn.interrupted {
		return nil
	}
	// 普通 Responses（含 Lite 被模型映射/能力门禁剥离的情况）沿用 Codex 的 drain 流程。
	if turn.binding.ForwardInterrupt && turn.binding.Send != nil {
		if err := turn.binding.Send(payload); err != nil && !errors.Is(err, ErrResponsesWebSocketControlInactive) {
			return api.NewAPIError(api.ErrCodeUpstreamError, "Unable to send response.interrupt to the active upstream websocket", api.ErrorTypeUpstream)
		}
	}
	turn.interrupted = true
	return nil
}
