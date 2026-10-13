package wsrelay

import (
	"context"
	"time"

	"github.com/codex2api/proxy"
	"github.com/gorilla/websocket"
)

func registerResponsesWebSocketControl(ctx context.Context, response *WsResponse) *proxy.ResponsesWebSocketControlLease {
	return proxy.RegisterResponsesWebSocketControl(ctx, proxy.ResponsesWebSocketControlBinding{
		ForwardInterrupt: response.forwardInterrupt,
		Send: func(payload []byte) error {
			err := response.conn.writeRequestControl(response.pendingReq.RequestID, payload)
			if err != nil && err != proxy.ErrResponsesWebSocketControlInactive {
				response.markConnBroken()
			}
			return err
		},
	})
}

// writeRequestControl 不开启新的读租约；只给当前已提交的请求写控制帧。
func (wc *WsConnection) writeRequestControl(requestID string, payload []byte) error {
	wc.writeMu.Lock()
	defer wc.writeMu.Unlock()
	state := wc.ensureReadState()
	state.mu.Lock()
	active := !state.readerStopped && state.activeLease == requestID && state.leasePhase == readLeaseCommitted
	state.mu.Unlock()
	if !active {
		return proxy.ErrResponsesWebSocketControlInactive
	}
	if !wc.IsConnected() || wc.conn == nil {
		return proxy.ErrResponsesWebSocketControlInactive
	}
	if err := wc.conn.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
		return err
	}
	defer wc.conn.SetWriteDeadline(time.Time{})
	if err := wc.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return wc.preservePeerCloseCause(err)
	}
	wc.Touch()
	return nil
}
