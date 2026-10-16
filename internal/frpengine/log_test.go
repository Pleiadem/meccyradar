package frpengine

import "testing"

func TestRedactLogLineRemovesConfiguredSecrets(t *testing.T) {
	line := redactLogLine("connect user=card-user token=secret-token server=node.example:7000", []string{"card-user", "secret-token", "node.example:7000"})
	if line != "connect user=[REDACTED] token=[REDACTED] server=[REDACTED]" {
		t.Fatalf("unexpected redacted line: %q", line)
	}
}

func TestRedactLogLineIgnoresEmptySecrets(t *testing.T) {
	line := redactLogLine("ordinary log", []string{"", "  "})
	if line != "ordinary log" {
		t.Fatalf("unexpected line: %q", line)
	}
}
