package proxy

import (
	"bytes"
	"strings"
	"testing"
)

func TestResponsesInputSnapshotSurvivesBodyRewrite(t *testing.T) {
	const historyBytes = 4 * 1024 * 1024
	body := []byte(`{"model":"gpt-5.5","input":[{"role":"user","content":"` + strings.Repeat("x", historyBytes) + `"}],"store":false}`)
	original := bytes.Clone(body)
	prepared := prepareResponsesBodyForOwnerDetailed(body, "")
	snapshot := bytes.Clone(prepared.ExpandedInput)
	_ = normalizeCodexResponsesLiteBody(prepared.Body, false)
	_ = prepareCompactResponsesBodyForOwnerDetailed(prepared.Body, "")
	if !bytes.Equal(original, body) || !bytes.Equal(snapshot, prepared.ExpandedInput) {
		t.Fatal("body preparation changed shared request or replay snapshot")
	}
}

func TestFailedResponsesPreparationDoesNotRetainInputView(t *testing.T) {
	for _, raw := range []string{
		`{"input":[{"role":"user","content":"original"}]`,
		`{"input":[{"role":"user","content":"original"}],"number":1e1000}`,
	} {
		body := []byte(raw)
		snapshot := bytes.Clone(body)
		prepared := prepareResponsesBodyForOwnerDetailed(body, "")
		compact := prepareCompactResponsesBodyForOwnerDetailed(body, "")
		if prepared.ExpandedInput != nil || compact.ExpandedInput != nil {
			t.Fatal("failed preparation retained an unvalidated shared input view")
		}
		if !bytes.Equal(snapshot, body) {
			t.Fatal("failed preparation changed the shared request")
		}
	}
}
