package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestRequiredToolChoiceOnlyWhileRecoveryIsActive(t *testing.T) {
	ctx, state := withRequiredToolChoice(context.Background())
	var choices []string
	transport := quotaAwareTransport{base: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		choices = append(choices, string(payload["tool_choice"]))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}}, nil
	})}
	request := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/chat/completions", bytes.NewReader([]byte(`{"messages":[],"tools":[{"type":"function"}]}`)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
	request()
	if !state.attempted.Load() || choices[0] != `"required"` {
		t.Fatalf("active recovery did not require a tool: attempted=%t choice=%q", state.attempted.Load(), choices[0])
	}
	state.active.Store(false)
	request()
	if choices[1] != "" {
		t.Fatalf("normal continuation still required a tool: %q", choices[1])
	}
}
