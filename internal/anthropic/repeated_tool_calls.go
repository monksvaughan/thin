package anthropic

// RepeatedToolCall reports a tool_use signature that appears more than once in
// request history. It is instrumentation only.
type RepeatedToolCall struct {
	Name         string `json:"name"`
	Occurrences  int    `json:"occurrences"`
	FirstMessage int    `json:"first_message_index"`
	LastMessage  int    `json:"last_message_index"`
}

func RepeatedToolCalls(req *MessagesRequest) []RepeatedToolCall {
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
		blocks, ok := contentBlocks(m.Content)
		if !ok {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" {
				continue
			}
			h := hashToolUse(b.Name, b.Input)
			s, ok := stats[h]
			if !ok {
				stats[h] = &stat{name: b.Name, occurrences: 1, firstMessage: i, lastMessage: i}
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
