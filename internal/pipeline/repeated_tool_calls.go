package pipeline

// RepeatedToolCall reports a tool call signature that appears more than once in
// the request history. It is instrumentation only: callers can use it to spot
// rereads/retries and tune compaction thresholds without changing behavior.
type RepeatedToolCall struct {
	Name         string `json:"name"`
	Occurrences  int    `json:"occurrences"`
	FirstMessage int    `json:"first_message_index"`
	LastMessage  int    `json:"last_message_index"`
}

func RepeatedToolCalls(req *ChatRequest) []RepeatedToolCall {
	type stat struct {
		name         string
		occurrences  int
		firstMessage int
		lastMessage  int
	}
	stats := map[string]*stat{}
	order := []string{}
	for i, m := range req.Messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			h := hashCall(tc.Function.Name, tc.Function.Arguments)
			s, ok := stats[h]
			if !ok {
				stats[h] = &stat{name: tc.Function.Name, occurrences: 1, firstMessage: i, lastMessage: i}
				order = append(order, h)
				continue
			}
			s.occurrences++
			s.lastMessage = i
		}
	}
	out := []RepeatedToolCall{}
	for _, h := range order {
		s := stats[h]
		if s.occurrences < 2 {
			continue
		}
		out = append(out, RepeatedToolCall{Name: s.name, Occurrences: s.occurrences, FirstMessage: s.firstMessage, LastMessage: s.lastMessage})
	}
	return out
}
