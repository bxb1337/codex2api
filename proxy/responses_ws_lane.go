package proxy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codex2api/api"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type responsesWSLane struct {
	mux       *responsesWSMultiplexer
	streamID  string
	messages  chan responsesWSInboundMessage
	writeMu   sync.Mutex
	preempted atomic.Bool
}

func (lane *responsesWSLane) run() {
	for {
		select {
		case <-lane.mux.ctx.Done():
			return
		case message := <-lane.messages:
			if !lane.execute(message) {
				return
			}
		}
	}
}

func (lane *responsesWSLane) execute(message responsesWSInboundMessage) bool {
	defer message.releaseQueueBudget()
	select {
	case lane.mux.slots <- struct{}{}:
	case <-lane.mux.ctx.Done():
		return false
	}
	defer func() { <-lane.mux.slots }()
	lane.preempted.Store(false)
	message.releaseQueueBudget()
	c := lane.mux.base.Copy()
	ctx, cancel := context.WithCancel(context.WithValue(lane.mux.ctx, responsesWSLaneKey{}, lane))
	defer cancel()
	c.Request = c.Request.Clone(ctx)
	c.Set(contextScopeBudgetGate, nil)
	payload, eventID := stripNewAPIPolicyWebSocketEventID(message.payload)
	c.Set("raw_body", payload)
	interactive := responsesWSInteractive(c)
	if eventID == "" {
		eventID = fmt.Sprintf("responses:%d", message.turn)
	}
	err := lane.mux.handler.forwardResponsesWebSocketTurn(c, lane.mux.conn, payload, eventID, nil)
	var closeErr *responsesWSCloseError
	if !interactive && lane.streamID == "" && lane.mux.named.Load() == 0 && errors.As(err, &closeErr) {
		closeResponsesWS(lane.mux.conn, closeErr.code, closeErr.reason)
		lane.mux.cancel()
		_ = lane.mux.conn.Close()
		return false
	}
	if responsesWSConnectionFatal(err) || errors.Is(err, errResponsesWSClientGone) && !lane.preempted.Load() {
		lane.mux.cancel()
		_ = lane.mux.conn.Close()
		return false
	}
	return lane.mux.ctx.Err() == nil
}

func (lane *responsesWSLane) write(payload []byte, timeout time.Duration) error {
	lane.writeMu.Lock()
	defer lane.writeMu.Unlock()
	return writeResponsesWSMessageWithin(lane.mux.conn, responsesWSStreamFrame(payload, lane.streamID), timeout)
}

func (lane *responsesWSLane) drain() {
	for {
		select {
		case message := <-lane.messages:
			message.releaseQueueBudget()
		default:
			return
		}
	}
}

func notifyResponsesWSPreempted(c *gin.Context, conn *websocket.Conn) {
	lane := responsesWSLaneForContext(c)
	if lane != nil && (lane.streamID != "" || lane.mux.named.Load() > 0) {
		lane.notifyPreempted()
		return
	}
	closeResponsesWS(conn, websocket.CloseTryAgainLater, responsesWSSessionPreemptedCloseReason)
}

func (lane *responsesWSLane) notifyPreempted() {
	lane.preempted.Store(true)
	lane.mux.writeError(lane.streamID, api.NewAPIError(api.ErrCodeServiceUnavailable, responsesWSSessionPreemptedCloseReason, api.ErrorTypeServer))
}
