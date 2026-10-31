package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestPrintedUpgradeCommandDoesNotWaitForTerminalInput(t *testing.T) {
	repository := installerRepositoryRoot(t)
	core := buildCoreFrom(t, repository, "v30.0.99-terminal")
	paths := newInteropPaths(t)
	driver := `import importlib.util, os, pathlib, pty, shlex, subprocess, sys
spec = importlib.util.spec_from_file_location("installer", sys.argv[1])
installer = importlib.util.module_from_spec(spec)
sys.modules["installer"] = installer
spec.loader.exec_module(installer)
paths = installer.paths_for(pathlib.Path(sys.argv[2]))
binary = paths.data_root / "v30.0.99" / "bin" / "concord"
binary.parent.mkdir(parents=True)
binary.symlink_to(sys.argv[3])
command = shlex.join([str(binary), "upgrade"])
master, slave = pty.openpty()
try:
    with subprocess.Popen(["sh", "-c", "exec " + command], stdin=slave, stdout=subprocess.PIPE, stderr=subprocess.PIPE) as child:
        try:
            out, err = child.communicate(timeout=2)
        except subprocess.TimeoutExpired:
            child.kill()
            out, err = child.communicate()
            raise AssertionError("printed migration command waits silently for terminal EOF")
        assert child.returncode == 1, (child.returncode, out, err)
        assert b"JSON" in err and b"stdin" in err, err
        assert not (paths.data_root / "concord.db").exists()
        assert not (paths.data_root / "maintenance.json").exists()
finally:
    os.close(slave)
    os.close(master)
`
	command := exec.Command("python3", "-c", driver, filepath.Join(repository, "scripts", "install.py"), paths.root, core)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("terminal upgrade: %v\n%s", err, output)
	}
}

func TestPrintedMigrationRecoveryRunsWithTerminalOnActualCore(t *testing.T) {
	repository := installerRepositoryRoot(t)
	core := buildCoreFrom(t, repository, "v30.0.99")
	root := filepath.Join(t.TempDir(), "root with spaces")
	db := filepath.Join(root, "data", "concord", "concord.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	driver := `import contextlib, importlib.util, io, json, os, pathlib, pty, signal, subprocess, sys
spec = importlib.util.spec_from_file_location("installer", sys.argv[1])
installer = importlib.util.module_from_spec(spec)
sys.modules["installer"] = installer
spec.loader.exec_module(installer)
paths = installer.paths_for(pathlib.Path(sys.argv[2]))
binary = paths.data_root / "v30.0.99/bin/concord"
binary.parent.mkdir(parents=True)
binary.symlink_to(sys.argv[3])
record = {"version":"v30.0.99", "blockers":[], "cutover_command":"cutover-primary", "migration_command":installer.plan_migration_command(paths, "v30.0.99"), "activation_command":"activate-optional"}
output = io.StringIO()
with contextlib.redirect_stdout(output):
    installer.print_prepared_release(record)
lines = output.getvalue().splitlines()
matches = [line for line in lines if record["migration_command"] in line]
command = matches[0].split(": ", 1)[1] if len(matches) == 1 else record["migration_command"]
environment = dict(os.environ, XDG_DATA_HOME=str(paths.home / "other"), CONCORD_DB_PATH=str(paths.home / "other.db"))
master, slave = pty.openpty()
try:
    with subprocess.Popen(["sh", "-c", command], stdin=slave, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=environment, start_new_session=True) as child:
        try:
            out, err = child.communicate(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(child.pid, signal.SIGKILL)
            child.communicate()
            raise AssertionError("printed migration command waits for terminal input")
        assert child.returncode == 0, (child.returncode, out, err)
        json.loads(out)
        assert not (paths.home / "other.db").exists()
        assert not (paths.home / "other").exists()
        assert len(matches) == 1 and command == record["migration_command"], lines
finally:
    os.close(slave)
    os.close(master)
`
	command := exec.Command("python3", "-c", driver, filepath.Join(repository, "scripts/install.py"), root, core)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("printed migration recovery: %v\n%s", err, output)
	}
}
