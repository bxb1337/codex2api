package proxy

import (
	"encoding/json"
	"strings"

	"github.com/codex2api/api"
	"github.com/tidwall/gjson"
)

type responsesWSSteerReference struct {
	ID                 string `json:"id"`
	PreviousResponseID string `json:"previous_response_id"`
}

func (lane *responsesWSLane) emitSteer(event string, steer *responsesWSSteer, detail any) error {
	frame := map[string]any{"type": event, "sequence_number": 0,
		"steer": responsesWSSteerReference{steer.ID, steer.PreviousResponseID}}
	if event == "response.steer.failed" {
		frame["steer"], frame["error"] = steer, detail
	}
	if event == "response.steer.pending" {
		frame["reason"], frame["required_input"] = "waiting_for_required_input", detail
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return lane.writeRaw(payload, responsesWSWriteTimeout)
}

func (s *responsesWSControlSession) handleSteer(payload []byte) {
	steer, apiErr := normalizeResponsesWSSteer(payload)
	if apiErr != nil {
		s.rejectSteer(payload, apiErr)
		return
	}
	lane := s.responseLane(steer.PreviousResponseID)
	if lane == nil {
		s.rejectSteer(payload, responsesWSSteerError("response_not_found", "The target response is not available on this connection"))
		return
	}
	if apiErr = lane.steering.accept(steer); apiErr != nil {
		_ = lane.emitSteer("response.steer.failed", steer, apiErr)
	}
}

func (s *responsesWSControlSession) rejectSteer(payload []byte, apiErr *api.APIError) {
	input := json.RawMessage(gjson.GetBytes(payload, "input").Raw)
	if !json.Valid(input) {
		input = json.RawMessage(`""`)
	}
	steer := &responsesWSSteer{PreviousResponseID: gjson.GetBytes(payload, "previous_response_id").String(), Input: input}
	if lane := s.responseLane(steer.PreviousResponseID); lane != nil {
		_ = lane.emitSteer("response.steer.failed", steer, apiErr)
		return
	}
	frame, _ := json.Marshal(map[string]any{"type": "response.steer.failed", "sequence_number": 0, "steer": steer, "error": apiErr})
	_ = writeResponsesWSMessage(s.conn, frame)
}

func collectResponsesWSSteeringCall(turn *responsesWSSteeringTurn, item gjson.Result) {
	stub := responsesWSRequiredInput(item)
	if stub == nil || len(turn.required) >= responseOutputCollectorMaxItems {
		return
	}
	key := stub["type"] + ":" + stub["call_id"] + ":" + stub["approval_request_id"]
	if _, exists := turn.seenCalls[key]; exists {
		return
	}
	turn.seenCalls[key] = struct{}{}
	turn.required = append(turn.required, stub)
}

func (s *responsesWSControlSession) responseLane(responseID string) *responsesWSLane {
	s.mu.Lock()
	lanes := make([]*responsesWSLane, 0, len(s.lanes))
	for _, lane := range s.lanes {
		lanes = append(lanes, lane)
	}
	s.mu.Unlock()
	for _, lane := range lanes {
		if lane.steering.owns(responseID) {
			return lane
		}
	}
	return nil
}

func responsesWSRequiredInputs(response gjson.Result) []map[string]string {
	var required []map[string]string
	for _, item := range response.Get("output").Array() {
		if stub := responsesWSRequiredInput(item); stub != nil {
			required = append(required, stub)
		}
	}
	return required
}

func responsesWSRequiredInput(item gjson.Result) map[string]string {
	kind := item.Get("type").String()
	output := map[string]string{"function_call": "function_call_output", "custom_tool_call": "custom_tool_call_output",
		"computer_call": "computer_call_output", "shell_call": "shell_call_output", "apply_patch_call": "apply_patch_call_output",
		"tool_search_call": "tool_search_output", "mcp_approval_request": "mcp_approval_response"}[kind]
	if output == "" || kind == "tool_search_call" && item.Get("execution").String() != "client" {
		return nil
	}
	stub := map[string]string{"type": output}
	if kind == "mcp_approval_request" {
		stub["approval_request_id"] = strings.Clone(item.Get("id").String())
		return stub
	}
	stub["call_id"] = strings.Clone(item.Get("call_id").String())
	if kind == "function_call" {
		stub["name"] = strings.Clone(item.Get("name").String())
	}
	if kind == "tool_search_call" {
		stub["execution"] = "client"
	}
	return stub
}
