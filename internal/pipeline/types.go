// Package pipeline defines the optimization passes that rewrite a chat
// completion request before it goes upstream. The types here are a minimal
// subset of the OpenAI Chat Completions schema — just enough to identify
// messages, tools, and tool calls so we can rewrite them.
//
// Unknown fields are preserved via json.RawMessage on a passthrough field
// so we don't lose anything the upstream cares about (response_format,
// reasoning_effort, etc.).
package pipeline

import "encoding/json"

// ChatRequest models /v1/chat/completions. Extra is a catch-all for fields
// we don't explicitly model; we serialize them back out unchanged.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream,omitempty"`
	// Extra holds passthrough fields. Populated by custom UnmarshalJSON.
	Extra map[string]json.RawMessage `json:"-"`
}

// Message is one element of the messages array. Content can be a string or
// a list of parts (for multimodal or for Anthropic-style cache_control
// blocks), so we keep it as RawMessage.
type Message struct {
	Role       string                     `json:"role"`
	Content    json.RawMessage            `json:"content,omitempty"`
	Name       string                     `json:"name,omitempty"`
	ToolCalls  []ToolCall                 `json:"tool_calls,omitempty"`
	ToolCallID string                     `json:"tool_call_id,omitempty"`
	Extra      map[string]json.RawMessage `json:"-"`
}

// Tool is a function tool definition. Extra preserves any top-level fields
// we don't model (Anthropic's cache_control, vendor-specific hints) so they
// survive the round trip to the upstream.
type Tool struct {
	Type     string                     `json:"type"`
	Function ToolFunction               `json:"function"`
	Extra    map[string]json.RawMessage `json:"-"`
}

// ToolFunction describes a callable function tool. Extra preserves fields
// like OpenAI's `strict: true` that we don't model explicitly — dropping
// them would silently change upstream behavior.
type ToolFunction struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	Parameters  json.RawMessage            `json:"parameters,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

// ToolCall is a single invocation produced by the assistant.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	Extra map[string]json.RawMessage `json:"-"`
}

// known fields we model explicitly; everything else flows into Extra.
var knownTopLevelFields = map[string]bool{
	"model": true, "messages": true, "tools": true, "stream": true,
}

var knownMessageFields = map[string]bool{
	"role": true, "content": true, "name": true, "tool_calls": true, "tool_call_id": true,
}

var knownToolCallFields = map[string]bool{
	"id": true, "type": true, "function": true,
}

var knownToolFields = map[string]bool{
	"type": true, "function": true,
}

var knownToolFunctionFields = map[string]bool{
	"name": true, "description": true, "parameters": true,
}

func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	type alias ChatRequest
	var tmp alias
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*r = ChatRequest(tmp)

	r.Extra = make(map[string]json.RawMessage)
	for k, v := range raw {
		if !knownTopLevelFields[k] {
			r.Extra[k] = v
		}
	}
	return nil
}

func (r ChatRequest) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	type alias ChatRequest
	known, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(known, &out); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type alias Message
	var tmp alias
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*m = Message(tmp)
	m.Extra = nil
	for k, v := range raw {
		if !knownMessageFields[k] {
			if m.Extra == nil {
				m.Extra = map[string]json.RawMessage{}
			}
			m.Extra[k] = v
		}
	}
	return nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	type alias Message
	known, err := json.Marshal(alias(m))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(known, &out); err != nil {
		return nil, err
	}
	for k, v := range m.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}

func (tc *ToolCall) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type alias ToolCall
	var tmp alias
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*tc = ToolCall(tmp)
	tc.Extra = nil
	for k, v := range raw {
		if !knownToolCallFields[k] {
			if tc.Extra == nil {
				tc.Extra = map[string]json.RawMessage{}
			}
			tc.Extra[k] = v
		}
	}
	return nil
}

func (tc ToolCall) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	type alias ToolCall
	known, err := json.Marshal(alias(tc))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(known, &out); err != nil {
		return nil, err
	}
	for k, v := range tc.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}

func (t *Tool) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type alias Tool
	var tmp alias
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*t = Tool(tmp)
	t.Extra = nil
	for k, v := range raw {
		if !knownToolFields[k] {
			if t.Extra == nil {
				t.Extra = map[string]json.RawMessage{}
			}
			t.Extra[k] = v
		}
	}
	return nil
}

func (t Tool) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	type alias Tool
	known, err := json.Marshal(alias(t))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(known, &out); err != nil {
		return nil, err
	}
	for k, v := range t.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}

func (f *ToolFunction) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type alias ToolFunction
	var tmp alias
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*f = ToolFunction(tmp)
	f.Extra = nil
	for k, v := range raw {
		if !knownToolFunctionFields[k] {
			if f.Extra == nil {
				f.Extra = map[string]json.RawMessage{}
			}
			f.Extra[k] = v
		}
	}
	return nil
}

func (f ToolFunction) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	type alias ToolFunction
	known, err := json.Marshal(alias(f))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(known, &out); err != nil {
		return nil, err
	}
	for k, v := range f.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}
