package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkContextReadingClosedSources(t *testing.T) {
	for _, source := range []string{
		`{"kind":"repository_file","project_id":"project-a","path":"src/file.go","commit_oid":"` + strings.Repeat("a", 40) + `"}`,
		`{"kind":"knowledge","source_id":"project-a","law_id":"law-a","content_hash":"sha256:` + strings.Repeat("b", 64) + `"}`,
	} {
		raw := `{"domain_id":"domain-a","reason":"Required reading","source":` + source + `}`
		var reading WorkContextReading
		if err := json.Unmarshal([]byte(raw), &reading); err != nil {
			t.Fatal(err)
		}
		if err := validateWorkContextReading(reading); err != nil {
			t.Fatal(err)
		}
		for _, extra := range []string{`,"selector":{"path":"src"}`, `,"unknown":null`, `,"kind":"knowledge"`} {
			invalid := strings.TrimSuffix(source, "}") + extra + "}"
			if err := json.Unmarshal([]byte(`{"domain_id":"domain-a","reason":"Required reading","source":`+invalid+`}`), &reading); err == nil {
				t.Fatalf("closed reading source accepted %s", invalid)
			}
		}
	}
	var reading WorkContextReading
	if err := json.Unmarshal([]byte(`{"domain_id":"domain-a","reason":"Required reading","source":{"kind":"domain_shape","source_id":"project-a","selector":{"path":"src"}}}`), &reading); err == nil {
		t.Fatal("retired reading source accepted")
	}
}
