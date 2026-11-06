package sysmac

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRealProjectSLWDRoundTrip checks every SLWD payload below the
// directories listed in OMRON_MCP_REALDATA_DIRS (path list) for a
// byte-identical parse/marshal round trip. Real projects are never checked in.
func TestRealProjectSLWDRoundTrip(t *testing.T) {
	dirs := filepath.SplitList(os.Getenv("OMRON_MCP_REALDATA_DIRS"))
	if len(dirs) == 0 || dirs[0] == "" {
		t.Skip("OMRON_MCP_REALDATA_DIRS not set")
	}
	checked := 0
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(path) != ".xml" {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.HasPrefix(bytes.TrimPrefix(data, []byte(utf8BOM)), []byte("[SLWD")) {
				return nil
			}
			doc, err := ParseSLWDDocument(data)
			if err != nil {
				t.Errorf("%s: %v", path, err)
				return nil
			}
			out, _ := MarshalSLWDDocument(doc)
			if !bytes.Equal(out, data) {
				t.Errorf("%s: round trip changed bytes", path)
			}
			for _, variable := range doc.Variables() {
				if variable.Type == "" || variable.Name == "" {
					t.Errorf("%s: incomplete variable %q", path, variable.Name)
				}
			}
			checked++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("checked %d SLWD payloads", checked)
}
