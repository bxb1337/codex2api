package proxy

import (
	"bytes"
	"strings"
	"testing"
)

func TestResponsesReadOnlyChecksPreserveSharedInput(t *testing.T) {
	const historyBytes = 4 * 1024 * 1024
	body := []byte(`{"input":[{"role":"developer","content":"<multi_agent_mode>Proactive multi-agent delegation is active"},{"type":"function_call","name":"tool"},{"type":"compaction","encrypted_content":"owned"},{"type":"additional_tools","tools":[{"type":"function","name":"tool"}]},{"role":"user","content":"` + strings.Repeat("x", historyBytes) + `"}]}`)
	snapshot := bytes.Clone(body)
	_ = normalizeCompactionTriggerFinal(body, false)
	_ = requestBodyCompactionMeta(body)
	_ = ValidateResponsesFunctionNames(body)
	_ = requestCompactionEncryptedContents(body)
	_ = stripResponsesInputImageTools(body)
	_ = detectUltraModeFromBody(body)
	if !bytes.Equal(snapshot, body) {
		t.Fatal("read-only checks changed the shared request")
	}
}

func TestCompactionEncryptedContentsOwnReturnedFields(t *testing.T) {
	for _, raw := range []string{
		`{"input":[{"type":"compaction","encrypted_content":"  owned-secret  "}]}`,
		`{"input":{"type":"context_compaction","encrypted_content":"owned\u002dsecret"}}`,
	} {
		body := []byte(raw)
		contents := requestCompactionEncryptedContents(body)
		if len(contents) != 1 {
			t.Fatal("missing owned encrypted field")
		}
		clear(body)
		if expected := "owned-secret"; expected != contents[0] {
			t.Fatal("returned field still references the mutable request")
		}
	}
}
