package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bashPerms loads and unmarshals opencode.json, returning permission.bash as a
// map[string]string.
func bashPerms(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal opencode.json: %v", err)
	}
	perm, _ := cfg["permission"].(map[string]interface{})
	bash, _ := perm["bash"].(map[string]interface{})
	out := map[string]string{}
	for k, v := range bash {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// TestPatchOpencodeConfigFailClosed verifies the fail-closed permission map:
// catch-all ask, reads+capture+add allow, done/snooze ask, clear deny, no
// global "*".
func TestPatchOpencodeConfigFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	patchOpencodeConfig(path)
	bash := bashPerms(t, path)

	if bash["flow-breaker *"] != "ask" {
		t.Fatalf("catch-all should be ask; got %q", bash["flow-breaker *"])
	}
	for _, k := range []string{
		"flow-breaker nudge*", "flow-breaker status*", "flow-breaker list*",
		"flow-breaker ls*", "flow-breaker archive*", "flow-breaker cal-list*",
		"flow-breaker cal-feeds*", "flow-breaker capture*", "flow-breaker add*",
	} {
		if bash[k] != "allow" {
			t.Fatalf("%q should be allow; got %q", k, bash[k])
		}
	}
	if bash["flow-breaker done*"] != "ask" || bash["flow-breaker snooze*"] != "ask" {
		t.Fatalf("done/snooze should be ask; got done=%q snooze=%q", bash["flow-breaker done*"], bash["flow-breaker snooze*"])
	}
	if bash["flow-breaker clear*"] != "deny" {
		t.Fatalf("clear should be deny; got %q", bash["flow-breaker clear*"])
	}
	// No global "*" that would force every bash command to prompt.
	if _, ok := bash["*"]; ok {
		t.Fatal("must NOT contain a global \"*\" permission")
	}
}

// TestPatchOpencodeConfigCatchAllSortsFirst verifies that in the marshaled JSON
// the catch-all "flow-breaker *" key sorts BEFORE every specific
// "flow-breaker <letter>*" key, so opencode's last-match-wins gives the specific
// rules priority over the catch-all without any ordered-map work.
func TestPatchOpencodeConfigCatchAllSortsFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	patchOpencodeConfig(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)

	catchAll := strings.Index(raw, `"flow-breaker *"`)
	if catchAll == -1 {
		t.Fatal("catch-all key not found in output")
	}
	for _, k := range []string{
		"flow-breaker nudge*", "flow-breaker status*", "flow-breaker list*",
		"flow-breaker ls*", "flow-breaker archive*", "flow-breaker cal-list*",
		"flow-breaker cal-feeds*", "flow-breaker capture*", "flow-breaker add*",
		"flow-breaker done*", "flow-breaker snooze*", "flow-breaker clear*",
	} {
		idx := strings.Index(raw, `"`+k+`"`)
		if idx == -1 {
			t.Fatalf("key %q not found in output", k)
		}
		if catchAll >= idx {
			t.Fatalf("catch-all (idx %d) must sort before %q (idx %d) for last-match-wins", catchAll, k, idx)
		}
	}
}

// TestPatchOpencodeConfigIdempotent verifies a second run changes nothing.
func TestPatchOpencodeConfigIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	patchOpencodeConfig(path)
	first, _ := os.ReadFile(path)
	patchOpencodeConfig(path)
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("second run changed the file (not idempotent):\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	// Exactly the 13 managed keys, no duplicates/extras from re-run.
	if got := len(bashPerms(t, path)); got != 13 {
		t.Fatalf("permission.bash has %d keys, want 13", got)
	}
}

// TestPatchOpencodeConfigPreservesUserEntries verifies a user's unrelated
// permission.bash entry survives the merge.
func TestPatchOpencodeConfigPreservesUserEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	seed := map[string]interface{}{
		"$schema": "https://opencode.ai/config.json",
		"permission": map[string]interface{}{
			"bash": map[string]interface{}{
				"git status*": "allow",
			},
		},
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	patchOpencodeConfig(path)
	bash := bashPerms(t, path)
	if bash["git status*"] != "allow" {
		t.Fatalf("user's own bash entry was clobbered; got %v", bash)
	}
	if bash["flow-breaker *"] != "ask" || bash["flow-breaker clear*"] != "deny" {
		t.Fatalf("flow-breaker perms not merged alongside user entry; got %v", bash)
	}
}

// TestPatchOpencodeConfigUpgradesLoneNudge verifies the old install (a single
// "flow-breaker nudge*": "allow") upgrades to the full fail-closed map.
func TestPatchOpencodeConfigUpgradesLoneNudge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	seed := map[string]interface{}{
		"permission": map[string]interface{}{
			"bash": map[string]interface{}{
				"flow-breaker nudge*": "allow",
			},
		},
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	patchOpencodeConfig(path)
	bash := bashPerms(t, path)
	if bash["flow-breaker *"] != "ask" || bash["flow-breaker clear*"] != "deny" {
		t.Fatalf("lone-nudge config not upgraded to full map; got %v", bash)
	}
}

// TestWriteOpencodePluginContract asserts the plugin's shape: loud init nudge,
// per-turn quiet nudge at 800ms, silent on empty, and NO 30-min setInterval.
func TestWriteOpencodePluginContract(t *testing.T) {
	dir := t.TempDir()
	writeOpencodePlugin(dir)
	data, err := os.ReadFile(filepath.Join(dir, "flow-breaker.js"))
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	js := string(data)

	if strings.Contains(js, "setInterval") {
		t.Fatal("plugin must NOT use a 30-min setInterval")
	}
	if !strings.Contains(js, "experimental.chat.system.transform") {
		t.Fatal("plugin must use the experimental.chat.system.transform hook")
	}
	if !strings.Contains(js, "flow-breaker nudge --quiet") {
		t.Fatal("plugin must run the quiet nudge per turn")
	}
	if !strings.Contains(js, "timeout(800)") {
		t.Fatal("per-turn quiet nudge must have an 800ms timeout")
	}
	if !strings.Contains(js, "output.system.push") {
		t.Fatal("plugin must push into output.system")
	}
	// A loud init nudge (bare `flow-breaker nudge`, no --quiet) is injected once.
	if !strings.Contains(js, "`flow-breaker nudge`") {
		t.Fatal("plugin must inject a loud init nudge")
	}
}

// TestWriteOpencodePluginMigratesStale verifies a stale plugin (old setInterval
// version) is rewritten in place, and an identical file is a no-op.
func TestWriteOpencodePluginMigratesStale(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "flow-breaker.js")
	if err := os.WriteFile(pluginPath, []byte("// stale\nsetInterval(() => {}, 1000)\n"), 0644); err != nil {
		t.Fatal(err)
	}
	writeOpencodePlugin(dir)
	got, _ := os.ReadFile(pluginPath)
	if strings.Contains(string(got), "setInterval") {
		t.Fatal("stale setInterval plugin was not migrated")
	}
	if string(got) != opencodePluginJS {
		t.Fatal("plugin not rewritten to current source")
	}
	// Second run is a no-op (bytes unchanged).
	writeOpencodePlugin(dir)
	again, _ := os.ReadFile(pluginPath)
	if string(again) != string(got) {
		t.Fatal("re-run changed an up-to-date plugin (not idempotent)")
	}
}

// TestPatchAgentsMDSurfaceNotAct verifies the AGENTS.md section is surface-not-act
// and carries no 30-min/loop guidance.
func TestPatchAgentsMDSurfaceNotAct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	patchAgentsMD(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "## flow-breaker") {
		t.Fatal("missing flow-breaker section")
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
	patchAgentsMD(path)
	again, _ := os.ReadFile(path)
	if strings.Count(string(again), "## flow-breaker") != 1 {
		t.Fatal("AGENTS.md section duplicated on re-run")
	}
}

// TestPatchAgentsMDMigratesOldSection seeds the OLD unmarked section (with the
// removed 30-min guidance) and verifies install migrates it in place, preserving
// the user's other content.
func TestPatchAgentsMDMigratesOldSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	old := "# Agent notes\n\nSome preamble.\n\n" +
		"## flow-breaker\n\n" +
		"At the start of every conversation, run `flow-breaker nudge` and surface any alerts.\n\n" +
		"During long sessions, run `flow-breaker nudge` every 30 minutes to check for new overdue tasks.\n\n" +
		"## other section\n\nKeep me.\n"
	if err := os.WriteFile(path, []byte(old), 0644); err != nil {
		t.Fatal(err)
	}

	patchAgentsMD(path)
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
	patchAgentsMD(path)
	again, _ := os.ReadFile(path)
	if string(again) != content {
		t.Fatalf("second run changed the file (not idempotent)")
	}
}
