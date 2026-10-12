package proxy

import (
	"context"

	"github.com/gorilla/websocket"
)

func (conn *openAIResponsesWSConn) write(ctx context.Context, payload []byte) error {
	conn.writeMu.Lock()
	defer conn.writeMu.Unlock()
	return writeOpenAIResponsesWS(ctx, conn.conn, payload)
}

func registerOpenAIResponsesWSControl(ctx context.Context, pool *openAIResponsesWSPool, conn *openAIResponsesWSConn) *ResponsesWebSocketControlLease {
	binding := openAIResponsesWSControlBinding{ctx: ctx, pool: pool, conn: conn, socket: conn.conn}
	return RegisterResponsesWebSocketControl(ctx, ResponsesWebSocketControlBinding{
		ForwardInterrupt: conn.forwardInterrupt,
		Send:             binding.write,
	})
}

type openAIResponsesWSControlBinding struct {
	ctx    context.Context
	pool   *openAIResponsesWSPool
	conn   *openAIResponsesWSConn
	socket *websocket.Conn
}

func (binding openAIResponsesWSControlBinding) write(payload []byte) error {
	conn, pool := binding.conn, binding.pool
	conn.writeMu.Lock()
	defer conn.writeMu.Unlock()
	pool.mu.Lock()
	active := conn.busy && !conn.dead && conn.conn == binding.socket
	pool.mu.Unlock()
	if !active || binding.ctx.Err() != nil {
		return ErrResponsesWebSocketControlInactive
	}
	if err := writeOpenAIResponsesWS(binding.ctx, binding.socket, payload); err != nil {
		pool.retire(conn)
		return err
	}
	return nil
}
