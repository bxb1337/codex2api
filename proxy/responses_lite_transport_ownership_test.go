package proxy

import (
	"bytes"
	"net/http"
	"reflect"
	"testing"

	"github.com/codex2api/internal/jsonbody"
)

func TestResponsesLiteTransportPreservesSharedBodyAndHeaders(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"snapshot"}],"reasoning":{"effort":"high"},"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"false"}}`)
	original := bytes.Clone(body)
	input := jsonbody.New(body).RawBytes("input")
	snapshot := bytes.Clone(input)
	headers := make(http.Header)
	headers.Set(codexResponsesLiteHeader, "true")
	headers.Set("X-Client-Request-Id", "original")
	originalHeaders := headers.Clone()
	for _, websocket := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			transport := responsesLiteTransport{headers: headers, websocket: websocket, enabled: enabled}
			_, _ = prepareCodexResponsesLiteTransport(body, transport)
			_, _ = prepareCodexResponsesLiteRequest(body, transport)
		}
	}
	if !bytes.Equal(original, body) || !bytes.Equal(snapshot, input) {
		t.Fatal("Lite preparation changed shared body or input snapshot")
	}
	if !reflect.DeepEqual(originalHeaders, headers) {
		t.Fatal("Lite preparation changed the shared request headers")
	}
}
