package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func attributionCLIStore(t *testing.T) (string, string) {
	t.Helper()
	path, root := cliStoreRoot(t)
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := pm1fixture.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := pm1fixture.Seed(context.Background(), s, corpus); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path, root
}

func attributionCall(t *testing.T, input any) (int, []byte, string) {
	t.Helper()
	b, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"relation-attribution"}, bytes.NewReader(b), &out, &errOut)
	return code, out.Bytes(), errOut.String()
}

func TestAttributionCLIInput(t *testing.T) {
	for _, input := range []string{``, `{}`, `{"action":"bad"}`, `{"action":"inspect","cutoff":"bad"}`, `{"action":"inspect","cutoff":"2030-01-01T00:00:00Z","unknown":true}`, `{"action":"inspect","cutoff":"2030-01-01T00:00:00Z"} {}`, `{"action":"clear","backup_path":"/tmp/test","idempotency_key":" "}`} {
		var out, errOut bytes.Buffer
		if code := runWithInput([]string{"relation-attribution"}, strings.NewReader(input), &out, &errOut); code == 0 {
			t.Fatalf("accepted %s", input)
		}
	}
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"relation-attribution", "--help"}, strings.NewReader("not JSON"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "inspect") {
		t.Fatalf("help: %d %s %s", code, out.String(), errOut.String())
	}
	if code := runWithInput([]string{"relation-attribution", "extra"}, strings.NewReader(`{}`), &out, &errOut); code == 0 {
		t.Fatal("accepted positional argument")
	}
}

func TestAttributionCLILifecycle(t *testing.T) {
	_, root := attributionCLIStore(t)
	code, data, diag := attributionCall(t, attributionInput{Action: "inspect", Cutoff: "2090-01-01T00:00:00Z"})
	if code != 0 {
		t.Fatalf("inspect: %s", diag)
	}
	var inspected struct {
		Snapshot string                    `json:"snapshot"`
		Hold     string                    `json:"hold"`
		Manifest store.AttributionManifest `json:"manifest"`
	}
	if err := json.Unmarshal(data, &inspected); err != nil {
		t.Fatal(err)
	}
	if len(inspected.Manifest.Candidates) == 0 {
		t.Fatal("fixture has no eligible attribution")
	}
	if _, err := os.Stat(inspected.Hold); err != nil {
		t.Fatal(err)
	}
	offlineInspect := attributionInput{Action: "inspect", Cutoff: inspected.Manifest.Cutoff, BackupPath: inspected.Snapshot}
	if code, _, diag := attributionCall(t, offlineInspect); code != 0 {
		t.Fatalf("offline inspect: %s", diag)
	}
	input := attributionInput{Action: "clear", BackupPath: inspected.Snapshot, Manifest: inspected.Manifest, IdempotencyKey: "clear-test"}
	bad := input
	bad.Manifest.Candidates = append([]store.AttributionCandidate(nil), input.Manifest.Candidates...)
	bad.Manifest.Candidates[0].Fingerprint = "different"
	if code, _, _ := attributionCall(t, bad); code == 0 {
		t.Fatal("accepted mismatched manifest")
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
		t.Fatalf("mismatch fence: %+v %v", fence, err)
	}
	code, data, diag = attributionCall(t, input)
	if code != 0 {
		t.Fatalf("clear: %s", diag)
	}
	var cleared struct {
		Result store.AttributionResult `json:"result"`
	}
	if err := json.Unmarshal(data, &cleared); err != nil {
		t.Fatal(err)
	}
	if len(cleared.Result.Treated) != len(inspected.Manifest.Candidates) || !cleared.Result.GraphPreserved || !cleared.Result.ReceiptsPreserved {
		t.Fatalf("clear result: %+v", cleared)
	}
	if err := os.Rename(inspected.Snapshot, inspected.Snapshot+".offline"); err != nil {
		t.Fatal(err)
	}
	code, data, diag = attributionCall(t, input)
	if code != 0 {
		t.Fatalf("offline replay: %s", diag)
	}
	var replay struct {
		Result store.AttributionResult `json:"result"`
	}
	if err := json.Unmarshal(data, &replay); err != nil || !reflect.DeepEqual(replay.Result, cleared.Result) {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if err := os.Rename(inspected.Snapshot+".offline", inspected.Snapshot); err != nil {
		t.Fatal(err)
	}
	conflict := input
	conflict.Manifest.GraphDigest = "different"
	if code, _, _ := attributionCall(t, conflict); code == 0 {
		t.Fatal("replay accepted changed request")
	}
	input.Action = "verify"
	code, data, diag = attributionCall(t, input)
	if code != 0 {
		t.Fatalf("verify: %s", diag)
	}
	var verified struct {
		Result store.AttributionResult `json:"result"`
	}
	if err := json.Unmarshal(data, &verified); err != nil || !reflect.DeepEqual(verified.Result.Treated, cleared.Result.Treated) || !verified.Result.GraphPreserved || !verified.Result.ReceiptsPreserved {
		t.Fatalf("verify result: %+v %v", verified, err)
	}
	input.Action, input.ClearKey, input.IdempotencyKey = "undo", "clear-test", "undo-test"
	code, data, diag = attributionCall(t, input)
	if code != 0 {
		t.Fatalf("undo: %s", diag)
	}
	var undone struct {
		SafetySnapshot string                  `json:"safety_snapshot"`
		SafetyHold     string                  `json:"safety_hold"`
		Result         store.AttributionResult `json:"result"`
	}
	if err := json.Unmarshal(data, &undone); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(undone.Result.Restored, cleared.Result.Treated) {
		t.Fatalf("undo: %+v", undone)
	}
	if _, err := os.Stat(undone.SafetyHold); err != nil {
		t.Fatal(err)
	}
	if undone.SafetySnapshot == inspected.Snapshot {
		t.Fatal("undo replaced original binding")
	}
	code, data, diag = attributionCall(t, input)
	if code != 0 {
		t.Fatalf("undo replay: %s", diag)
	}
	var undoReplay struct {
		SafetySnapshot string                  `json:"safety_snapshot"`
		Result         store.AttributionResult `json:"result"`
	}
	if err := json.Unmarshal(data, &undoReplay); err != nil || undoReplay.SafetySnapshot != "" || !reflect.DeepEqual(undoReplay.Result, undone.Result) {
		t.Fatalf("undo replay: %+v %v", undoReplay, err)
	}
}

func TestAttributionCLIRefusesForeignFenceAndLiveSession(t *testing.T) {
	for _, kind := range []string{"fence", "session"} {
		t.Run(kind, func(t *testing.T) {
			_, root := attributionCLIStore(t)
			if kind == "fence" {
				if _, err := hostlease.EnsureFence(root, hostlease.Fence{FenceID: "foreign", Operation: "foreign"}); err != nil {
					t.Fatal(err)
				}
			} else {
				start, err := hostlease.ProcessStart(os.Getpid())
				if err != nil {
					t.Fatal(err)
				}
				releaseRoot, binary, err := selfRelease()
				if err != nil {
					t.Fatal(err)
				}
				if err := hostlease.Write(root, hostlease.Lease{PID: os.Getpid(), PidStart: start, ReleaseRoot: releaseRoot, CoreBinary: binary, SchemaVersion: store.CurrentSchemaVersion(), FenceProtocol: hostlease.CurrentFenceProtocol}); err != nil {
					t.Fatal(err)
				}
			}
			if code, _, diag := attributionCall(t, attributionInput{Action: "inspect", Cutoff: "2090-01-01T00:00:00Z"}); code == 0 {
				t.Fatalf("accepted %s: %s", kind, diag)
			}
			fence, err := hostlease.ReadFence(root)
			if err != nil || (kind == "fence" && (fence == nil || fence.FenceID != "foreign")) || (kind == "session" && fence != nil) {
				t.Fatalf("fence: %+v %v", fence, err)
			}
		})
	}
}

func TestAttributionCLIUncertainEffectRetainsFence(t *testing.T) {
	_, root := attributionCLIStore(t)
	code, data, diag := attributionCall(t, attributionInput{Action: "inspect", Cutoff: "2090-01-01T00:00:00Z"})
	if code != 0 {
		t.Fatal(diag)
	}
	var inspected struct {
		Snapshot string                    `json:"snapshot"`
		Manifest store.AttributionManifest `json:"manifest"`
	}
	if err := json.Unmarshal(data, &inspected); err != nil {
		t.Fatal(err)
	}
	original := clearAttribution
	t.Cleanup(func() { clearAttribution = original })
	clearAttribution = func(*store.Store, context.Context, store.AttributionRequest) (store.AttributionResult, error) {
		return store.AttributionResult{}, fmt.Errorf("unclassified commit failure")
	}
	input := attributionInput{Action: "clear", BackupPath: inspected.Snapshot, Manifest: inspected.Manifest, IdempotencyKey: "clear-test"}
	if code, _, diag := attributionCall(t, input); code == 0 || !strings.Contains(diag, "fence retained: true") {
		t.Fatalf("failure: %d %s", code, diag)
	}
	fence, err := hostlease.ReadFence(root)
	if err != nil || fence == nil || fence.Operation != hostlease.FenceOperationRelationAttribution {
		t.Fatalf("fence: %+v %v", fence, err)
	}
}

func TestAttributionCLICurrentPopulationMismatchDoesNotWrite(t *testing.T) {
	path, root := attributionCLIStore(t)
	code, data, diag := attributionCall(t, attributionInput{Action: "inspect", Cutoff: "2090-01-01T00:00:00Z"})
	if code != 0 {
		t.Fatal(diag)
	}
	var inspected struct {
		Snapshot string                    `json:"snapshot"`
		Manifest store.AttributionManifest `json:"manifest"`
	}
	if err := json.Unmarshal(data, &inspected); err != nil {
		t.Fatal(err)
	}
	if len(inspected.Manifest.Candidates) == 0 {
		t.Fatal("fixture has no candidates")
	}
	s, err := store.OpenMaintenance(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	id := inspected.Manifest.Candidates[0].ID
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE relations SET reason='changed' WHERE id=?; DELETE FROM write_guard`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	input := attributionInput{Action: "clear", BackupPath: inspected.Snapshot, Manifest: inspected.Manifest, IdempotencyKey: "clear-test"}
	if code, _, diag := attributionCall(t, input); code == 0 || !strings.Contains(diag, "changed") {
		t.Fatalf("mismatch: %d %s", code, diag)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
		t.Fatalf("fence: %+v %v", fence, err)
	}
	s, err = store.OpenMaintenance(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var reason string
	if err := s.DatabaseForTesting().QueryRow(`SELECT reason FROM relations WHERE id=?`, id).Scan(&reason); err != nil || reason != "changed" {
		t.Fatalf("row changed: %q %v", reason, err)
	}
	_, found, err := s.LookupMutationIdempotency(t.Context(), store.MutationIdempotencyKey{PrincipalRef: "operator", Tool: "relation_attribution", OperationKind: "clear", IdempotencyKey: "clear-test"})
	if err != nil || found {
		t.Fatalf("unexpected retained result: %t %v", found, err)
	}
}
