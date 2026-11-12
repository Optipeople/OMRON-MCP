package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProgramCompareToolIdenticalProjects(t *testing.T) {
	dir := t.TempDir()
	before, after := filepath.Join(dir, "before"), filepath.Join(dir, "after")
	for _, project := range []string{before, after} {
		writeFiles(t, project, map[string]string{"sol.oem": libToolProject, "refs.xml": "<data />", "old.xml": "<data />"})
	}
	output := filepath.Join(dir, "compare.md")
	result, err := callArgs(t, "sysmac_program_compare", map[string]any{"old_path": before, "new_path": after, "output_path": output})
	if err != nil {
		t.Fatal(err)
	}
	content := result["structuredContent"].(map[string]any)
	raw, _ := json.Marshal(content["summary"])
	var summary struct{ Missing, Unexplained int }
	if err := json.Unmarshal(raw, &summary); err != nil || summary.Missing != 0 || summary.Unexplained != 0 {
		t.Fatalf("summary = %v", summary)
	}
	markdown, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(markdown), "Ingen manglende funktioner og ingen uforklarede") {
		t.Fatalf("markdown: %v\n%s", err, markdown)
	}
	if _, err := callArgs(t, "sysmac_program_compare", map[string]any{"old_path": before, "new_path": after, "output_path": filepath.Join(dir, "x.txt")}); err == nil {
		t.Fatal("non-.md output accepted")
	}
}
