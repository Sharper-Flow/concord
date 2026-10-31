package store

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func adapterNativeRecipeControl() OracleControl {
	return OracleControl{
		Argv:    []string{"check", "adapter-move", "adapter-publication", "adapter-transport"},
		Cwd:     "adapter/opencode",
		CaseIDs: []string{"case:adapter-native"},
	}
}

func TestAdapterNativeRecipeSelector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".concord", "oracles", "adapter", "native-acceptance.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := decodeNativeTargetManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	control := adapterNativeRecipeControl()
	witnesses, err := nativeTargetSelector(manifest, control)
	if err != nil || !slices.Equal(witnesses, control.Argv[1:]) {
		t.Fatalf("adapter recipe control does not name its exact closed selector: %v, %v", witnesses, err)
	}
}

func TestAdapterNativeRecipeGenerated(t *testing.T) {
	root := filepath.Join("..", "..")
	cmd := exec.Command("python3", "scripts/generate-adapter-oracle.py", "--check")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("adapter recipe generation: %s: %v", out, err)
	}
}

func TestNativeHelperRecipeSelectors(t *testing.T) {
	for _, tt := range []struct {
		name, materializer, cwd, caseID string
		witnesses                       []string
		checks                          [][]string
	}{
		{
			name: "adapter", materializer: "bun", cwd: "adapter/opencode", caseID: "case:adapter-native-helper",
			witnesses: []string{"adapter-native-helper"},
			checks:    [][]string{{"python3", "acceptance.py", "session-scope.test.ts", "primary-role.test.ts", "role-publication.test.ts"}},
		},
		{
			name: "agents", materializer: "none", cwd: ".", caseID: "case:primary-native-helper",
			witnesses: []string{"primary-helper-installer-collision", "primary-helper-installer-inventory", "primary-profile-parity", "primary-profile-tests"},
			checks: [][]string{
				{"python3", "scripts/test-installer.py", "InstallerTests.test_native_researcher_upgrade_preserves_an_unmanaged_definition"},
				{"python3", "scripts/test-installer.py", "InstallerTests.test_agent_files_equal_every_generated_definition"},
				{"python3", "scripts/generate-agent-profiles.py", "--check"},
				{"python3", "scripts/test-generate-agent-profiles.py"},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", ".concord", "oracles", tt.name, "native-helper.v1.json"))
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := decodeNativeTargetManifest(raw)
			if err != nil {
				t.Fatal(err)
			}
			control := OracleControl{Argv: append([]string{"check"}, tt.witnesses...), Cwd: tt.cwd, CaseIDs: []string{tt.caseID}}
			if err := validateNativeTargetManifest(manifest, control); err != nil {
				t.Fatal(err)
			}
			witnesses, err := nativeTargetSelector(manifest, control)
			if err != nil || !slices.Equal(witnesses, tt.witnesses) {
				t.Fatalf("helper recipe control does not name its exact closed selector: %v, %v", witnesses, err)
			}
			if manifest.Materializer != tt.materializer || manifest.Runner != "check" || len(manifest.Checks) != len(tt.checks) {
				t.Fatalf("helper recipe changed its family or check inventory: %+v", manifest)
			}
			for i, witness := range witnesses {
				check, err := nativeTargetCheckFor(manifest, witness)
				if err != nil || !slices.Equal(check.Argv, tt.checks[i]) {
					t.Fatalf("helper witness %s changed its exact command: %v, %v", witness, check.Argv, err)
				}
			}
		})
	}
}

func TestAdapterNativeRecipeRecordsReady(t *testing.T) {
	ctx := context.Background()
	const recipePath = ".concord/oracles/adapter/native-acceptance.v1.json"
	repo, head := storeOracleClone(t)
	manifestFile, raw, err := nativeGitFile(ctx, ExecGitRunner{}, repo, head, recipePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := decodeNativeTargetManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", "target-branch")
	const work = "adapter-native-recipe"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeTargetFixture{
		s: seed.store, work: work, repo: repo, head: head,
		root: "adapter/opencode", recipePath: recipePath,
		controlArgv: adapterNativeRecipeControl().Argv,
	}
	f.seedTargetAuthority(t)
	f.bundle.Owner.Mechanism = OracleMechanism{ProjectID: "project", Path: "adapter/opencode/concord-plugin.ts", EntryPoint: "ConcordPlugin"}
	f.bundle.Control.CaseIDs = []string{"case:adapter-native"}
	f.bundle.Cases[0].CaseID = "case:adapter-native"
	if err := validateNativeTargetManifest(manifest, f.bundle.Control); err != nil {
		t.Fatal(err)
	}
	// The committed recipe pins the preparation host. Other hosts still check
	// generation and manifest shape, but cannot claim this exact runtime.
	for tool, want := range map[string]string{"python3": "Python " + manifest.Pin.Python, "bun": manifest.Pin.Bun} {
		out, err := exec.Command(tool, "--version").Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			t.Skipf("pinned adapter runtime unavailable: %s requires %s, got %q (%v)", tool, want, out, err)
		}
	}
	f.prepareWithCurrentBundle(t, "adapter-recipe")
	p := f.prepare.Oracle
	if f.launches != 0 || p.ManifestBlob != manifestFile.BlobOID || p.ManifestDigest != manifestFile.SHA256 || p.RecipeSource != f.bundle.Control.RecipeSource || p.SubjectCommit != head || !p.StreamsComplete {
		t.Fatalf("preparation lost its committed recipe or ran acceptance: %+v launches=%d", p, f.launches)
	}
	if p.TargetMaterializer != "bun" || !slices.Equal(p.SelectedWitnessIDs, f.controlArgv[1:]) {
		t.Fatalf("preparation lost its runtime or exact witnesses: %+v", p)
	}
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err := f.s.VerifyWorktree(ctx, f.request("adapter-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 || f.launches != len(p.SelectedWitnessIDs) {
		t.Fatalf("execute pinned adapter checks: oracle=%+v, %v launches=%d output=%s", r.Oracle, err, f.launches, r.Output)
	}
	assertNativeTargetReceipt(t, r, "bun", "check", f.root)
	t.Logf("recipe_commit=%s recipe_blob=%s native_preparation=%s qualification=%s native_execution=%s", head, manifestFile.BlobOID, f.prepare.OperationRef, p.Qualification, r.OperationRef)
}
