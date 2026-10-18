package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// hostRegistryProbeTimeout bounds each probe. The host resolves plugins and
// providers before it prints, so this is generous relative to the measured
// cost; it exists to turn a hung host into a typed, retryable
// hostProbeInterruptedError rather than a probe that never returns.
const hostRegistryProbeTimeout = 60 * time.Second

// defaultHostCommand names the calling host binary the registry probe runs.
var defaultHostCommand = []string{"opencode"}

// hostCommandResolution is the outcome of the one bounded bare registry probe
// in a directory: the probe command, and the registry document the calling
// host resolves there, which the identity verification reads.
type hostCommandResolution struct {
	Command  []string
	Registry []byte
}

// sessionHostCommandFunc resolves the registry document in dir. The directory
// is a parameter so the resolution observes the same resolved directory the
// identity steps use.
type sessionHostCommandFunc func(ctx context.Context, dir string) (hostCommandResolution, error)

// hostConfigProbeFunc runs the supplied host argv in dir and returns the
// configuration document the host prints. It is a parameter so tests can
// supply documents without starting a host process.
type hostConfigProbeFunc func(ctx context.Context, argv []string, dir string) ([]byte, error)

// hostProbeInterruptedError reports a host registry probe that ended before
// the host printed a configuration document: the probe deadline passed, or a
// signal ended the host process. Nothing about the host's configuration
// refused — the same request passes once the host answers — so the failure
// is transient, and session-prepare reports it with the ordinary failure
// status instead of the refusal status. The diagnostic names the deadline or
// the signal that ended the probe.
type hostProbeInterruptedError struct {
	DeadlinePassed bool
	Cause          error
}

func (e *hostProbeInterruptedError) Error() string {
	message := "host registry probe was interrupted"
	if e.DeadlinePassed {
		message += "; the probe deadline passed"
	}
	if e.Cause != nil {
		message += "; cause: " + e.Cause.Error()
	}
	return message
}

func (e *hostProbeInterruptedError) Unwrap() error { return e.Cause }

// probeSignaled reports whether a probe error reports a host process a
// signal ended, including the kill exec.CommandContext delivers when the
// probe's deadline passes. A process cmd.Run waited on reports ExitCode -1
// exactly when a signal ended it (os.ProcessState.ExitCode).
func probeSignaled(probeErr error) bool {
	var exit *exec.ExitError
	if errors.As(probeErr, &exit) && exit.ProcessState != nil {
		return exit.ProcessState.ExitCode() == -1
	}
	return false
}

// hostProbeArgv returns the argv the calling host resolves its configuration
// with.
func hostProbeArgv(command []string) []string {
	return append(append([]string(nil), command...), "debug", "config")
}

// hostSessionPrepareRegistry is the production wiring for session-prepare's
// registry. session-prepare launches nothing: the session it prepares keeps
// running in the host process that called it, and the call inherits that
// process's environment. One bare probe in dir under that environment
// resolves the registry the calling host resolves there (CD-0189 D4).
func hostSessionPrepareRegistry(ctx context.Context, dir string) (hostCommandResolution, error) {
	return sessionPrepareRegistryProbe(ctx, dir, probeHostConfig)
}

// sessionPrepareRegistryProbe is the probe-parameterized core of the
// session-prepare registry resolution, so tests can supply documents without
// starting a host process.
func sessionPrepareRegistryProbe(ctx context.Context, dir string, probe hostConfigProbeFunc) (hostCommandResolution, error) {
	document, err := probe(ctx, hostProbeArgv(defaultHostCommand), dir)
	if err != nil {
		var interrupted *hostProbeInterruptedError
		if errors.As(err, &interrupted) {
			return hostCommandResolution{}, interrupted
		}
		return hostCommandResolution{}, fmt.Errorf("host registry probe failed: %w", err)
	}
	return hostCommandResolution{Command: append([]string(nil), defaultHostCommand...), Registry: document}, nil
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
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is the fixed bare probe; this call does not invoke a shell.
	cmd.Dir = dir
	cmd.Env = os.Environ()
	// Only stdout carries the document. Host plugins log to stderr, and
	// mixing the two would corrupt the JSON.
	cmd.Stdout = sink
	if err := cmd.Run(); err != nil {
		deadlinePassed := errors.Is(ctx.Err(), context.DeadlineExceeded)
		if deadlinePassed || probeSignaled(err) {
			return nil, &hostProbeInterruptedError{DeadlinePassed: deadlinePassed, Cause: err}
		}
		return nil, err
	}
	if err := sink.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(sink.Name())
}
