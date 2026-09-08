package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

const defaultReleaseBaseURL = "https://github.com/Sharper-Flow/concord/releases"

// repairInterpreter is the program that runs the release installer. A test
// substitutes a direct-exec stub; production always asks python3.
var repairInterpreter = "python3"

// runRepairCommand completes an incomplete installation of the installed
// release (#912). The verb is orchestration around the owning installer:
// resolve the installer a release published, checksum-verify it against that
// release's published checksums, snapshot the work database through the
// store's backup API, then hand the mutation to the installer's own repair
// subcommand. The file-level repair logic lives in one place, the installer.
// When the core executable itself is missing, the supported entry is the
// published installer run directly: python3 concord-installer.py repair.
func runRepairCommand(args []string, in io.Reader, out, errOut io.Writer) int {
	var request struct {
		InstallerVersion string `json:"installer_version"`
		ArtifactDir      string `json:"artifact_dir"`
		BaseURL          string `json:"base_url"`
	}
	if code := decodeReleaseInput(args, in, out, errOut, "repair", &request); code != 0 {
		return code
	}
	dataRoot, err := leaseDataRoot()
	if err != nil {
		writeOperatorDiagnostic(errOut, "repair", err.Error())
		return 1
	}
	manifestPath := filepath.Join(dataRoot, "install-manifest.json")
	rawManifest, err := os.ReadFile(manifestPath) //nolint:gosec // manifestPath is the installer's own durable state under the leased data root, not agent input.
	if err != nil {
		writeOperatorDiagnostic(errOut, "repair", fmt.Sprintf("no installer manifest at %s; run install", manifestPath))
		return 1
	}
	var manifest struct {
		Version string `json:"version"`
	}
	// The manifest is the installer's own durable state, not agent input, so
	// it decodes permissively: only the version field carries meaning here.
	if err := json.Unmarshal(rawManifest, &manifest); err != nil || manifest.Version == "" {
		writeOperatorDiagnostic(errOut, "repair", fmt.Sprintf("installer manifest at %s has no version; run install", manifestPath))
		return 1
	}
	installerTag := request.InstallerVersion
	if installerTag == "" {
		installerTag = manifest.Version
	}
	baseURL := request.BaseURL
	if baseURL == "" {
		baseURL = defaultReleaseBaseURL
	}
	workspace, err := os.MkdirTemp("", "concord-repair-")
	if err != nil {
		writeOperatorDiagnostic(errOut, "repair", "cannot create a repair workspace: "+err.Error())
		return 1
	}
	defer func() { _ = os.RemoveAll(workspace) }()
	assets := []struct {
		name string
		tag  string
	}{
		{fmt.Sprintf("concord-%s.sha256", manifest.Version), manifest.Version},
		{fmt.Sprintf("concord-%s.tar.gz", manifest.Version), manifest.Version},
		{"concord-installer.py", installerTag},
	}
	if installerTag != manifest.Version {
		assets = append(assets, struct {
			name string
			tag  string
		}{fmt.Sprintf("concord-%s.sha256", installerTag), installerTag})
	}
	for _, asset := range assets {
		if err := gatherRepairAsset(asset.name, asset.tag, baseURL, request.ArtifactDir, workspace); err != nil {
			writeOperatorDiagnostic(errOut, "repair", err.Error())
			return 1
		}
	}
	// The installer this verb is about to execute must match the checksum the
	// named release published for it; unverified code never runs.
	checksumPath := filepath.Join(workspace, fmt.Sprintf("concord-%s.sha256", installerTag))
	records, err := parseChecksumFile(checksumPath)
	if err != nil {
		writeOperatorDiagnostic(errOut, "repair", err.Error())
		return 1
	}
	installerPath := filepath.Join(workspace, "concord-installer.py")
	expected, ok := records["concord-installer.py"]
	if !ok {
		writeOperatorDiagnostic(errOut, "repair", fmt.Sprintf("%s has no concord-installer.py entry", checksumPath))
		return 1
	}
	if err := verifyFileSHA256(installerPath, expected); err != nil {
		writeOperatorDiagnostic(errOut, "repair", fmt.Sprintf("installer checksum mismatch for concord-installer.py from %s: %s", installerTag, err.Error()))
		return 1
	}
	// The direct-exec entry documented for a missing executable needs the
	// executable bit; the gathered copy carries data permissions only.
	if err := os.Chmod(installerPath, 0o755); err != nil { //nolint:gosec // the executable bit is the documented direct-exec entry contract for the checksum-verified installer copy.
		writeOperatorDiagnostic(errOut, "repair", "cannot prepare the verified installer: "+err.Error())
		return 1
	}
	// A verified database snapshot precedes the repair mutation. A missing
	// database is reported, not fatal: an incomplete install may never have
	// reached store creation.
	if code := backupBeforeRepair(out, errOut, dataRoot, manifest.Version); code != 0 {
		return code
	}
	_, _ = fmt.Fprintf(out, "repair: release %s, installer from %s\n", manifest.Version, installerTag)
	var command *exec.Cmd
	installerArgs := []string{"repair", "--version", manifest.Version, "--artifact-dir", workspace}
	if repairInterpreter == "" {
		command = exec.Command(installerPath, installerArgs...) //nolint:gosec // installerPath is the checksum-verified installer copy this command materialized.
	} else {
		command = exec.Command(repairInterpreter, append([]string{installerPath}, installerArgs...)...) //nolint:gosec // the interpreter is an explicit operator flag and the installer copy is checksum-verified.
	}
	command.Stdout = out
	command.Stderr = errOut
	command.Env = os.Environ()
	if err := command.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		writeOperatorDiagnostic(errOut, "repair", "cannot run the verified installer: "+err.Error())
		return 1
	}
	return 0
}

// backupBeforeRepair snapshots the work database to a fresh path under the
// data root and prints the location. The backup verb's own rules apply: the
// destination must not exist, and the snapshot is verified after it is taken.
func backupBeforeRepair(out, errOut io.Writer, dataRoot, version string) int {
	path, err := databasePath()
	if err != nil {
		writeOperatorDiagnostic(errOut, "repair", err.Error())
		return 1
	}
	info, statErr := os.Stat(path)
	if statErr != nil || info.IsDir() {
		_, _ = fmt.Fprintln(out, "repair: no work database found; skipped the backup")
		return 0
	}
	s, err := store.Open(context.Background(), path)
	if err != nil {
		writeOperatorDiagnostic(errOut, "repair", "cannot open the work database for backup: "+err.Error())
		return 1
	}
	defer func() { _ = s.Close() }()
	// The backup API requires an existing destination parent; the repair
	// tree under the data root is created on first use.
	if err := os.MkdirAll(filepath.Join(dataRoot, "backups"), 0o750); err != nil {
		writeOperatorDiagnostic(errOut, "repair", "cannot create the backup directory: "+err.Error())
		return 1
	}
	destination := filepath.Join(
		dataRoot, "backups",
		fmt.Sprintf("pre-repair-%s-%s", version, time.Now().UTC().Format("20060102T150405Z")),
	)
	if _, err := store.Backup(context.Background(), s, destination); err != nil {
		writeOperatorDiagnostic(errOut, "repair", "database backup failed; nothing was repaired: "+err.Error())
		return 1
	}
	if _, err := store.VerifyBackup(context.Background(), destination); err != nil {
		writeOperatorDiagnostic(errOut, "repair", "database backup verification failed; nothing was repaired: "+err.Error())
		return 1
	}
	_, _ = fmt.Fprintf(out, "repair: database backup at %s\n", destination)
	return 0
}

// gatherRepairAsset materializes one release asset in the workspace, from the
// offline artifact directory when one is given and the network otherwise.
func gatherRepairAsset(name, tag, baseURL, artifactDir, workspace string) error {
	destination := filepath.Join(workspace, name)
	if artifactDir != "" {
		source := filepath.Join(artifactDir, name)
		content, err := os.ReadFile(source) //nolint:gosec // source is a member of the operator-named artifact directory, checksum-verified before use.
		if err != nil {
			return fmt.Errorf("artifact directory is missing %s", source)
		}
		return os.WriteFile(destination, content, 0o600) //nolint:gosec // destination joins the command workspace with a release asset name the checksum file governs.
	}
	url := strings.TrimSuffix(baseURL, "/") + "/download/" + tag + "/" + name
	response, err := http.Get(url) //nolint:gosec // url derives from the release base URL, and the downloaded asset is checksum-verified before any use.
	if err != nil {
		return fmt.Errorf("could not download %s: %s", url, err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("could not download %s: status %s", url, response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<30))
	if err != nil {
		return fmt.Errorf("could not download %s: %s", url, err.Error())
	}
	return os.WriteFile(destination, content, 0o600)
}

// parseChecksumFile reads a sha256sum-format file into name → digest.
func parseChecksumFile(path string) (map[string]string, error) {
	content, err := os.ReadFile(path) //nolint:gosec // the checksum file is a release asset this command downloaded into its own workspace.
	if err != nil {
		return nil, fmt.Errorf("cannot read %s", path)
	}
	records := make(map[string]string)
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		records[filepath.Base(fields[1])] = fields[0]
	}
	return records, nil
}

// verifyFileSHA256 refuses unless the file's digest equals the expected one.
func verifyFileSHA256(path, expected string) error {
	content, err := os.ReadFile(path) //nolint:gosec // the digest is computed from a workspace asset whose checksum file already verified.
	if err != nil {
		return fmt.Errorf("cannot read %s", path)
	}
	digest := sha256.Sum256(content)
	actual := hex.EncodeToString(digest[:])
	if actual != strings.ToLower(expected) {
		return fmt.Errorf("expected %s, got %s", expected, actual)
	}
	return nil
}
