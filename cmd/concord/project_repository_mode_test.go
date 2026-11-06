package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProjectRepositoryModeCLI(t *testing.T) {
	repo := initLocatorRepo(t)
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "concord.db"))
	runCLIJSON(t, []string{"product-create"}, map[string]any{"product_id": "p", "display_name": "P", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only", "project_id": "p1", "project_display_name": "P1", "role": "primary"})
	runCLIJSON(t, []string{"project-locator-add"}, map[string]any{"project_id": "p1", "locator_id": "l", "kind": "canonical_path", "value": repo, "expected_version": 1})
	resolve := func(want string) {
		t.Helper()
		var got struct {
			RepositoryMode string `json:"repository_mode"`
		}
		if err := json.Unmarshal(runCLIJSON(t, []string{"project-resolve"}, map[string]any{"directory": repo}), &got); err != nil || got.RepositoryMode != want {
			t.Fatalf("resolve mode=%q want=%q err=%v", got.RepositoryMode, want, err)
		}
	}
	resolve("off")
	for _, tc := range []struct {
		mode    string
		version int
	}{{"on", 2}, {"off", 3}} {
		raw := runCLIJSON(t, []string{"project", "repository-mode-update"}, map[string]any{"project_id": "p1", "repository_mode": tc.mode, "expected_version": tc.version})
		var got operatorResponse
		if err := json.Unmarshal(raw, &got); err != nil || !got.OK || got.ProjectID != "p1" || len(got.EventIDs) != 1 || len(got.ChangedRefs) != 1 || got.ChangedRefs[0].EntityKind != "project" || got.ChangedRefs[0].ID != "p1" || got.ChangedRefs[0].Version != strconv.Itoa(tc.version+1) {
			t.Fatalf("update=%s err=%v", raw, err)
		}
		resolve(tc.mode)
	}
	for _, raw := range []string{`{"project_id":"p1","repository_mode":"bad","expected_version":4}`, `{"project_id":"p1","repository_mode":"on","expected_version":2}`, `{"project_id":"missing","repository_mode":"on","expected_version":1}`} {
		var out, errOut bytes.Buffer
		if code := runWithInput([]string{"project-repository-mode-update"}, strings.NewReader(raw), &out, &errOut); code != 1 || out.Len() != 0 || errOut.Len() == 0 {
			t.Errorf("request=%s code=%d stdout=%s stderr=%s", raw, code, out.String(), errOut.String())
		}
	}
}
