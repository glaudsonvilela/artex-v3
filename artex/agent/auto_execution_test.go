package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestAutoExecutionGuidanceSurvivesDatabasePrompt(t *testing.T) {
	previous := PromptOverride()
	t.Cleanup(func() { SetPromptOverride(previous) })
	SetPromptOverride(func(key string) (string, bool) {
		if key == "auto" {
			return "You are an assistant.", true
		}
		return "", false
	})

	auto := chatSystem("auto", "/data", "/work")
	if !strings.Contains(auto, "Use Bash, Read, Write, Edit") {
		t.Fatal("Auto lost its direct VM execution guidance")
	}
	if !strings.Contains(auto, "operator-specified path") {
		t.Fatal("Auto cannot use paths requested by the operator")
	}
	if strings.Contains(chatSystem("pentest", "/data", "/work"), "Auto operator execution:") {
		t.Fatal("Auto guidance leaked into another chat role")
	}
}

func TestAutoRetriesPlanOnlyReplyWithToolExecution(t *testing.T) {
	calls := 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		calls++
		var events []llm.StreamEvent
		switch calls {
		case 1:
			events = []llm.StreamEvent{{Type: llm.SETextDelta, Text: "I would make a plan."}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		case 2:
			events = []llm.StreamEvent{{Type: llm.SEToolUseStart, ToolID: "call-1", ToolName: "Bash"}, {Type: llm.SEToolInputJSON, Text: `{"command":"printf action-executed"}`}, {Type: llm.SEMessageDelta, StopReason: "tool_use"}}
		default:
			events = []llm.StreamEvent{{Type: llm.SETextDelta, Text: "done"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		}
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}}
	c := NewChatAgent(provider, "fixture", t.TempDir(), nil, 0)
	var activities []db.Activity
	_, err := c.Chat(t.Context(), "auto", "conv-auto-execute", "Run a command", 5, 0, false, func(a db.Activity) { activities = append(activities, a) })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("provider calls=%d, want plan + tool + completion", calls)
	}
	used, result := false, false
	for _, a := range activities {
		if strings.Contains(a.Detail, "I would make a plan.") {
			t.Fatal("plan-only first reply leaked into visible activities")
		}
		if a.Kind == "tool_use" && a.Tool == "Bash" {
			used = true
		}
		if a.Kind == "tool_result" && strings.Contains(a.Detail, "action-executed") {
			result = true
		}
	}
	if !used || !result {
		t.Fatalf("Auto did not execute Bash: used=%t result=%t", used, result)
	}
}
