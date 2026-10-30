package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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
		"Bash(flow-breaker list:*)",
		"Bash(flow-breaker ls:*)",
		"Bash(flow-breaker archive:*)",
		"Bash(flow-breaker cal-list:*)",
		"Bash(flow-breaker cal-feeds:*)",
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
	// Every granted name must be a subcommand main.go actually dispatches — no
	// phantom perms (next/overdue/alarm/calendar were never wired).
	dispatched := dispatchedSubcommands(t)
	for _, list := range [][]string{allow, permStrings(t, s, "ask"), permStrings(t, s, "deny")} {
		for _, p := range list {
			cmd := strings.TrimSuffix(strings.TrimPrefix(p, "Bash(flow-breaker "), ":*)")
			if !dispatched[cmd] {
				t.Fatalf("permission %q names %q, which main.go does not dispatch", p, cmd)
			}
		}
	}
	// The marquee capture path (and snooze) must actually dispatch now.
	if !dispatched["capture"] || !dispatched["snooze"] {
		t.Fatalf("main.go must dispatch capture and snooze; dispatched=%v", dispatched)
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
	// allow has exactly the 9 granted entries, no dupes.
	if got := len(permStrings(t, s, "allow")); got != 9 {
		t.Fatalf("allow len = %d, want 9 (no duplicates)", got)
	}
}

// dispatchedSubcommands parses the case labels in main.go so tests can assert
// permissions name only real, wired subcommands.
func dispatchedSubcommands(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	caseRe := regexp.MustCompile(`(?m)^\s*case\s+(.+):`)
	tokRe := regexp.MustCompile(`"([^"]+)"`)
	set := map[string]bool{}
	for _, line := range caseRe.FindAllStringSubmatch(string(data), -1) {
		for _, tok := range tokRe.FindAllStringSubmatch(line[1], -1) {
			set[tok[1]] = true
		}
	}
	return set
}

// TestPatchSettingsPrunesStaleNudgePerm verifies the pre-v2 no-colon pattern is
// removed on upgrade, leaving only the granular Bash(flow-breaker nudge:*).
func TestPatchSettingsPrunesStaleNudgePerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := map[string]interface{}{
		"permissions": map[string]interface{}{
			"allow": []interface{}{"Bash(flow-breaker nudge*)"},
		},
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	patchSettings(path)
	allow := permStrings(t, readSettings(t, path), "allow")
	if contains(allow, "Bash(flow-breaker nudge*)") {
		t.Fatalf("stale no-colon perm not pruned; got %v", allow)
	}
	if !contains(allow, "Bash(flow-breaker nudge:*)") {
		t.Fatalf("granular nudge perm missing; got %v", allow)
	}
}

// TestPatchClaudeMDMigratesOldSection seeds an OLD unmarked section (with the
// removed 30-min/loop guidance) and verifies install migrates it in place: the
// stale text is gone, the surface-not-act text is present, and a second run is a
// no-op.
func TestPatchClaudeMDMigratesOldSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	old := "# My notes\n\nSome preamble.\n\n" +
		"## flow-breaker\n\n" +
		"At the start of every conversation, run `flow-breaker nudge`.\n\n" +
		"During long sessions, run it every 30 minutes. Use `/loop 30m flow-breaker nudge` to automate this.\n\n" +
		"## other section\n\nKeep me.\n"
	if err := os.WriteFile(path, []byte(old), 0644); err != nil {
		t.Fatal(err)
	}

	patchClaudeMD(path)
	got, _ := os.ReadFile(path)
	content := string(got)

	if strings.Contains(content, "every 30 minutes") || strings.Contains(content, "/loop") {
		t.Fatalf("old 30-min/loop guidance not migrated away:\n%s", content)
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
	if !strings.Contains(content, fbBeginMarker) || !strings.Contains(content, fbEndMarker) {
		t.Fatalf("migrated section not wrapped in markers:\n%s", content)
	}

	// Second run is a no-op.
	patchClaudeMD(path)
	again, _ := os.ReadFile(path)
	if string(again) != content {
		t.Fatalf("second run changed the file (not idempotent):\n%s", string(again))
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
