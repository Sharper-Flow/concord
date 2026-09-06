// Package hostlease records which release each live host session runs on.
// The installer reads the lease set to decide which release directories it
// may remove (CD-0111 D2), and concord upgrade reads it to refuse while a
// live session predates a pending breaking migration (CD-0111 D3). Both
// decisions belong to callers; this package only writes, reads, and prunes
// the leases themselves.
//
// A lease is live when its process exists and started when the lease says it
// did. The start time separates a live pid from a recycled one. Liveness
// reads /proc, so the package operates on Linux, the only release platform.
package hostlease

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Lease is one host session's claim on the release it runs.
type Lease struct {
	PID            int    `json:"pid"`
	PidStart       uint64 `json:"pid_start"`
	ReleaseRoot    string `json:"release_root"`
	CoreBinary     string `json:"core_binary"`
	SchemaVersion  int    `json:"schema_version"`
	ManifestDigest string `json:"manifest_digest"`
	RecordedAt     string `json:"recorded_at"`
}

// ErrStaleLease marks a lease whose process no longer matches. Write uses it
// so a session cannot claim liveness for a process that already ended.
var ErrStaleLease = errors.New("hostlease: the process is not live with the recorded start time")

// Directory returns the lease directory for a data root. The database path
// and the releases live under the same root, so the caller derives both from
// one XDG data home.
func Directory(dataRoot string) string {
	return filepath.Join(dataRoot, "hosts")
}

// Write records the lease for the process, replacing any earlier lease the
// same process wrote. It fails when the process is not live as described.
func Write(dataRoot string, lease Lease) error {
	if lease.PID <= 0 {
		return fmt.Errorf("hostlease: lease pid %d is invalid", lease.PID)
	}
	start, err := ProcessStart(lease.PID)
	if err != nil {
		return err
	}
	if start != lease.PidStart {
		return fmt.Errorf("%w: pid %d now starts at %d, lease says %d", ErrStaleLease, lease.PID, start, lease.PidStart)
	}
	dir := Directory(dataRoot)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("hostlease: cannot create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("hostlease: cannot secure %s: %w", dir, err)
	}
	encoded, err := json.Marshal(lease)
	if err != nil {
		return fmt.Errorf("hostlease: cannot encode the lease: %w", err)
	}
	target := filepath.Join(dir, strconv.Itoa(lease.PID)+".json")
	temporary := target + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("hostlease: cannot write %s: %w", temporary, err)
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("hostlease: cannot replace %s: %w", target, err)
	}
	return nil
}

// List returns the live leases under the data root and prunes the stale
// ones. A lease whose process ended or was recycled is stale. A lease file
// this package cannot interpret is an error, not a stale entry: deleting an
// unreadable lease could remove a release a newer release's session still
// holds, so the caller must observe the failure instead of guessing.
func List(dataRoot string) ([]Lease, error) {
	dir := Directory(dataRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("hostlease: cannot read %s: %w", dir, err)
	}
	var live []Lease
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("hostlease: refusing unrecognized entry %s", filepath.Join(dir, name))
		}
		encoded, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("hostlease: cannot read %s: %w", filepath.Join(dir, name), err)
		}
		var lease Lease
		if err := json.Unmarshal(encoded, &lease); err != nil {
			return nil, fmt.Errorf("hostlease: refusing malformed lease %s: %w", filepath.Join(dir, name), err)
		}
		start, err := ProcessStart(lease.PID)
		if err != nil || start != lease.PidStart {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return nil, fmt.Errorf("hostlease: cannot prune %s: %w", filepath.Join(dir, name), err)
			}
			continue
		}
		live = append(live, lease)
	}
	return live, nil
}

// ProcessStart reads the process start time from /proc: field 22 of
// /proc/<pid>/stat, in clock ticks. The comm field can contain spaces and
// parentheses, so the field is counted after its closing parenthesis.
func ProcessStart(pid int) (uint64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("hostlease: process %d is not observable: %w", pid, err)
	}
	text := string(raw)
	end := strings.LastIndexByte(text, ')')
	if end < 0 || end+2 > len(text) {
		return 0, fmt.Errorf("hostlease: /proc/%d/stat is malformed", pid)
	}
	fields := strings.Fields(text[end+2:])
	// Field 3 (state) is the first field after comm, so starttime is field
	// 22 overall: index 22-3 in this slice.
	const starttime = 22 - 3
	if len(fields) <= starttime {
		return 0, fmt.Errorf("hostlease: /proc/%d/stat is truncated", pid)
	}
	value, err := strconv.ParseUint(fields[starttime], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("hostlease: /proc/%d/stat starttime is not a number: %w", pid, err)
	}
	return value, nil
}
