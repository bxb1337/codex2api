package proxy

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/codex2api/api"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesWSSteering struct {
	mu      sync.Mutex
	lane    *responsesWSLane
	current *responsesWSSteeringTurn
	closed  bool
}

type responsesWSSteeringTurn struct {
	settings    []byte
	reservation *responsesWSRetainedMemory
	responseID  string
	model       string
	running     bool
	terminal    string
	reason      string
	prewarm     bool
	required    []map[string]string
	pending     []*responsesWSSteer
	applied     []*responsesWSSteer
	committed   bool
	seenCalls   map[string]struct{}
	automatic   bool
}

func (s *responsesWSSteering) owns(responseID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.current != nil && s.current.responseID == responseID
}

func (s *responsesWSSteering) accept(steer *responsesWSSteer) *api.APIError {
	s.mu.Lock()
	defer s.mu.Unlock()
	if apiErr := s.validate(steer); apiErr != nil {
		return apiErr
	}
	t := s.current
	if len(t.pending) >= responsesWSPendingSteerLimit || !reserveResponsesWSSteer(s.lane.mux, steer) {
		return responsesWSSteerError("too_many_pending_steers", "Too much steering input is pending")
	}
	steer.ID = "steer_" + strings.ReplaceAll(NewUpstreamSessionUUID(), "-", "")
	if err := s.lane.emitSteer("response.steer.accepted", steer, nil); err != nil {
		steer.release()
		s.lane.mux.cancel()
		return nil
	}
	t.pending = append(t.pending, steer)
	return nil
}

func (s *responsesWSSteering) validate(steer *responsesWSSteer) *api.APIError {
	t := s.current
	if s.closed || t == nil || t.responseID != steer.PreviousResponseID {
		return responsesWSSteerError("response_not_found", "The target response is not available")
	}
	if !t.running || t.terminal != "" {
		return responsesWSSteerError("response_already_completed", "The target response has already completed")
	}
	if t.prewarm || !strings.HasPrefix(strings.ToLower(t.model), "gpt-6") || t.settings == nil {
		return responsesWSSteerError("steering_not_supported", "The model or execution mode does not support steering")
	}
	return nil
}

func reserveResponsesWSSteer(mux *responsesWSMultiplexer, steer *responsesWSSteer) bool {
	size := len(steer.Input)
	for _, item := range steer.items {
		size += len(item)
	}
	reservation, ok := mux.retain(int64(size))
	steer.reservation = reservation
	return ok
}

func (s *responsesWSSteering) begin(payload []byte, automatic []*responsesWSSteer) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	applied := automatic
	if t := s.current; t != nil {
		if automatic == nil && gjson.GetBytes(payload, "previous_response_id").String() == t.responseID {
			applied = t.pending
			payload = appendResponsesWSSteers(payload, applied)
		} else {
			s.fail(t.pending, "response_not_active", "A new request replaced the steering target")
		}
		t.reservation.Release()
	}
	settings := responsesWSSteerSettings(payload)
	reservation, admitted := s.lane.mux.retain(int64(len(settings)))
	if !admitted {
		settings = nil
	}
	s.current = &responsesWSSteeringTurn{settings: settings, reservation: reservation, running: true,
		model: strings.Clone(gjson.GetBytes(payload, "model").String()), prewarm: gjson.GetBytes(payload, "generate").Type == gjson.False,
		applied: applied, seenCalls: make(map[string]struct{}), automatic: automatic != nil}
	return payload
}

func (s *responsesWSSteering) deliverFailure() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.current
	return t == nil || !t.automatic || t.committed
}

func responsesWSSteerSettings(payload []byte) []byte {
	for _, field := range []string{"input", "previous_response_id", "event_id", newAPIPolicyWebSocketEventField, "generate"} {
		payload, _ = sjson.DeleteBytes(payload, field)
	}
	return payload
}

func appendResponsesWSSteers(payload []byte, steers []*responsesWSSteer) []byte {
	if len(steers) == 0 {
		return payload
	}
	var input []json.RawMessage
	for _, steer := range steers {
		input = append(input, steer.items...)
	}
	current := gjson.GetBytes(payload, "input")
	if current.Exists() && current.Type != gjson.Null {
		input = append(input, responsesWSSteerItems(current)...)
	}
	encoded, _ := json.Marshal(input)
	result, err := sjson.SetRawBytes(payload, "input", encoded)
	if err != nil {
		return payload
	}
	return result
}

func (s *responsesWSSteering) observe(payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.current
	if t == nil || s.closed {
		return
	}
	typeName := gjson.GetBytes(payload, "type").String()
	response := gjson.GetBytes(payload, "response")
	if typeName == "response.output_item.done" {
		collectResponsesWSSteeringCall(t, gjson.GetBytes(payload, "item"))
	}
	if typeName == "response.created" {
		t.responseID = strings.Clone(response.Get("id").String())
		if model := response.Get("model").String(); model != "" {
			t.model = strings.Clone(model)
		}
	}
	if isResponsesTerminalEvent(typeName) || typeName == "error" {
		t.terminal, t.reason = typeName, strings.Clone(response.Get("incomplete_details.reason").String())
		if output := response.Get("output"); output.IsArray() && len(output.Array()) > 0 {
			t.required = responsesWSRequiredInputs(response)
		}
	}
}

func (s *responsesWSSteering) delivered(payload []byte) {
	if gjson.GetBytes(payload, "type").String() != "response.created" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.current; t != nil {
		t.committed = true
		for _, steer := range t.applied {
			steer.release()
		}
		t.applied = nil
	}
}

func (s *responsesWSSteering) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.current
	if t == nil {
		return
	}
	t.running = false
	if !t.committed {
		s.fail(t.applied, "successor_creation_failed", "The successor response could not be created")
		t.applied = nil
	}
	if !responsesWSSteerTerminalContinues(t) {
		s.fail(t.pending, "response_not_active", "The target response ended without a continuation")
		t.pending = nil
		return
	}
	for _, steer := range t.pending {
		if len(t.required) > 0 && len(s.lane.messages) == 0 && !steer.pendingNotified {
			_ = s.lane.emitSteer("response.steer.pending", steer, t.required)
			steer.pendingNotified = true
		}
	}
}

func responsesWSSteerTerminalContinues(turn *responsesWSSteeringTurn) bool {
	return turn.terminal == "response.completed" || turn.terminal == "response.incomplete" &&
		(turn.reason == "steered" || turn.reason == "interrupted")
}

func (s *responsesWSSteering) next() ([]byte, []*responsesWSSteer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.current
	if s.closed || t == nil || t.running || len(t.required) > 0 || len(t.pending) == 0 {
		return nil, nil
	}
	payload, _ := sjson.SetBytes(t.settings, "previous_response_id", t.responseID)
	payload = appendResponsesWSSteers(payload, t.pending)
	steers := t.pending
	t.pending = nil
	return payload, steers
}

func (s *responsesWSSteering) fail(steers []*responsesWSSteer, code, message string) {
	for _, steer := range steers {
		_ = s.lane.emitSteer("response.steer.failed", steer, responsesWSSteerError(code, message))
		steer.release()
	}
}

func (s *responsesWSSteering) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if t := s.current; t != nil {
		t.reservation.Release()
		for _, steer := range append(t.pending, t.applied...) {
			steer.release()
		}
	}
	s.current = nil
}
