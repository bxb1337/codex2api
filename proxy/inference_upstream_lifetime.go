package proxy

import (
	"context"
	"io"
	"net/http"
)

type inferenceResponseBody struct {
	io.ReadCloser
	lease *InferenceRequestLease
}

func (body *inferenceResponseBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if err != nil {
		body.lease.Finish()
	}
	return n, err
}

func (body *inferenceResponseBody) Close() error {
	err := body.ReadCloser.Close()
	body.lease.Finish()
	return err
}

func trackInferenceResponse(resp *http.Response, lease *InferenceRequestLease) {
	if lease == nil {
		return
	}
	if resp == nil || resp.Body == nil {
		lease.Finish()
		return
	}
	resp.Body = &inferenceResponseBody{ReadCloser: resp.Body, lease: lease}
}

func observeInferenceTerminal(ctx context.Context, eventType string) {
	if isResponsesTerminalEvent(eventType) || eventType == "error" {
		FinishInferenceRequest(ctx)
	}
}
