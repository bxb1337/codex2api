package proxy

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var responsesWSWriteLocks sync.Map

func registerResponsesWSWriter(conn *websocket.Conn) func() {
	responsesWSWriteLocks.Store(conn, &sync.Mutex{})
	return func() { responsesWSWriteLocks.Delete(conn) }
}

func writeResponsesWSMessageWithin(conn *websocket.Conn, payload []byte, timeout time.Duration) error {
	if conn == nil {
		return errResponsesWSClientGone
	}
	if lock, ok := responsesWSWriteLocks.Load(conn); ok {
		writer := lock.(*sync.Mutex)
		writer.Lock()
		defer writer.Unlock()
	}
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}
