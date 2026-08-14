package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexEvents loads hooks.json and returns the top-level hooks event map.
func codexEvents(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal hooks.json: %v", err)
	}
	events, _ := root["hooks"].(map[string]interface{})
	if events == nil {
		t.Fatalf("hooks.json missing top-level \"hooks\" object: %s", data)
	}
	return events
}

func eventLen(t *testing.T, events map[string]interface{}, name string) int {
	t.Helper()
	arr, _ := events[name].([]interface{})
	return len(arr)
}

// supacodeSeed mirrors the real multi-tenant ~/.codex/hooks.json shape: events
// wrapped under a top-level "hooks" object, with supacode-managed entries in
// SessionStart and Stop.
func supacodeSeed() []byte {
	root := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "supacode session_start # supacode-managed-hook", "timeout": 5},
					},
				},
			},
			"Stop": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "supacode stop # supacode-managed-hook", "timeout": 10},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(root, "", "  ")
	return data
}

// TestPatchCodexHooksMergesPreservingSupacode seeds a supacode-style hooks.json
// and verifies flow-breaker's entries are APPENDED (loud SessionStart, quiet
// UserPromptSubmit) while every supacode entry survives.
func TestPatchCodexHooksMergesPreservingSupacode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, supacodeSeed(), 0644); err != nil {
		t.Fatal(err)
	}

	if err := patchCodexHooks(path); err != nil {
		t.Fatalf("patchCodexHooks: %v", err)
	}
	events := codexEvents(t, path)

	// supacode's SessionStart entry survives + flow-breaker's is appended (2).
	if n := eventLen(t, events, "SessionStart"); n != 2 {
		t.Fatalf("SessionStart entries = %d, want 2 (supacode + flow-breaker)", n)
	}
	// supacode's Stop entry is untouched.
	if n := eventLen(t, events, "Stop"); n != 1 {
		t.Fatalf("Stop entries = %d, want 1 (supacode preserved)", n)
	}
	// UserPromptSubmit gets flow-breaker's quiet nudge.
	if n := eventLen(t, events, "UserPromptSubmit"); n != 1 {
		t.Fatalf("UserPromptSubmit entries = %d, want 1", n)
	}

	// supacode's commands are still present.
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "supacode-managed-hook") {
		t.Fatal("supacode entries were clobbered")
	}

	// flow-breaker: loud SessionStart, quiet UserPromptSubmit.
	if !hookInstalled(events, "SessionStart", "flow-breaker nudge") {
		t.Fatal("flow-breaker SessionStart nudge not appended")
	}
	if hookInstalled(events, "SessionStart", "--quiet") {
		t.Fatal("SessionStart must be LOUD (no --quiet)")
	}
	if !hookInstalled(events, "UserPromptSubmit", "flow-breaker nudge --quiet") {
		t.Fatal("flow-breaker UserPromptSubmit quiet nudge not appended")
	}
}

// TestPatchCodexHooksIdempotent verifies a re-run appends nothing.
func TestPatchCodexHooksIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, supacodeSeed(), 0644); err != nil {
		t.Fatal(err)
	}
	if err := patchCodexHooks(path); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if err := patchCodexHooks(path); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("second run changed the file (not idempotent):\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	events := codexEvents(t, path)
	if n := eventLen(t, events, "SessionStart"); n != 2 {
		t.Fatalf("SessionStart entries = %d after re-run, want 2 (no dup)", n)
	}
	if n := eventLen(t, events, "UserPromptSubmit"); n != 1 {
		t.Fatalf("UserPromptSubmit entries = %d after re-run, want 1 (no dup)", n)
	}
}

// TestPatchCodexHooksFromScratch verifies a missing hooks.json is created with
// the top-level "hooks" wrapper and both flow-breaker entries.
func TestPatchCodexHooksFromScratch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := patchCodexHooks(path); err != nil {
		t.Fatalf("patchCodexHooks: %v", err)
	}
	events := codexEvents(t, path)
	if !hookInstalled(events, "SessionStart", "flow-breaker nudge") {
		t.Fatal("SessionStart nudge missing")
	}
	if !hookInstalled(events, "UserPromptSubmit", "flow-breaker nudge --quiet") {
		t.Fatal("UserPromptSubmit quiet nudge missing")
	}
	// Each entry carries a generous timeout.
	if !strings.Contains(func() string { d, _ := os.ReadFile(path); return string(d) }(), "\"timeout\"") {
		t.Fatal("hook entries should carry a timeout")
	}
}

// TestWriteCodexRulesFailClosed verifies all 13 ExecPolicy prefix_rules with the
// fail-closed base (prompt) and clear (forbidden).
func TestWriteCodexRulesFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules", "flow-breaker.rules")
	if err := writeCodexRules(path); err != nil {
		t.Fatalf("writeCodexRules: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rules: %v", err)
	}
	content := string(data)

	if n := strings.Count(content, "prefix_rule("); n != 13 {
		t.Fatalf("expected 13 prefix_rules, got %d:\n%s", n, content)
	}
	// Base prefix must be prompt (fail-closed for unknown subcommands).
	if !strings.Contains(content, `prefix_rule(pattern=["flow-breaker"], decision="prompt")`) {
		t.Fatalf("base rule must be prompt:\n%s", content)
	}
	// clear must be forbidden.
	if !strings.Contains(content, `prefix_rule(pattern=["flow-breaker", "clear"], decision="forbidden")`) {
		t.Fatalf("clear must be forbidden:\n%s", content)
	}
	// reads + capture + add allow.
	for _, sub := range []string{"nudge", "status", "list", "ls", "archive", "cal-list", "cal-feeds", "capture", "add"} {
		want := `prefix_rule(pattern=["flow-breaker", "` + sub + `"], decision="allow")`
		if !strings.Contains(content, want) {
			t.Fatalf("missing allow rule for %q:\n%s", sub, content)
		}
	}
	// done/snooze prompt.
	for _, sub := range []string{"done", "snooze"} {
		want := `prefix_rule(pattern=["flow-breaker", "` + sub + `"], decision="prompt")`
		if !strings.Contains(content, want) {
			t.Fatalf("missing prompt rule for %q:\n%s", sub, content)
		}
	}
	// clear must NOT be allow/prompt anywhere.
	if strings.Contains(content, `"clear"], decision="allow"`) || strings.Contains(content, `"clear"], decision="prompt"`) {
		t.Fatal("clear must be forbidden-only")
	}
}

// TestWriteCodexRulesIdempotent verifies a re-run produces identical bytes.
func TestWriteCodexRulesIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules", "flow-breaker.rules")
	if err := writeCodexRules(path); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if err := writeCodexRules(path); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("rules file changed on re-run (not idempotent)")
	}
}

// TestPatchCodexAgentsMDSurfaceNotAct verifies the Codex AGENTS.md section is
// surface-not-act, marker-wrapped, describes the hooks.json transport, and
// carries no 30-min/loop guidance.
func TestPatchCodexAgentsMDSurfaceNotAct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	patchCodexAgentsMD(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "## flow-breaker") {
		t.Fatal("missing flow-breaker section")
	}
	if !strings.Contains(content, "hooks.json") {
		t.Fatal("section should describe the Codex hooks.json transport")
	}
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "never act") && !strings.Contains(lower, "not act") {
		t.Fatal("section must instruct to SURFACE, never act on the nudge")
	}
	if strings.Contains(content, "30 min") || strings.Contains(content, "/loop") || strings.Contains(content, "every 30 minutes") {
		t.Fatal("old 30-minute / /loop guidance must be removed")
	}
	if !strings.Contains(content, fbBeginMarker) || !strings.Contains(content, fbEndMarker) {
		t.Fatal("section must be marker-wrapped for in-place upsert")
	}

	// Idempotent: second run does not duplicate the section.
	patchCodexAgentsMD(path)
	again, _ := os.ReadFile(path)
	if strings.Count(string(again), "## flow-breaker") != 1 {
		t.Fatal("AGENTS.md section duplicated on re-run")
	}
}

// TestPatchCodexAgentsMDMigratesOldSection seeds an OLD unmarked section and
// verifies install migrates it in place, preserving the user's other content.
func TestPatchCodexAgentsMDMigratesOldSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	old := "# Agent notes\n\nSome preamble.\n\n" +
		"## flow-breaker\n\n" +
		"At the start of every conversation, run `flow-breaker nudge` and surface any alerts.\n\n" +
		"During long sessions, run `flow-breaker nudge` every 30 minutes to check for new overdue tasks.\n\n" +
		"## other section\n\nKeep me.\n"
	if err := os.WriteFile(path, []byte(old), 0644); err != nil {
		t.Fatal(err)
	}

	patchCodexAgentsMD(path)
	got, _ := os.ReadFile(path)
	content := string(got)

	if strings.Contains(content, "every 30 minutes") {
		t.Fatalf("old 30-min guidance not migrated away:\n%s", content)
	}
	if !strings.Contains(content, "never act on it") {
		t.Fatalf("migrated section missing surface-not-act text:\n%s", content)
	}
	if !strings.Contains(content, "## other section") || !strings.Contains(content, "Some preamble.") {
		t.Fatalf("migration clobbered the user's other content:\n%s", content)
	}
	if strings.Count(content, "## flow-breaker") != 1 {
		t.Fatalf("expected exactly one flow-breaker heading:\n%s", content)
	}

	// Second run is a no-op.
	patchCodexAgentsMD(path)
	again, _ := os.ReadFile(path)
	if string(again) != content {
		t.Fatal("second run changed the file (not idempotent)")
	}
}
