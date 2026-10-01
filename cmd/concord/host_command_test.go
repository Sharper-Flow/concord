package main

import (
	"context"
	"encoding/json"
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
// argv fails the probe, which is the answer an unreachable configured
// command produces.
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

func probeDocument(t *testing.T, document any) string {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// concordTuple renders a resolved plugin list with one Concord tuple whose
// options carry the supplied fields.
func concordTuple(t *testing.T, options map[string]any) string {
	t.Helper()
	return probeDocument(t, map[string]any{
		"agent":  map[string]any{},
		"plugin": []any{[]any{"file:///hosts/tools/concord-plugin.ts", options}},
	})
}

// Without the option, the host command is the bare default, the bare
// document is the registry, and exactly one probe runs. This is the behavior
// every release before the option shipped.
func TestResolveHostCommandDefaultsToTheBareHost(t *testing.T) {
	document := `{"agent":{"concord-1":{"mode":"primary"}}}`
	probe := &probeStub{documents: map[string]string{"opencode": document}}
	resolution, err := resolveHostCommand(context.Background(), "/resolved/dir", probe.probe)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !slices.Equal(resolution.Command, []string{"opencode"}) {
		t.Fatalf("command=%q, want the bare default", resolution.Command)
	}
	if string(resolution.Registry) != document {
		t.Fatalf("registry=%q, want the bare probe's document", resolution.Registry)
	}
	if len(probe.calls) != 1 || !slices.Equal(probe.calls[0], []string{"opencode", "debug", "config"}) {
		t.Fatalf("probes=%q, want exactly the bare probe argv", probe.calls)
	}
}

// The option lives in the options of the Concord plugin tuple. When it is
// present, a second probe runs through the configured command, that
// document becomes the registry, and the launch command is the operator's
// argv for Concord to append its fixed arguments to.
func TestResolveHostCommandReadsTheConcordTupleAndVerifiesThroughIt(t *testing.T) {
	bare := concordTuple(t, map[string]any{"host_command": []string{"host-wrapper", "--profile", "work"}})
	configured := probeDocument(t, map[string]any{
		"agent":  map[string]any{"concord-1": map[string]any{"mode": "primary"}},
		"plugin": []any{[]any{"file:///hosts/tools/concord-plugin.ts", map[string]any{"host_command": []string{"host-wrapper", "--profile", "work"}}}},
	})
	probe := &probeStub{documents: map[string]string{"opencode": bare, "host-wrapper": configured}}
	resolution, err := resolveHostCommand(context.Background(), "/resolved/dir", probe.probe)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !slices.Equal(resolution.Command, []string{"host-wrapper", "--profile", "work"}) {
		t.Fatalf("command=%q, want the configured argv", resolution.Command)
	}
	if string(resolution.Registry) != configured {
		t.Fatalf("registry came from the bare probe, want the configured command's document")
	}
	if len(probe.calls) != 2 || !slices.Equal(probe.calls[1], []string{"host-wrapper", "--profile", "work", "debug", "config"}) {
		t.Fatalf("probes=%q, want the second probe through the configured command", probe.calls)
	}
}

// A plugin tuple the host resolved for another plugin carries no
// host_command Concord may read, and a bare entry names no options at all.
func TestResolveHostCommandIgnoresForeignTuplesAndBareEntries(t *testing.T) {
	t.Run("option on a foreign tuple is ignored", func(t *testing.T) {
		document := probeDocument(t, map[string]any{
			"agent":  map[string]any{},
			"plugin": []any{[]any{"file:///operator/other-plugin.ts", map[string]any{"host_command": []string{"not-concords"}}}},
		})
		probe := &probeStub{documents: map[string]string{"opencode": document}}
		resolution, err := resolveHostCommand(context.Background(), "", probe.probe)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !slices.Equal(resolution.Command, defaultHostCommand) {
			t.Fatalf("command=%q, want the bare default", resolution.Command)
		}
	})
	t.Run("bare Concord entry names no options", func(t *testing.T) {
		document := probeDocument(t, map[string]any{
			"agent":  map[string]any{},
			"plugin": []any{"file:///hosts/tools/concord-plugin.ts"},
		})
		probe := &probeStub{documents: map[string]string{"opencode": document}}
		if _, err := resolveHostCommand(context.Background(), "", probe.probe); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	})
}

// A plugin list that carries more than one entry whose file basename names
// the Concord plugin leaves which tuple is Concord's genuinely ambiguous.
// Either order refuses with a diagnostic naming host_command and starts no
// host: a first match without the option must not mask a configured tuple,
// and the resolution never falls back to the bare host.
func TestResolveHostCommandRefusesTwoConcordEntriesInEitherOrder(t *testing.T) {
	withOption := func() []any {
		return []any{[]any{"file:///hosts/tools/concord-plugin.ts", map[string]any{"host_command": []string{"host-wrapper"}}}}
	}
	withoutOption := func(install string) []any {
		return []any{[]any{"file://" + install + "/concord-plugin.ts", map[string]any{}}}
	}
	cases := []struct {
		name  string
		order []any
	}{
		{name: "unconfigured entry first", order: append(withoutOption("/stale/install"), withOption()...)},
		{name: "configured entry first", order: append(withOption(), withoutOption("/stale/install")...)},
		{name: "neither entry names the option", order: append(withoutOption("/stale/install"), withoutOption("/other/install")...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document := probeDocument(t, map[string]any{"agent": map[string]any{}, "plugin": tc.order})
			probe := &probeStub{documents: map[string]string{"opencode": document}}
			_, err := resolveHostCommand(context.Background(), "", probe.probe)
			if err == nil {
				t.Fatal("resolution accepted an ambiguous Concord plugin list")
			}
			if !strings.Contains(err.Error(), "host_command") || !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("diagnostic %q does not name host_command and the ambiguity", err.Error())
			}
			if len(probe.calls) != 1 {
				t.Fatalf("probes=%d, want the refusal before any second probe", len(probe.calls))
			}
		})
	}
}

// A matching tuple whose options element is present but is not a JSON object
// is not an entry with no options: silently reading it that way would turn a
// configured command into a bare launch. The resolution refuses with a
// diagnostic naming host_command. A foreign tuple with a broken options
// element is none of Concord's to read and stays ignored.
func TestResolveHostCommandRefusesAMatchingTupleWhoseOptionsAreNotAnObject(t *testing.T) {
	cases := []struct {
		name    string
		options any
	}{
		{name: "string", options: "work"},
		{name: "array", options: []any{"work"}},
		{name: "null", options: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document := probeDocument(t, map[string]any{
				"agent":  map[string]any{},
				"plugin": []any{[]any{"file:///hosts/tools/concord-plugin.ts", tc.options}},
			})
			probe := &probeStub{documents: map[string]string{"opencode": document}}
			_, err := resolveHostCommand(context.Background(), "", probe.probe)
			if err == nil {
				t.Fatal("resolution accepted a Concord tuple whose options element is not an object")
			}
			if !strings.Contains(err.Error(), "host_command") {
				t.Fatalf("diagnostic %q does not name host_command", err.Error())
			}
			if len(probe.calls) != 1 {
				t.Fatalf("probes=%d, want the refusal before any second probe", len(probe.calls))
			}
		})
	}
	t.Run("a foreign tuple with a non-object options element is ignored", func(t *testing.T) {
		document := probeDocument(t, map[string]any{
			"agent":  map[string]any{},
			"plugin": []any{[]any{"file:///operator/other-plugin.ts", "not an object"}},
		})
		probe := &probeStub{documents: map[string]string{"opencode": document}}
		resolution, err := resolveHostCommand(context.Background(), "", probe.probe)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !slices.Equal(resolution.Command, defaultHostCommand) {
			t.Fatalf("command=%q, want the bare default", resolution.Command)
		}
	})
}

// A present but malformed value refuses with a diagnostic naming
// host_command, and no second probe runs: CD-0049 D4 admits no degraded
// start, and the diagnostic is the only place the operator can see why.
func TestResolveHostCommandRefusesAMalformedValue(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{name: "empty array", value: []string{}},
		{name: "null", value: nil},
		{name: "bare string", value: "opencode"},
		{name: "number", value: 42},
		{name: "object", value: map[string]any{"argv": []string{"opencode"}}},
		{name: "empty element", value: []string{"host-wrapper", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &probeStub{documents: map[string]string{"opencode": concordTuple(t, map[string]any{"host_command": tc.value})}}
			_, err := resolveHostCommand(context.Background(), "", probe.probe)
			if err == nil {
				t.Fatal("resolution accepted a malformed host_command")
			}
			if !strings.Contains(err.Error(), "host_command") || !strings.Contains(err.Error(), "malformed") {
				t.Fatalf("diagnostic %q does not name host_command and the malformation", err.Error())
			}
			if len(probe.calls) != 1 {
				t.Fatalf("probes=%d, want the refusal before any second probe", len(probe.calls))
			}
		})
	}
}

// The second probe closes the verified-versus-executing gap: a wrapper may
// resolve a different configuration than the bare host, so its own document
// must carry the identical value. Absent, different, and unreadable each
// refuse.
func TestResolveHostCommandRefusesASecondDocumentThatDisagrees(t *testing.T) {
	configuredCommand := []string{"host-wrapper", "--profile", "work"}
	bare := concordTuple(t, map[string]any{"host_command": configuredCommand})
	cases := []struct {
		name      string
		second    string
		failProbe bool
		fragment  string
	}{
		{
			name:     "second document names no host_command",
			second:   `{"agent":{}}`,
			fragment: "absent from the configured command's own document",
		},
		{
			name:     "second document names a different command",
			second:   concordTuple(t, map[string]any{"host_command": []string{"other-wrapper"}}),
			fragment: "but [\"other-wrapper\"]",
		},
		{
			name:     "second document is unreadable",
			second:   "{not json",
			fragment: "could not be read from the configured command's own document",
		},
		{
			name:      "second probe cannot run",
			second:    "",
			failProbe: true,
			fragment:  "could not be probed through the configured command",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &probeStub{documents: map[string]string{"opencode": bare, "host-wrapper": tc.second}, failOnCall: 2}
			if !tc.failProbe {
				probe.failOnCall = 0
			}
			_, err := resolveHostCommand(context.Background(), "", probe.probe)
			if err == nil {
				t.Fatal("resolution accepted a configured command its own document contradicts")
			}
			if !strings.Contains(err.Error(), "host_command") || !strings.Contains(err.Error(), tc.fragment) {
				t.Fatalf("diagnostic %q omits host_command or %q", err.Error(), tc.fragment)
			}
		})
	}
}

// An unreadable bootstrap document is not an absent option. Concord cannot
// establish what the host resolves, so the resolution refuses rather than
// launching a command it did not verify.
func TestResolveHostCommandRefusesAnUnreadableBootstrapDocument(t *testing.T) {
	t.Run("probe fails", func(t *testing.T) {
		probe := &probeStub{documents: map[string]string{}, failOnCall: 1}
		_, err := resolveHostCommand(context.Background(), "", probe.probe)
		if err == nil || !strings.Contains(err.Error(), "host registry probe failed") {
			t.Fatalf("err=%v, want the probe failure", err)
		}
	})
	t.Run("document is malformed", func(t *testing.T) {
		probe := &probeStub{documents: map[string]string{"opencode": "{not json"}}
		_, err := resolveHostCommand(context.Background(), "", probe.probe)
		if err == nil || !strings.Contains(err.Error(), "unreadable document") {
			t.Fatalf("err=%v, want the unreadable-document refusal", err)
		}
	})
}

// A probe that ends before the host prints is transient, and the resolution
// carries the typed interrupted-probe error through both probes: the bare
// probe's failure is not a refused document, and a configured command that
// has not answered yet is not a refused host_command option.
func TestResolveHostCommandPreservesAnInterruptedProbe(t *testing.T) {
	t.Run("bare probe", func(t *testing.T) {
		probe := func(context.Context, []string, string) ([]byte, error) {
			return nil, &hostProbeInterruptedError{DeadlinePassed: true, Cause: errors.New("signal: killed")}
		}
		_, err := resolveHostCommand(context.Background(), "/resolved/dir", probe)
		var interrupted *hostProbeInterruptedError
		if !errors.As(err, &interrupted) {
			t.Fatalf("bare probe error=%v, want the typed interrupted-probe error", err)
		}
		var invalid *hostCommandInvalidError
		if errors.As(err, &invalid) {
			t.Fatalf("bare probe error=%v, want it not to be a host_command refusal", err)
		}
	})
	t.Run("configured command probe", func(t *testing.T) {
		bare := concordTuple(t, map[string]any{"host_command": []string{"host-wrapper"}})
		probe := func(_ context.Context, argv []string, _ string) ([]byte, error) {
			if argv[0] == "opencode" {
				return []byte(bare), nil
			}
			return nil, &hostProbeInterruptedError{Cause: errors.New("signal: killed")}
		}
		_, err := resolveHostCommand(context.Background(), "/resolved/dir", probe)
		var interrupted *hostProbeInterruptedError
		if !errors.As(err, &interrupted) {
			t.Fatalf("configured probe error=%v, want the typed interrupted-probe error", err)
		}
		var invalid *hostCommandInvalidError
		if errors.As(err, &invalid) {
			t.Fatalf("configured probe error=%v, want it not to be a host_command refusal", err)
		}
		if !strings.Contains(err.Error(), "signal: killed") {
			t.Fatalf("diagnostic %q, want it to name the signal", err.Error())
		}
	})
}

// probeHostConfig classifies the probe's own deadline as an interrupted
// probe: the error is the typed interrupted-probe error, it names the
// deadline, and it is not a host_command refusal.
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
	var invalid *hostCommandInvalidError
	if errors.As(err, &invalid) {
		t.Fatalf("probe error=%v, want it not to be a host_command refusal", err)
	}
}
