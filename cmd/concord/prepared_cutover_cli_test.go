package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

func TestHostLeasesReadOnlyPreservesStaleAndDefaultPrunes(t *testing.T) {
	_, root := cliStoreRoot(t)
	if err := os.MkdirAll(hostlease.Directory(root), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(hostlease.Directory(root), "stale.json")
	if err := os.WriteFile(stale, []byte(`{"pid":0,"pid_start":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"read_only":true}`, `{}`} {
		var out, errOut bytes.Buffer
		if code := runHostLeasesCommand(nil, strings.NewReader(input), &out, &errOut); code != 0 {
			t.Fatalf("host-leases %s: %s", input, errOut.String())
		}
		_, err := os.Stat(stale)
		if input != `{}` && err != nil || input == `{}` && !os.IsNotExist(err) {
			t.Fatalf("host-leases %s stale file: %v", input, err)
		}
	}
}

func TestPreparedUpgradeRefusesEvenACurrentSchemaLeaseBeforeSnapshot(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	start, err := hostlease.ProcessStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	releaseRoot, binary, err := selfRelease()
	if err != nil {
		t.Fatal(err)
	}
	if err := hostlease.Write(root, hostlease.Lease{PID: os.Getpid(), PidStart: start,
		ReleaseRoot: releaseRoot, CoreBinary: binary, SchemaVersion: store.CurrentSchemaVersion(),
		FenceProtocol: hostlease.CurrentFenceProtocol}); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(hostlease.Directory(root), "0-stale.json")
	if err := os.WriteFile(stale, []byte(`{"pid":0,"pid_start":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runUpgradeStdin(t, `{"require_no_sessions":true}`)
	if code != 1 || !strings.Contains(errOut, "pid "+strconv.Itoa(os.Getpid())) || !strings.Contains(errOut, "requires no live sessions") {
		t.Fatalf("current-schema holder must refuse cutover: code=%d out=%s err=%s", code, out, errOut)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("strict refusal must preserve stale lease: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "backups")); !os.IsNotExist(err) {
		t.Fatalf("a refused cutover must take no snapshot: %v", err)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence != nil {
		t.Fatalf("a refused cutover must leave no new fence: %+v %v", fence, err)
	}
	plan, err := store.PlanUpgradeReadiness(t.Context(), path)
	if err != nil || plan.SchemaVersion != 110 {
		t.Fatalf("a refused cutover must apply nothing: %+v %v", plan, err)
	}
}

func TestPreparedCutoverMigratesSnapshotsActivatesAndRecovers(t *testing.T) {
	repository := installerRepositoryRoot(t)
	core := buildCoreFrom(t, repository, "v30.0.99")
	paths := newInteropPaths(t)
	if err := os.MkdirAll(paths.dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	pendingBreakingStore(t, paths.dbPath, true)
	driver := `import hashlib, importlib.util, json, os, pathlib, shlex, shutil, sqlite3, subprocess, sys, tarfile
sys.path.insert(0, str(pathlib.Path(sys.argv[1]) / "scripts"))
spec = importlib.util.spec_from_file_location("fixture", pathlib.Path(sys.argv[1]) / "scripts/test-installer.py")
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)
from types import SimpleNamespace
from unittest import mock
case = fixture.InstallerTests()
case.setUp()
try:
    root = pathlib.Path(sys.argv[2])
    shutil.copytree(case.root, root, dirs_exist_ok=True)
    case.root = root
    case.commands = root / "bin"
    case.artifacts = root / "published"
    case.env["PATH"] = str(case.commands) + os.pathsep + os.defpath
    case.env["CONCORD_DB_PATH"] = str(root / "must-not-be-opened.db")
    installed = case.install_release("v1.0.0")
    assert installed.returncode == 0, (installed.stdout, installed.stderr)
    version = "v30.0.99"
    case.make_release(version)
    source = root / ("source-" + version)
    binary = source / "bin/concord"
    shutil.copy2(sys.argv[3], binary)
    descriptor = json.loads(subprocess.check_output([str(binary), "--version", "--json"], env=case.env))
    (source / "adapter/opencode/generated-contracts.ts").write_text('export const manifestDigest = "' + descriptor["manifest_digest"] + '" as const\n')
    archive = case.artifacts / ("concord-" + version + ".tar.gz")
    with tarfile.open(archive, "w:gz") as bundle:
        for path in sorted(source.rglob("*")):
            bundle.add(path, path.relative_to(source))
    (case.artifacts / ("concord-" + version + ".sha256")).write_text(
        hashlib.sha256(binary.read_bytes()).hexdigest() + "  concord-" + version + "\n" +
        hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + archive.name + "\n")
    prepared = case.run_installer("install", "--version", version, "--artifact-dir", str(case.artifacts))
    assert prepared.returncode == 0, (prepared.stdout, prepared.stderr)
    record_bytes = case.prepared_path.read_bytes()
    record = json.loads(record_bytes)
    command = shlex.split(record["cutover_command"])
    candidate = case.data_root / version / "bin/concord"
    original_run = subprocess.run
    superseded = False
    def supersede_before_upgrade(argv, *args, **kwargs):
        global superseded
        if argv == [str(candidate), "upgrade"] and b'"require_no_sessions": true' in kwargs.get("input", b"") and not superseded:
            superseded = True
            replacement = case.install_release("v31.0.0", env=case.plan_env(blocked=True))
            assert replacement.returncode == 1 and "another maintenance command is in progress" in replacement.stderr, (replacement.stdout, replacement.stderr)
            raise RuntimeError("fixture: concurrent maintenance refused")
        return original_run(argv, *args, **kwargs)
    with mock.patch.object(subprocess, "run", side_effect=supersede_before_upgrade), mock.patch.dict(os.environ, case.env, clear=True):
        try:
            fixture.installer.cutover(SimpleNamespace(root=root, version=version, confirm_sessions_stopped=False, cancel_cutover_pinned_work=False))
        except RuntimeError as error:
            assert str(error) == "fixture: concurrent maintenance refused"
        else:
            raise AssertionError("the concurrent maintenance probe did not execute")
    assert superseded
    with sqlite3.connect(case.data_root / "concord.db") as db:
        assert db.execute("SELECT max(version) FROM schema_migrations").fetchone()[0] == 110
    assert not case.fence_path.exists() and not (case.data_root / "backups").exists()
    case.assert_active_release("v1.0.0", "v1.0.0")
    assert case.prepared_path.read_bytes() == record_bytes
    environment = case.env.copy()
    environment.pop("CONCORD_DB_PATH")
    environment["XDG_DATA_HOME"] = str(root / "data")
    leased = subprocess.run([str(candidate), "host-lease"], input=json.dumps({"pid":os.getpid(), "directory":"/synthetic/session"}), text=True, capture_output=True, env=environment)
    assert leased.returncode == 0, (leased.stdout, leased.stderr)
    stale = case.data_root / "hosts/0-stale.json"
    stale.write_text(json.dumps({"pid": 0, "pid_start": 1}))
    before = (case.data_root / "concord.db").read_bytes()
    refused = subprocess.run(command, text=True, capture_output=True, env=case.env, timeout=10)
    assert refused.returncode == 1 and ("pid " + str(os.getpid())) in refused.stderr, (refused.stdout, refused.stderr)
    assert stale.exists(), "refused cutover pruned the stale lease"
    assert before == (case.data_root / "concord.db").read_bytes()
    assert case.prepared_path.read_bytes() == record_bytes
    assert not case.fence_path.exists() and not (case.data_root / "backups").exists()
    case.assert_active_release("v1.0.0", "v1.0.0")
    for lease in (case.data_root / "hosts").iterdir():
        lease.unlink()
    crash_env = case.env.copy()
    crash_env["CONCORD_INSTALLER_STOP_AFTER_PHASE"] = "staged"
    crashed = subprocess.run(command, text=True, capture_output=True, env=crash_env, timeout=60)
    assert crashed.returncode != 0, (crashed.stdout, crashed.stderr)
    assert case.fence_path.exists() and case.prepared_path.exists(), (crashed.stdout, crashed.stderr)
    transaction = next((case.data_root / fixture.installer.TRANSACTION_PARENT).iterdir())
    fixture.installer.replace_durable(case.data_root / version, transaction / "backup/live-version")
    for _ in range(2):
        completed = subprocess.run(command, text=True, capture_output=True, env=case.env, timeout=60)
        assert completed.returncode == 0, (completed.stdout, completed.stderr)
        assert "Verified Concord " + version in completed.stdout
        assert not case.fence_path.exists() and not case.prepared_path.exists()
        case.assert_active_release(version, version)
    assert not (root / "must-not-be-opened.db").exists()
    with sqlite3.connect(case.data_root / "concord.db") as db:
        assert db.execute("SELECT max(version) FROM schema_migrations").fetchone()[0] == descriptor["schema_version"]
finally:
    case.tearDown()
`
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-c", driver, repository, paths.root, core)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepared cutover: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(filepath.Join(paths.dataRoot, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	snapshots := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "pre-upgrade-") || strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), "-wal") || strings.HasSuffix(entry.Name(), "-shm") {
			continue
		}
		snapshot := filepath.Join(paths.dataRoot, "backups", entry.Name())
		manifest, err := store.VerifyBackup(context.Background(), snapshot)
		if err != nil || manifest.SchemaVersion != 110 {
			t.Fatalf("pre-cutover snapshot: %+v %v", manifest, err)
		}
		assertSnapshotLacksMigration(t, snapshot, 111)
		snapshots++
	}
	if snapshots != 1 {
		t.Fatalf("want one verified snapshot across recovery and replay, found %d in %v", snapshots, entries)
	}
}
