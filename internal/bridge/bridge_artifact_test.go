package bridge

import (
	"encoding/json"
	"testing"
)

func TestRewriteArtifactURL(t *testing.T) {
	b := &Bridge{gatewayBaseURL: "https://gw.example.com"}

	if got := b.rewriteArtifactURL("/api/resource?file=a%2Fb.txt"); got != "https://gw.example.com/api/resource?file=a%2Fb.txt" {
		t.Fatalf("rewrite absolute-path URL failed, got %q", got)
	}
	if got := b.rewriteArtifactURL("api/resource?file=a%2Fb.txt"); got != "https://gw.example.com/api/resource?file=a%2Fb.txt" {
		t.Fatalf("rewrite relative URL failed, got %q", got)
	}
	if got := b.rewriteArtifactURL("https://cdn.example.com/a.txt"); got != "https://cdn.example.com/a.txt" {
		t.Fatalf("http URL should remain unchanged, got %q", got)
	}
}

func TestNormalizeRunnerEvent_RewriteArtifactPublish(t *testing.T) {
	b := &Bridge{gatewayBaseURL: "https://gw.example.com"}
	raw := json.RawMessage(`{
	  "type":"artifact.publish",
	  "chatId":"chat_1",
	  "runId":"run_1",
	  "artifact":{"type":"file","name":"report.md","url":"/api/resource?file=chat_1%2Fartifacts%2Frun_1%2Freport.md"}
	}`)

	out := b.normalizeRunnerEvent("chat_1", raw)
	var payload map[string]interface{}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal normalized event: %v", err)
	}
	artifact := payload["artifact"].(map[string]interface{})
	got := artifact["url"].(string)
	want := "https://gw.example.com/api/resource?file=chat_1%2Fartifacts%2Frun_1%2Freport.md"
	if got != want {
		t.Fatalf("artifact url not rewritten, got %q want %q", got, want)
	}
}

func TestNormalizeRunnerEvent_NonArtifactUnchanged(t *testing.T) {
	b := &Bridge{gatewayBaseURL: "https://gw.example.com"}
	raw := json.RawMessage(`{"type":"content.delta","delta":"hello"}`)
	out := b.normalizeRunnerEvent("chat_1", raw)
	if string(out) != string(raw) {
		t.Fatalf("non artifact event should stay unchanged, got %s", string(out))
	}
}

