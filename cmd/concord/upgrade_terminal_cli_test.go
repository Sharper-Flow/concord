package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrintedUpgradeCommandDoesNotWaitForTerminalInput(t *testing.T) {
	repository := installerRepositoryRoot(t)
	core := buildCoreFrom(t, repository, "v30.0.99-terminal")
	paths := newInteropPaths(t)
	driver := `import importlib.util, os, pathlib, pty, subprocess, sys
spec = importlib.util.spec_from_file_location("installer", sys.argv[1])
installer = importlib.util.module_from_spec(spec)
sys.modules["installer"] = installer
spec.loader.exec_module(installer)
paths = installer.paths_for(pathlib.Path(sys.argv[2]))
binary = paths.data_root / "v30.0.99" / "bin" / "concord"
binary.parent.mkdir(parents=True)
binary.symlink_to(sys.argv[3])
command = installer.plan_migration_command(paths, "v30.0.99")
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
