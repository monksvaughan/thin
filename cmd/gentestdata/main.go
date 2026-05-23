// Command gentestdata writes a JSONL file of synthetic coding-agent
// requests to stdout. It models the kind of waste we expect to see:
//   - 30 tool definitions, of which only 3 are actually used
//   - The same file read 3+ times across turns
//   - Long stack traces and build outputs in old tool results
//
// This is for smoke-testing the replay tool and getting a sanity-check on
// pass effectiveness BEFORE we have real recorded traffic.
//
// Usage:
//   go run ./cmd/gentestdata > test.jsonl
//   cat test.jsonl | go run ./cmd/replay
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string                 `json:"name"`
		Description string                 `json:"description"`
		Parameters  map[string]interface{} `json:"parameters"`
	} `json:"function"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type message struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content,omitempty"`
	ToolCalls  []toolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Tools    []tool    `json:"tools"`
	Stream   bool      `json:"stream"`
}

func mkTool(name, desc string) tool {
	t := tool{Type: "function"}
	t.Function.Name = name
	t.Function.Description = desc
	t.Function.Parameters = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path":  map[string]string{"type": "string"},
			"query": map[string]string{"type": "string"},
		},
	}
	return t
}

func main() {
	// 30 tools — realistic for an MCP-rich coding agent.
	tools := []tool{
		mkTool("read_file", "Read the contents of a file at a given path."),
		mkTool("write_file", "Write contents to a file."),
		mkTool("run_bash", "Run a bash command in the workspace."),
		mkTool("grep", "Search files for a pattern."),
		mkTool("list_dir", "List directory contents."),
		mkTool("git_status", "Show git status."),
		mkTool("git_diff", "Show git diff."),
		mkTool("git_log", "Show git log."),
		mkTool("git_blame", "Run git blame."),
		mkTool("git_branch", "List branches."),
		mkTool("git_commit", "Make a commit."),
		mkTool("git_push", "Push to a remote."),
		mkTool("create_pr", "Open a pull request."),
		mkTool("list_prs", "List open pull requests."),
		mkTool("review_pr", "Review a pull request."),
		mkTool("merge_pr", "Merge a pull request."),
		mkTool("close_issue", "Close a GitHub issue."),
		mkTool("transfer_issue", "Transfer an issue to another repo."),
		mkTool("delete_branch", "Delete a branch."),
		mkTool("delete_file", "Delete a file."),
		mkTool("delete_repo", "Delete the entire repository."),
		mkTool("web_search", "Search the web."),
		mkTool("web_fetch", "Fetch a URL."),
		mkTool("run_tests", "Run the test suite."),
		mkTool("run_lint", "Run linting."),
		mkTool("format_code", "Format code with the project's formatter."),
		mkTool("install_dep", "Install an npm/pip dependency."),
		mkTool("upgrade_dep", "Upgrade a dependency."),
		mkTool("docker_build", "Build a Docker image."),
		mkTool("docker_run", "Run a container."),
	}

	// The agent will only ever use these three:
	// read_file (multiple times on the same file!), grep, run_tests.

	authJsContent := strings.Repeat("export function authenticate(user) { /* impl */ }\n", 80)
	stackTrace := strings.Repeat("    at Object.<anonymous> (/app/src/foo.ts:42:13)\n", 50)

	// Build a 10-turn conversation.
	var msgs []message
	msgs = append(msgs, message{Role: "system", Content: "You are a coding agent. Use the available tools to investigate and fix the user's bug."})
	msgs = append(msgs, message{Role: "user", Content: "Login is broken in main. Investigate and fix."})

	// Turn 1: agent reads auth.js
	msgs = append(msgs, message{Role: "assistant", ToolCalls: []toolCall{{
		ID: "c1", Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read_file", Arguments: `{"path":"src/auth.js"}`},
	}}})
	msgs = append(msgs, message{Role: "tool", ToolCallID: "c1", Content: authJsContent})

	// Turn 2: agent runs tests, gets a long failure
	msgs = append(msgs, message{Role: "assistant", ToolCalls: []toolCall{{
		ID: "c2", Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "run_tests", Arguments: `{}`},
	}}})
	msgs = append(msgs, message{Role: "tool", ToolCallID: "c2", Content: "FAIL src/auth.test.js\n" + stackTrace})

	// Turn 3: agent re-reads auth.js (waste!)
	msgs = append(msgs, message{Role: "assistant", ToolCalls: []toolCall{{
		ID: "c3", Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read_file", Arguments: `{"path":"src/auth.js"}`},
	}}})
	msgs = append(msgs, message{Role: "tool", ToolCallID: "c3", Content: authJsContent})

	// Turn 4: grep
	msgs = append(msgs, message{Role: "assistant", ToolCalls: []toolCall{{
		ID: "c4", Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "grep", Arguments: `{"query":"authenticate"}`},
	}}})
	msgs = append(msgs, message{Role: "tool", ToolCallID: "c4", Content: "src/auth.js:1: export function authenticate(...)"})

	// Turn 5: re-read AGAIN (more waste!)
	msgs = append(msgs, message{Role: "assistant", ToolCalls: []toolCall{{
		ID: "c5", Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read_file", Arguments: `{"path":"src/auth.js"}`},
	}}})
	msgs = append(msgs, message{Role: "tool", ToolCallID: "c5", Content: authJsContent})

	msgs = append(msgs, message{Role: "user", Content: "What did you find?"})

	enc := json.NewEncoder(os.Stdout)
	// Simulate the proxy seeing this conversation grow turn by turn.
	for cut := 4; cut <= len(msgs); cut += 2 {
		req := chatRequest{
			Model:    "gpt-5",
			Messages: msgs[:cut],
			Tools:    tools,
			Stream:   true,
		}
		if err := enc.Encode(req); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
