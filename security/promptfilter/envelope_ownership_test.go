package promptfilter

import (
	"strings"
	"testing"
	"unsafe"
)

func TestBoundedEnvelopeOwnsSharedReferences(t *testing.T) {
	const sourceBytes = 4 * 1024 * 1024
	source := strings.Repeat("x", sourceBytes)
	original := RequestEnvelope{
		RequestedModel: source[:8], EffectiveModel: source[8:16], Endpoint: source[16:24],
		Segments:                 []Segment{{Origin: OriginCurrentUser, Role: "user", Text: source[:64], SafetyEvidence: source[64:72]}},
		AdapterUnclassifiedTypes: []string{source[72:80]}, currentUserExactText: source[80:96],
	}
	owned := ApplyGuardPerformanceBudget(original, RecommendedAdvancedConfig().Guard.Performance, DefaultMaxTextLength)
	for _, pair := range [][2]string{
		{original.RequestedModel, owned.RequestedModel}, {original.EffectiveModel, owned.EffectiveModel},
		{original.Endpoint, owned.Endpoint}, {original.Segments[0].Text, owned.Segments[0].Text},
		{original.AdapterUnclassifiedTypes[0], owned.AdapterUnclassifiedTypes[0]},
		{original.currentUserExactText, owned.currentUserExactText},
	} {
		if pair[0] != pair[1] || unsafe.StringData(pair[0]) == unsafe.StringData(pair[1]) {
			t.Fatal("envelope changed text or retained shared backing storage")
		}
	}
	owned.Segments[0].Text = "changed"
	owned.AdapterUnclassifiedTypes[0] = "changed"
	if original.Segments[0].Text != source[:64] || original.AdapterUnclassifiedTypes[0] != source[72:80] {
		t.Fatal("owned envelope changed shared slices")
	}
}

func TestAuditMetadataPreservesSharedEnvelope(t *testing.T) {
	original := RequestEnvelope{Segments: []Segment{{Text: "original"}}, currentUserExactText: "exact", currentUserPrecheck: &currentUserPrecheck{}, precheckIncomplete: true}
	metadata := original.AuditMetadata()
	if metadata.Segments != nil || metadata.currentUserExactText != "" || metadata.currentUserPrecheck != nil || metadata.precheckIncomplete {
		t.Fatal("audit metadata retained private request references")
	}
	if original.Segments[0].Text != "original" || original.currentUserExactText != "exact" || original.currentUserPrecheck == nil || !original.precheckIncomplete {
		t.Fatal("metadata extraction changed original envelope")
	}
}

func TestBuiltEnvelopeDoesNotRetainMutableBody(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"role":"user","content":"keep this text"}]}`)
	envelope := BuildEnvelope(body, "/v1/responses", "", TransportHTTP, DefaultMaxTextLength)
	for index := range body {
		body[index] = 'x'
	}
	if envelope.RequestedModel != "gpt-5.5" || len(envelope.Segments) != 1 || envelope.Segments[0].Text != "keep this text" || envelope.Segments[0].Role != "user" {
		t.Fatal("escaped envelope retained mutable request backing storage")
	}
}
