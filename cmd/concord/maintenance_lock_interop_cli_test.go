package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

// The maintenance lock is one shared identity across two languages:
// the installer's Python maintenance_lock and the core's Go
// hostlease.AcquireMaintenance must be the same non-blocking flock on the
// data root directory, including the first-install bootstrap of an absent
// root. Both directions below run the real artifacts — the working tree's
// built core binary and scripts/install.py itself — so the evidence is the
// lock's observable behavior, not a shared stub.
//
// The lock identity rules under test:
//
//   - a first-install bootstrap creates the root before opening it, so the
//     very first command already excludes every other maintenance command;
//   - the root is opened without following symlinks;
//   - after the flock, the open descriptor's device and inode must still
//     match the path, and a root replaced before admission is re-acquired
//     through a bounded local sequence that never sleeps on contention.

// installerRepositoryRoot locates the repository that owns this test source,
// the same way the released-pair test does. The interop tests drive the
// repository's real installer script beside this build's real core.
func installerRepositoryRoot(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is unavailable: %v", err)
	}
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	toplevel, err := exec.Command(git, "-C", working, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("the test source is not inside a git repository: %v", err)
	}
	return strings.TrimSpace(string(toplevel))
}

// interopPaths is one synthetic layout shared by both languages: the
// installer maps the root through paths_for, and the core's data root is the
// database path's directory, exactly the way the recorded migration command
// pins XDG_DATA_HOME.
type interopPaths struct {
	root     string
	dataRoot string
	dbPath   string
}

func newInteropPaths(t *testing.T) interopPaths {
	t.Helper()
	root := t.TempDir()
	return interopPaths{
		root:     root,
		dataRoot: filepath.Join(root, "data", "concord"),
		dbPath:   filepath.Join(root, "data", "concord", "concord.db"),
	}
}

// holdInstallerMaintenance runs the real installer module in a Python child
// that enters maintenance_lock on an absent root and holds it until its
// stdin is signalled. The first line of output reports the held lock. The
// returned release function signals the holder, waits for its clean exit,
// and returns the output that followed the hold.
func holdInstallerMaintenance(t *testing.T, installerPath string, paths interopPaths) (*bufio.Reader, func() string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 is unavailable: %v", err)
	}
	driver := filepath.Join(t.TempDir(), "hold-maintenance.py")
	script := `import importlib.util, pathlib, sys
spec = importlib.util.spec_from_file_location("installer", sys.argv[1])
installer = importlib.util.module_from_spec(spec)
sys.modules["installer"] = installer
spec.loader.exec_module(installer)
paths = installer.paths_for(pathlib.Path(sys.argv[2]))
with installer.maintenance_lock(paths):
    print("HELD", flush=True)
    sys.stdin.readline()
print("RELEASED", flush=True)
`
	if err := os.WriteFile(driver, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	runContext, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	command := exec.CommandContext(runContext, python, driver, installerPath, paths.root)
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "HELD" {
		_ = command.Process.Kill()
		cancel()
		t.Fatalf("the installer holder never reported the lock: %q %v", line, err)
	}
	release := func() string {
		_, _ = stdin.Write([]byte("\n"))
		trailing, _ := reader.ReadString('\n')
		_ = command.Wait()
		cancel()
		return trailing
	}
	return reader, release
}

// maintenanceLockIsHeld reports whether the non-blocking maintenance lock
// on the data root is currently held by someone else.
func maintenanceLockIsHeld(dataRoot string) (bool, error) {
	descriptor, err := syscall.Open(dataRoot, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = syscall.Close(descriptor) }()
	if err := syscall.Flock(descriptor, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	if err := syscall.Flock(descriptor, syscall.LOCK_UN); err != nil {
		return false, err
	}
	return false, nil
}

// waitUntilMaintenanceIsHeld polls the lock until another process holds it.
// The poll lives in the test only; the production acquisition never waits.
func waitUntilMaintenanceIsHeld(t *testing.T, dataRoot string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		held, err := maintenanceLockIsHeld(dataRoot)
		if err == nil && held {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the maintenance lock at %s was never held by the core", dataRoot)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runInstallerCommand runs the real installer and returns its exit code and
// combined output.
func runInstallerCommand(t *testing.T, installerPath string, arguments ...string) (int, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 is unavailable: %v", err)
	}
	context_, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var output bytes.Buffer
	argv := append([]string{installerPath}, arguments...)
	command := exec.CommandContext(context_, python, argv...)
	command.Stdout = &output
	command.Stderr = &output
	err = command.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("cannot run the installer: %v: %s", err, output.String())
	}
	return code, output.String()
}

// Direction one: the installer holds the maintenance lock over a first
// install's absent root, and the real core's migration command refuses
// before it reads the store.
func TestCoreRefusesMaintenanceWhileTheInstallerHoldsAnAbsentRoot(t *testing.T) {
	repository := installerRepositoryRoot(t)
	paths := newInteropPaths(t)
	_, stop := holdInstallerMaintenance(t, filepath.Join(repository, "scripts", "install.py"), paths)
	defer stop()

	core := buildCoreFrom(t, repository, "v11.63.99-interop")
	var out, errOut bytes.Buffer
	command := exec.Command(core, "upgrade")
	command.Stdin = strings.NewReader("{}")
	command.Stdout = &out
	command.Stderr = &errOut
	command.Env = append(os.Environ(), dbOverrideEnv+"="+paths.dbPath)
	err := command.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(errOut.String(), "another maintenance command is in progress") {
		t.Fatalf("the core must refuse while the installer bootstraps: code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), paths.dataRoot) {
		t.Fatalf("the refusal must name the shared data root: %s", errOut.String())
	}
	if _, err := os.Stat(paths.dbPath); !os.IsNotExist(err) {
		t.Fatalf("the refused core must not touch the store: %v", err)
	}
	if released := strings.TrimSpace(stop()); released != "RELEASED" {
		t.Fatalf("the installer holder did not release cleanly: %q", released)
	}
}

// Direction two: the real core holds the maintenance lock, and the real
// installer refuses before it recovers a transaction. The core is
// parked mid-upgrade on the shared admission lock the fence path takes, so
// the hold is deterministic and the migration itself stays inside the
// boundary until the test lets it finish.
func TestInstallerRefusesMaintenanceWhileTheCoreHoldsIt(t *testing.T) {
	repository := installerRepositoryRoot(t)
	paths := newInteropPaths(t)
	core := buildCoreFrom(t, repository, "v11.63.99-interop")
	pendingBreakingStore(t, paths.dbPath, true)

	// Park the core behind the admission lock: its fence opening blocks
	// there while it already holds the maintenance lock.
	if err := os.MkdirAll(paths.dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	admission, err := os.OpenFile(filepath.Join(paths.dataRoot, "admission.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admission.Close() }()
	if err := syscall.Flock(int(admission.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	runContext, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var coreOut, coreErr bytes.Buffer
	upgrade := exec.CommandContext(runContext, core, "upgrade")
	upgrade.Stdin = strings.NewReader("{}")
	upgrade.Stdout = &coreOut
	upgrade.Stderr = &coreErr
	upgrade.Env = append(os.Environ(), dbOverrideEnv+"="+paths.dbPath)
	if err := upgrade.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntilMaintenanceIsHeld(t, paths.dataRoot)

	code, output := runInstallerCommand(t, filepath.Join(repository, "scripts", "install.py"), "status", "--root", paths.root)
	if code != 1 || !strings.Contains(output, "another maintenance command is in progress") {
		t.Fatalf("the installer must refuse while the core migrates: code=%d out=%s", code, output)
	}

	// Let the parked migration finish inside its boundary.
	if err := syscall.Flock(int(admission.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := upgrade.Wait(); err != nil {
		if exit, ok := err.(*exec.ExitError); !ok {
			t.Fatal(err)
		} else if exit.ExitCode() != 0 {
			t.Fatalf("the parked migration did not complete: %s%s", coreOut.String(), coreErr.String())
		}
	}
	held, err := maintenanceLockIsHeld(paths.dataRoot)
	if err != nil || held {
		t.Fatalf("the core must release the maintenance lock at exit: held=%v %v", held, err)
	}
}

// The read-only plan cooperates with a held maintenance lock: it takes no
// maintenance lock itself and creates no missing store, so an operator can
// plan while a migration runs.
func TestPlanReadsNoStoreIntoExistenceWhileMaintenanceIsHeld(t *testing.T) {
	path, root := cliStoreRoot(t)
	release, err := hostlease.AcquireMaintenance(root)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	code, out, errOut := runUpgradeStdin(t, `{"plan":true}`)
	if code != 0 {
		t.Fatalf("the read-only plan must succeed while maintenance is held: %s", errOut)
	}
	if !strings.Contains(out, `"fresh_store":true`) {
		t.Fatalf("an absent store must plan as fresh: %s", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the plan must not create the missing store: %v", err)
	}
}
