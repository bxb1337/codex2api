package proxy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codex2api/api"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesWSLane struct {
	mux       *responsesWSMultiplexer
	streamID  string
	messages  chan responsesWSInboundMessage
	writeMu   sync.Mutex
	preempted atomic.Bool
	steering  *responsesWSSteering
	sequence  int64
}

func (lane *responsesWSLane) run() {
	for {
		select {
		case message := <-lane.messages:
			if !lane.execute(message, nil) {
				return
			}
			continue
		default:
		}
		if payload, applied := lane.steering.next(); payload != nil {
			if !lane.execute(responsesWSInboundMessage{payload: payload}, applied) {
				return
			}
			continue
		}
		select {
		case <-lane.mux.ctx.Done():
			return
		case message := <-lane.messages:
			if !lane.execute(message, nil) {
				return
			}
		}
	}
}

func (lane *responsesWSLane) execute(message responsesWSInboundMessage, applied []*responsesWSSteer) bool {
	defer message.releaseQueueBudget()
	select {
	case lane.mux.slots <- struct{}{}:
	case <-lane.mux.ctx.Done():
		return false
	}
	defer func() { <-lane.mux.slots }()
	lane.preempted.Store(false)
	message.releaseQueueBudget()
	payload, eventID := stripNewAPIPolicyWebSocketEventID(message.payload)
	payload = lane.steering.begin(payload, applied)
	defer lane.steering.finish()
	return lane.forward(payload, eventID)
}

func (lane *responsesWSLane) forward(payload []byte, eventID string) bool {
	c := lane.mux.base.Copy()
	ctx, cancel := context.WithCancel(context.WithValue(lane.mux.ctx, responsesWSLaneKey{}, lane))
	defer cancel()
	c.Request = c.Request.Clone(ctx)
	c.Set(contextScopeBudgetGate, nil)
	c.Set("raw_body", payload)
	interactive := responsesWSInteractive(c)
	if eventID == "" {
		eventID = "responses:" + NewUpstreamSessionUUID()
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
	lane.steering.observe(payload)
	if kind := gjson.GetBytes(payload, "type").String(); (kind == "error" || kind == "response.failed") && !lane.steering.deliverFailure() {
		return nil
	}
	err := lane.writeRaw(payload, timeout)
	if err == nil {
		lane.steering.delivered(payload)
	}
	return err
}

func (lane *responsesWSLane) writeRaw(payload []byte, timeout time.Duration) error {
	lane.writeMu.Lock()
	defer lane.writeMu.Unlock()
	if lane.streamID != "" || gjson.GetBytes(payload, "sequence_number").Exists() {
		payload, _ = sjson.SetBytes(payload, "sequence_number", lane.sequence)
		lane.sequence++
	}
	return writeResponsesWSMessageWithin(lane.mux.conn, responsesWSStreamFrame(payload, lane.streamID), timeout)
}

func (lane *responsesWSLane) drain() {
	defer lane.steering.close()
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
