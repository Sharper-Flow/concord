package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// repairFixture builds the offline layout the repair verb consumes: an
// installer manifest naming the installed release, the release checksum file,
// and the installer itself. The installer is a POSIX stub that records its
// argv, so the test observes exactly what the verified invocation received.
type repairFixture struct {
	root        string
	dataRoot    string
	artifactDir string
	argvPath    string
}

func newRepairFixture(t *testing.T, version, installerContent string) *repairFixture {
	t.Helper()
	fixture := &repairFixture{root: t.TempDir()}
	fixture.dataRoot = filepath.Join(fixture.root, "concord")
	if err := os.MkdirAll(fixture.dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"version":"` + version + `","adapter_files":{}}`)
	if err := os.WriteFile(filepath.Join(fixture.dataRoot, "install-manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.artifactDir = filepath.Join(fixture.root, "published")
	if err := os.MkdirAll(fixture.artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installerPath := filepath.Join(fixture.artifactDir, "concord-installer.py")
	if err := os.WriteFile(installerPath, []byte(installerContent), 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(installerContent))
	fixture.argvPath = filepath.Join(fixture.root, "argv")
	archive := filepath.Join(fixture.artifactDir, "concord-"+version+".tar.gz")
	if err := os.WriteFile(archive, []byte("release archive bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	archiveDigest := sha256.Sum256([]byte("release archive bytes"))
	checksum := hex.EncodeToString(digest[:]) + "  concord-installer.py\n" +
		hex.EncodeToString(archiveDigest[:]) + "  concord-" + version + ".tar.gz\n"
	if err := os.WriteFile(filepath.Join(fixture.artifactDir, "concord-"+version+".sha256"), []byte(checksum), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, filepath.Join(fixture.dataRoot, "store.db"))
	previous := repairInterpreter
	repairInterpreter = ""
	t.Cleanup(func() { repairInterpreter = previous })
	return fixture
}

func runRepairStdin(t *testing.T, stdin string) (int, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	code := runWithInput([]string{"repair"}, strings.NewReader(stdin), out, errOut)
	return code, out, errOut
}

func TestRepairRunsTheVerifiedInstallerWithTheInstalledRelease(t *testing.T) {
	version := "v9.9.9"
	// The stub records its argv so the test observes exactly what the
	// verified invocation received.
	argvPath := filepath.Join(t.TempDir(), "argv")
	installer := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argvPath + "\"\nexit 0\n"
	fixture := newRepairFixture(t, version, installer)

	code, out, errOut := runRepairStdin(t, `{"artifact_dir":"`+filepath.ToSlash(fixture.artifactDir)+`"}`)
	if code != 0 {
		t.Fatalf("repair refused: %s", errOut.String())
	}
	recorded, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("the verified installer never ran: %v", err)
	}
	argv := strings.Fields(string(recorded))
	want := []string{"repair", "--version", version}
	for i, argument := range want {
		if argv[i] != argument {
			t.Fatalf("argv[%d] = %q, want %q; full argv %v", i, argv[i], argument, argv)
		}
	}
	if !strings.Contains(strings.Join(argv, " "), "--artifact-dir") {
		t.Fatalf("the installer ran without the artifact directory: %v", argv)
	}
	if !strings.Contains(out.String(), "no work database found") {
		t.Fatalf("the missing database was not reported: %q", out.String())
	}
}

func TestRepairBacksUpTheWorkDatabaseBeforeRepair(t *testing.T) {
	version := "v9.9.9"
	installer := "#!/bin/sh\nexit 0\n"
	fixture := newRepairFixture(t, version, installer)
	// A live work database turns on the pre-repair snapshot path, including
	// the backup parent the verb must create on first use.
	database, err := store.Open(context.Background(), filepath.Join(fixture.dataRoot, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runRepairStdin(t, `{"artifact_dir":"`+filepath.ToSlash(fixture.artifactDir)+`"}`)

	if code != 0 {
		t.Fatalf("repair refused: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "database backup at") {
		t.Fatalf("the backup location was not reported: %q", out.String())
	}
	entries, err := os.ReadDir(filepath.Join(fixture.dataRoot, "backups"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no pre-repair backup was written under the data root: %v", err)
	}
}

func TestRepairRefusesAnInstallerThatFailsItsChecksum(t *testing.T) {
	version := "v9.9.9"
	fixture := newRepairFixture(t, version, "#!/bin/sh\nexit 0\n")
	// Tamper with the published checksum so the installer no longer matches.
	checksumPath := filepath.Join(fixture.artifactDir, "concord-"+version+".sha256")
	if err := os.WriteFile(checksumPath, []byte(strings.Repeat("0", 64)+"  concord-installer.py\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(fixture.root, "ran")
	if err := os.WriteFile(filepath.Join(fixture.artifactDir, "concord-installer.py"),
		[]byte("#!/bin/sh\ntouch \""+marker+"\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := runRepairStdin(t, `{"artifact_dir":"`+filepath.ToSlash(fixture.artifactDir)+`"}`)

	if code == 0 {
		t.Fatal("repair accepted an installer that fails its published checksum")
	}
	if !strings.Contains(errOut.String(), "checksum mismatch") {
		t.Fatalf("the refusal did not name the checksum mismatch: %q", errOut.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the unverified installer executed")
	}
}

func TestRepairRefusesWithoutAnInstalledManifest(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "concord")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, filepath.Join(dataRoot, "store.db"))

	code, _, errOut := runRepairStdin(t, `{}`)

	if code == 0 {
		t.Fatal("repair ran without an installed manifest")
	}
	if !strings.Contains(errOut.String(), "no installer manifest") {
		t.Fatalf("the refusal did not name the missing manifest: %q", errOut.String())
	}
}

func TestRepairRefusesAnArtifactDirectoryMissingTheReleaseAssets(t *testing.T) {
	version := "v9.9.9"
	fixture := newRepairFixture(t, version, "#!/bin/sh\nexit 0\n")
	if err := os.Remove(filepath.Join(fixture.artifactDir, "concord-"+version+".tar.gz")); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := runRepairStdin(t, `{"artifact_dir":"`+filepath.ToSlash(fixture.artifactDir)+`"}`)

	if code == 0 {
		t.Fatal("repair accepted an artifact directory without the release archive")
	}
	if !strings.Contains(errOut.String(), "missing") {
		t.Fatalf("the refusal did not name the missing artifact: %q", errOut.String())
	}
}
