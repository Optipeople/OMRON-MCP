package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rjboer/omron-mcp/internal/sysmac"
)

const nativeVariables = "[SLWD version=1.0]\r\n_EN=Variables\r\n+GN=VAR\tGVT=DefaultGroup\r\n" +
	"++D=ARRAY[0..24] OF sTagData\tN=Tags\tG=VAR\r\n" +
	"++D=sWillCfg\tN=Will\tIV=(WillTopic := '', WillMsg := 'x y')\tG=VAR\tCom=Will config\r\n" +
	"+GN=VAR_INPUT\tGA=InputOutput\tGVT=EdgeInputOutputGroup\r\n" +
	"++D=BOOL\tN=Enable\tIV=False\tOrd=0\tG=VAR_INPUT"

func TestSLWDToolNativeReadAndPartialWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "variables.xml")
	if err := os.WriteFile(path, []byte(nativeVariables), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := callArgs(t, "sysmac_slwd_read", map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	variables := read["structuredContent"].(map[string]any)["variables"].([]sysmac.SLWDVariable)
	if variables[0].Type != "ARRAY[0..24] OF sTagData" || variables[1].InitialValue != "(WillTopic := '', WillMsg := 'x y')" {
		t.Fatalf("read = %#v", variables)
	}
	// Writing back exactly what was read must not change a byte.
	if _, err := callArgs(t, "sysmac_slwd_write", map[string]any{"path": path, "variables": variables, "confirm_direct_write": true}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != nativeVariables {
		t.Fatalf("no-op write changed file:\n%q", data)
	}
	// Partial records only touch named fields.
	if _, err := callArgs(t, "sysmac_slwd_write", map[string]any{
		"path": path,
		"variables": []map[string]any{
			{"name": "Will", "comment": ""},
			{"name": "Enable", "rename_to": "Execute"},
			{"name": "Count", "type": "UDINT", "group": "VAR"},
		},
		"confirm_direct_write": true,
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := strings.Replace(nativeVariables, "\tCom=Will config", "", 1)
	want = strings.Replace(want, "\tG=VAR\r\n+GN=VAR_INPUT", "\tG=VAR\r\n++D=UDINT\tN=Count\tG=VAR\r\n+GN=VAR_INPUT", 1)
	want = strings.Replace(want, "N=Enable", "N=Execute", 1)
	if string(data) != want {
		t.Fatalf("partial write:\n%q\nwant\n%q", data, want)
	}
}
