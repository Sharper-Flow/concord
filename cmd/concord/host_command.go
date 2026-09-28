package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

// hostRegistryProbeTimeout bounds each probe. The host resolves plugins and
// providers before it prints, so this is generous relative to the measured
// cost; it exists to turn a hung host into a typed refusal rather than a
// session that never starts.
const hostRegistryProbeTimeout = 60 * time.Second

// defaultHostCommand is the host command Concord starts when the operator
// names none. It is the behavior every release before the host_command
// option shipped (CD-0093 D4 as amended by CD-0189).
var defaultHostCommand = []string{"opencode"}

// hostCommandOption is the plugin option key that names the host launch
// command. The operator sets it in the options of the Concord plugin tuple
// in host OpenCode config, beside the CD-0182 session_opener, and Concord
// reads it from the `opencode debug config` document the registry probe
// already parses. The wrapper is a host fact, so it lives in host
// configuration; Concord gains no config file and no store field.
const hostCommandOption = "host_command"

// concordPluginEntryFile is the file name of the shipped plugin entry. The
// installer registers the resolved path of this file, so the tuple whose
// entry ends with this name in the host's resolved plugin list is the
// Concord plugin tuple.
const concordPluginEntryFile = "concord-plugin.ts"

// hostCommandResolution is the outcome of resolving the host command in one
// directory: the argv every host invocation runs, and the registry document
// the identity verification reads. CD-0093 D2 binds the registry check and
// host execution to one directory; the resolution also binds them to one
// host, because when a command is configured the registry document is the
// one the configured command itself resolves, so the verified registry is
// the executing registry.
type hostCommandResolution struct {
	Command  []string
	Registry []byte
}

// sessionHostCommandFunc resolves the host command and registry document in
// dir. The directory is a parameter so the resolution observes the same
// resolved directory the identity and launch steps use (CD-0093 D2).
type sessionHostCommandFunc func(ctx context.Context, dir string) (hostCommandResolution, error)

// hostConfigProbeFunc runs the supplied host argv in dir and returns the
// configuration document the host prints. It is a parameter so tests can
// supply documents without starting a host process.
type hostConfigProbeFunc func(ctx context.Context, argv []string, dir string) ([]byte, error)

// hostCommandInvalidError reports a host_command option Concord refuses: a
// present but malformed value, or a configured command whose own document
// does not carry the identical value. The diagnostic names the option, and
// CD-0049 D4 admits no fallback start through the bare host.
type hostCommandInvalidError struct {
	Problem string
	Cause   error
}

func (e *hostCommandInvalidError) Error() string {
	message := "host_command " + e.Problem
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *hostCommandInvalidError) Unwrap() error { return e.Cause }

// hostProbeArgv returns the argv a host command resolves its configuration
// with. The bare default probe is hostProbeArgv(defaultHostCommand).
func hostProbeArgv(command []string) []string {
	return append(append([]string(nil), command...), "debug", "config")
}

// resolveHostCommand reads the host_command option from the bare probe's
// document in dir. Absent, the host command is the default and the bare
// document is the registry, so behavior without the option is unchanged.
// Present, a second probe runs through the configured command, and its
// document must carry an identical value: a wrapper may resolve a different
// configuration than the bare host, and CD-0093 D2 refuses to verify one
// registry and execute another. The extra probe is the recorded cost of a
// configured command, and each probe through a wrapper runs that wrapper's
// own start and exit behavior (CD-0189).
func resolveHostCommand(ctx context.Context, dir string, probe hostConfigProbeFunc) (hostCommandResolution, error) {
	document, err := probe(ctx, hostProbeArgv(defaultHostCommand), dir)
	if err != nil {
		return hostCommandResolution{}, fmt.Errorf("host registry probe failed: %w", err)
	}
	command, present, err := hostCommandFromDocument(document)
	if err != nil {
		return hostCommandResolution{}, fmt.Errorf("host registry probe returned an unreadable document: %w", err)
	}
	if !present {
		return hostCommandResolution{Command: append([]string(nil), defaultHostCommand...), Registry: document}, nil
	}
	configuredDocument, err := probe(ctx, hostProbeArgv(command), dir)
	if err != nil {
		return hostCommandResolution{}, &hostCommandInvalidError{Problem: "could not be probed through the configured command", Cause: err}
	}
	confirmed, present, err := hostCommandFromDocument(configuredDocument)
	if err != nil {
		return hostCommandResolution{}, &hostCommandInvalidError{Problem: "could not be read from the configured command's own document", Cause: err}
	}
	if !present {
		return hostCommandResolution{}, &hostCommandInvalidError{Problem: "is absent from the configured command's own document"}
	}
	if !equalArgv(confirmed, command) {
		return hostCommandResolution{}, &hostCommandInvalidError{Problem: fmt.Sprintf("names %q in the bootstrap probe but %q in the configured command's own document", command, confirmed)}
	}
	return hostCommandResolution{Command: command, Registry: configuredDocument}, nil
}

// hostCommandFromDocument reads the host_command option out of a resolved
// host configuration document. present is false when no Concord plugin
// tuple in the document names the option. A present value that is not a
// non-empty array of non-empty strings is malformed and refuses.
func hostCommandFromDocument(document []byte) (command []string, present bool, err error) {
	var resolved hostConfigDocument
	if err := json.Unmarshal(document, &resolved); err != nil {
		return nil, false, err
	}
	for _, raw := range resolved.Plugin {
		entry, options, ok := decodePluginEntry(raw)
		if !ok || path.Base(strings.TrimPrefix(entry, "file://")) != concordPluginEntryFile {
			continue
		}
		value, named := options[hostCommandOption]
		if !named {
			return nil, false, nil
		}
		return decodeHostCommand(value)
	}
	return nil, false, nil
}

// decodePluginEntry reads one entry of the host's resolved plugin list. The
// host carries a bare entry string, or a tuple whose second element is the
// plugin's options object. Anything else is not an entry Concord reads.
func decodePluginEntry(raw json.RawMessage) (entry string, options map[string]json.RawMessage, ok bool) {
	var tuple []json.RawMessage
	if err := json.Unmarshal(raw, &tuple); err == nil {
		if len(tuple) == 0 || json.Unmarshal(tuple[0], &entry) != nil {
			return "", nil, false
		}
		if len(tuple) > 1 {
			_ = json.Unmarshal(tuple[1], &options)
		}
		return entry, options, true
	}
	if err := json.Unmarshal(raw, &entry); err == nil {
		return entry, nil, true
	}
	return "", nil, false
}

// decodeHostCommand validates the host_command value: a non-empty JSON
// array of non-empty strings, run as argv with no shell and no placeholders.
// Concord appends its fixed arguments after the operator's argv, so the
// option owns the executable and its fixed prefix alone.
func decodeHostCommand(value json.RawMessage) ([]string, bool, error) {
	var argv []string
	if err := json.Unmarshal(value, &argv); err != nil {
		return nil, true, &hostCommandInvalidError{Problem: "is malformed", Cause: err}
	}
	if len(argv) == 0 {
		return nil, true, &hostCommandInvalidError{Problem: "is malformed: the array is empty"}
	}
	for i, element := range argv {
		if element == "" {
			return nil, true, &hostCommandInvalidError{Problem: fmt.Sprintf("is malformed: element %d is empty", i)}
		}
	}
	return argv, true, nil
}

func equalArgv(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// hostSessionHostCommand is the production wiring for the session's host
// command resolution: it probes the host in the resolved session directory
// (CD-0093 D2).
func hostSessionHostCommand(ctx context.Context, dir string) (hostCommandResolution, error) {
	return resolveHostCommand(ctx, dir, probeHostConfig)
}

// probeHostConfig runs the supplied host argv in dir and returns the
// configuration document the host prints. The document goes to a temporary
// file rather than a pipe. The host exits without draining stdout, so a
// pipe returns exactly one buffer — 65536 bytes of a document measured at
// 584613 here — and the truncation surfaces as a JSON parse error rather
// than as a short read. A regular file has no such boundary.
func probeHostConfig(ctx context.Context, argv []string, dir string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, hostRegistryProbeTimeout)
	defer cancel()
	sink, err := os.CreateTemp("", "concord-host-config-*.json")
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = sink.Close()
		_ = os.Remove(sink.Name())
	}()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the caller supplies the operator's validated host_command argv or the fixed bare probe; this call does not invoke a shell.
	cmd.Dir = dir
	// Only stdout carries the document. Host plugins log to stderr, and
	// mixing the two would corrupt the JSON.
	cmd.Stdout = sink
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if err := sink.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(sink.Name())
}
