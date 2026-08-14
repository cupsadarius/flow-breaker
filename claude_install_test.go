package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readSettings loads and unmarshals a settings.json written by patchSettings.
func readSettings(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	return m
}

// permStrings pulls settings.permissions[key] as a []string.
func permStrings(t *testing.T, s map[string]interface{}, key string) []string {
	t.Helper()
	perms, _ := s["permissions"].(map[string]interface{})
	list, _ := perms[key].([]interface{})
	var out []string
	for _, e := range list {
		if str, ok := e.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func countHooks(t *testing.T, s map[string]interface{}, event string) int {
	t.Helper()
	hooks, _ := s["hooks"].(map[string]interface{})
	entries, _ := hooks[event].([]interface{})
	return len(entries)
}

// TestPatchSettingsGranularFailClosed verifies the fail-closed permission model:
// reads + capture + add allowed, done/snooze ask, clear denied, and NO wildcard.
func TestPatchSettingsGranularFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	patchSettings(path)
	s := readSettings(t, path)

	allow := permStrings(t, s, "allow")
	for _, want := range []string{
		"Bash(flow-breaker nudge:*)",
		"Bash(flow-breaker status:*)",
		"Bash(flow-breaker next:*)",
		"Bash(flow-breaker overdue:*)",
		"Bash(flow-breaker alarm:*)",
		"Bash(flow-breaker calendar:*)",
		"Bash(flow-breaker capture:*)",
		"Bash(flow-breaker add:*)",
	} {
		if !contains(allow, want) {
			t.Fatalf("allow missing %q; got %v", want, allow)
		}
	}
	// No blanket wildcard that would auto-allow unknown subcommands.
	for _, a := range allow {
		if strings.Contains(a, "flow-breaker *") || strings.Contains(a, "flow-breaker:*") {
			t.Fatalf("allow contains a dangerous wildcard %q", a)
		}
	}

	ask := permStrings(t, s, "ask")
	if !contains(ask, "Bash(flow-breaker done:*)") || !contains(ask, "Bash(flow-breaker snooze:*)") {
		t.Fatalf("ask missing done/snooze; got %v", ask)
	}

	deny := permStrings(t, s, "deny")
	if !contains(deny, "Bash(flow-breaker clear:*)") {
		t.Fatalf("deny missing clear; got %v", deny)
	}
	// clear must NOT leak into allow/ask.
	if contains(allow, "Bash(flow-breaker clear:*)") || contains(ask, "Bash(flow-breaker clear:*)") {
		t.Fatal("clear must be deny-only")
	}
}

// TestPatchSettingsBothHooks verifies a loud SessionStart and a quiet
// UserPromptSubmit hook are installed.
func TestPatchSettingsBothHooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	patchSettings(path)
	s := readSettings(t, path)

	hooks, _ := s["hooks"].(map[string]interface{})
	if !hookInstalled(hooks, "SessionStart", "flow-breaker nudge") {
		t.Fatal("SessionStart hook not installed")
	}
	if hookInstalled(hooks, "SessionStart", "--quiet") {
		t.Fatal("SessionStart hook must be LOUD (no --quiet)")
	}
	if !hookInstalled(hooks, "UserPromptSubmit", "flow-breaker nudge --quiet") {
		t.Fatal("UserPromptSubmit quiet hook not installed")
	}
}

// TestPatchSettingsIdempotent verifies a second run adds nothing.
func TestPatchSettingsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	patchSettings(path)
	patchSettings(path)
	s := readSettings(t, path)

	if n := countHooks(t, s, "SessionStart"); n != 1 {
		t.Fatalf("SessionStart hooks = %d, want 1 (no duplicate)", n)
	}
	if n := countHooks(t, s, "UserPromptSubmit"); n != 1 {
		t.Fatalf("UserPromptSubmit hooks = %d, want 1 (no duplicate)", n)
	}
	// allow has exactly the 8 entries, no dupes.
	if got := len(permStrings(t, s, "allow")); got != 8 {
		t.Fatalf("allow len = %d, want 8 (no duplicates)", got)
	}
}

// TestPatchSettingsPreservesUserEntries verifies the merge does not clobber a
// user's pre-existing permission entries.
func TestPatchSettingsPreservesUserEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := map[string]interface{}{
		"permissions": map[string]interface{}{
			"allow": []interface{}{"Bash(git status:*)"},
		},
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	patchSettings(path)
	s := readSettings(t, path)
	allow := permStrings(t, s, "allow")
	if !contains(allow, "Bash(git status:*)") {
		t.Fatalf("user's own allow entry was clobbered; got %v", allow)
	}
	if !contains(allow, "Bash(flow-breaker nudge:*)") {
		t.Fatalf("flow-breaker perm not merged; got %v", allow)
	}
}

// TestPatchClaudeMDSurfaceNotAct verifies the CLAUDE.md section says surface,
// not act, and drops the old 30-min/loop guidance.
func TestPatchClaudeMDSurfaceNotAct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	patchClaudeMD(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "## flow-breaker") {
		t.Fatal("missing flow-breaker section")
	}
	if !strings.Contains(content, "UserPromptSubmit") {
		t.Fatal("section should mention the UserPromptSubmit per-turn hook")
	}
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "never act") && !strings.Contains(lower, "not act") {
		t.Fatal("section must instruct to SURFACE, never act on the nudge")
	}
	if strings.Contains(content, "30 min") || strings.Contains(content, "/loop") {
		t.Fatal("old 30-minute / /loop guidance must be removed")
	}

	// Idempotent: second run does not duplicate the section.
	patchClaudeMD(path)
	again, _ := os.ReadFile(path)
	if strings.Count(string(again), "## flow-breaker") != 1 {
		t.Fatal("CLAUDE.md section duplicated on re-run")
	}
}
