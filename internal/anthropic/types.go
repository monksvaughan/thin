// Package anthropic defines a native Anthropic Messages API rewrite pipeline.
package anthropic

import "encoding/json"

// MessagesRequest models /v1/messages. Extra preserves any request fields we
// don't explicitly understand so provider-specific options round-trip intact.
type MessagesRequest struct {
	Model    string                     `json:"model"`
	System   json.RawMessage            `json:"system,omitempty"`
	Messages []Message                  `json:"messages"`
	Tools    []Tool                     `json:"tools,omitempty"`
	Stream   bool                       `json:"stream,omitempty"`
	Extra    map[string]json.RawMessage `json:"-"`
}

type Message struct {
	Role    string                     `json:"role"`
	Content json.RawMessage            `json:"content,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

type Tool struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description,omitempty"`
	InputSchema json.RawMessage            `json:"input_schema,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

// ContentBlock is the small subset of Anthropic content blocks we need to
// inspect. Unknown fields such as cache_control are preserved.
type ContentBlock struct {
	Type      string                     `json:"type"`
	Text      string                     `json:"text,omitempty"`
	ID        string                     `json:"id,omitempty"`
	Name      string                     `json:"name,omitempty"`
	Input     json.RawMessage            `json:"input,omitempty"`
	ToolUseID string                     `json:"tool_use_id,omitempty"`
	Content   json.RawMessage            `json:"content,omitempty"`
	Extra     map[string]json.RawMessage `json:"-"`
}

var knownRequestFields = map[string]bool{
	"model": true, "system": true, "messages": true, "tools": true, "stream": true,
}

var knownMessageFields = map[string]bool{
	"role": true, "content": true,
}

var knownToolFields = map[string]bool{
	"name": true, "description": true, "input_schema": true,
}

var knownContentBlockFields = map[string]bool{
	"type": true, "text": true, "id": true, "name": true, "input": true,
	"tool_use_id": true, "content": true,
}

func (r *MessagesRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type alias MessagesRequest
	var tmp alias
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*r = MessagesRequest(tmp)
	r.Extra = nil
	for k, v := range raw {
		if !knownRequestFields[k] {
			if r.Extra == nil {
				r.Extra = map[string]json.RawMessage{}
			}
			r.Extra[k] = v
		}
	}
	return nil
}

func (r MessagesRequest) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	type alias MessagesRequest
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

func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	type alias ContentBlock
	var tmp alias
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*b = ContentBlock(tmp)
	b.Extra = nil
	for k, v := range raw {
		if !knownContentBlockFields[k] {
			if b.Extra == nil {
				b.Extra = map[string]json.RawMessage{}
			}
			b.Extra[k] = v
		}
	}
	return nil
}

func (b ContentBlock) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	type alias ContentBlock
	known, err := json.Marshal(alias(b))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(known, &out); err != nil {
		return nil, err
	}
	for k, v := range b.Extra {
		out[k] = v
	}
	return json.Marshal(out)
}

func contentBlocks(raw json.RawMessage) ([]ContentBlock, bool) {
	var blocks []ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, false
	}
	return blocks, true
}
