package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// probeStub records the argv of every probe it receives and answers each one
// with the document registered for the argv's first element. An unregistered
// argv fails the probe, which is the answer an unreachable host produces.
type probeStub struct {
	calls      [][]string
	documents  map[string]string
	failOnCall int // 1-based call number whose probe fails; 0 fails none
}

func (p *probeStub) probe(_ context.Context, argv []string, _ string) ([]byte, error) {
	p.calls = append(p.calls, append([]string(nil), argv...))
	if p.failOnCall == len(p.calls) {
		return nil, errors.New("probe transport failed")
	}
	document, ok := p.documents[argv[0]]
	if !ok {
		return nil, errors.New("no document for " + argv[0])
	}
	return []byte(document), nil
}

// The session-prepare registry resolution runs exactly one bare probe, and the document that
// probe printed is the registry the identity verification reads.
func TestSessionPrepareRegistryRunsOneBareProbe(t *testing.T) {
	document := `{"agent":{"concord-1":{"mode":"primary"}}}`
	probe := &probeStub{documents: map[string]string{"opencode": document}}
	resolution, err := sessionPrepareRegistryProbe(context.Background(), "/resolved/dir", probe.probe)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !slices.Equal(resolution.Command, defaultHostCommand) {
		t.Fatalf("command=%q, want the bare probe command", resolution.Command)
	}
	if string(resolution.Registry) != document {
		t.Fatalf("registry=%q, want the bare probe's document", resolution.Registry)
	}
	if len(probe.calls) != 1 || !slices.Equal(probe.calls[0], []string{"opencode", "debug", "config"}) {
		t.Fatalf("probes=%q, want exactly the bare probe argv", probe.calls)
	}
}

// A probe that ends before the host prints is transient, and the resolution
// carries the typed interrupted-probe error through: the probe's failure is
// not a refused configuration.
func TestSessionPrepareRegistryPreservesAnInterruptedProbe(t *testing.T) {
	probe := func(context.Context, []string, string) ([]byte, error) {
		return nil, &hostProbeInterruptedError{DeadlinePassed: true, Cause: errors.New("signal: killed")}
	}
	_, err := sessionPrepareRegistryProbe(context.Background(), "/resolved/dir", probe)
	var interrupted *hostProbeInterruptedError
	if !errors.As(err, &interrupted) {
		t.Fatalf("probe error=%v, want the typed interrupted-probe error", err)
	}
	if !interrupted.DeadlinePassed {
		t.Fatalf("interrupted=%+v, want the deadline flag", interrupted)
	}
	if !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("diagnostic %q, want it to name the signal", err.Error())
	}
}

// A failed bare probe is not an absent constraint: the resolution refuses
// with the probe's failure rather than carrying an empty registry.
func TestSessionPrepareRegistryCarriesAProbeFailure(t *testing.T) {
	probe := &probeStub{documents: map[string]string{}, failOnCall: 1}
	_, err := sessionPrepareRegistryProbe(context.Background(), "/resolved/dir", probe.probe)
	if err == nil || !strings.Contains(err.Error(), "probe transport failed") {
		t.Fatalf("err=%v, want the probe failure", err)
	}
}

// probeHostConfig classifies the probe's own deadline as an interrupted
// probe: the error is the typed interrupted-probe error and it names the
// deadline.
func TestTimedOutRegistryProbeIsTheTypedInterruptedError(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\nwhile true; do :; done\n"
	if err := os.WriteFile(filepath.Join(binDir, "sleepy-host"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	probeCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := probeHostConfig(probeCtx, hostProbeArgv([]string{"sleepy-host"}), t.TempDir())
	var interrupted *hostProbeInterruptedError
	if !errors.As(err, &interrupted) || !interrupted.DeadlinePassed {
		t.Fatalf("probe error=%v, want the typed interrupted-probe error naming the deadline", err)
	}
	if !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("diagnostic %q, want it to name the probe deadline", err.Error())
	}
}
