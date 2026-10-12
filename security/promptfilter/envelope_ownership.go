package promptfilter

import "strings"

// AuditMetadata 只保留审计归属信息，不携带正文、精确预检或其私有引用。
func (e RequestEnvelope) AuditMetadata() RequestEnvelope {
	e.Segments = nil
	e.currentUserExactText = ""
	e.currentUserPrecheck = nil
	e.precheckIncomplete = false
	e.untrustedApprovalModel = ""
	return ownEnvelopeMetadata(e)
}

func ownEnvelopeMetadata(e RequestEnvelope) RequestEnvelope {
	e.Endpoint = strings.Clone(e.Endpoint)
	e.RequestedModel = strings.Clone(e.RequestedModel)
	e.EffectiveModel = strings.Clone(e.EffectiveModel)
	e.untrustedApprovalModel = strings.Clone(e.untrustedApprovalModel)
	e.AdapterUnclassifiedTypes = append([]string(nil), e.AdapterUnclassifiedTypes...)
	for index, name := range e.AdapterUnclassifiedTypes {
		e.AdapterUnclassifiedTypes[index] = strings.Clone(name)
	}
	return e
}

// 预算裁剪完成后才复制字符串，避免小片段长期引用整个解码正文。
func ownBoundedEnvelope(e RequestEnvelope) RequestEnvelope {
	e = ownEnvelopeMetadata(e)
	e.Segments = append([]Segment(nil), e.Segments...)
	for index := range e.Segments {
		segment := &e.Segments[index]
		segment.Text = strings.Clone(segment.Text)
		segment.Role = strings.Clone(segment.Role)
		segment.SafetyEvidence = strings.Clone(segment.SafetyEvidence)
	}
	e.currentUserExactText = strings.Clone(e.currentUserExactText)
	return e
}

func envelopeRetainedTextBytes(e RequestEnvelope) int {
	total := len(e.Endpoint) + len(e.RequestedModel) + len(e.EffectiveModel) + len(e.untrustedApprovalModel) + len(e.currentUserExactText)
	for _, name := range e.AdapterUnclassifiedTypes {
		total += len(name)
	}
	for _, segment := range e.Segments {
		total += len(segment.Text) + len(segment.Role) + len(segment.SafetyEvidence)
	}
	return total
}
