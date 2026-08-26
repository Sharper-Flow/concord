package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// hostRegistryProbeFunc returns the host's resolved configuration document for
// a working directory. It is a parameter so tests can supply a registry
// without starting a host process.
type hostRegistryProbeFunc func(ctx context.Context, cwd string) ([]byte, error)

// hostRegistryProbeTimeout bounds the probe. The host resolves plugins and
// providers before it prints, so this is generous relative to the measured
// cost; it exists to turn a hung host into a typed refusal rather than a
// session that never starts.
const hostRegistryProbeTimeout = 60 * time.Second

// hostAgentEntry is the part of a resolved agent record Concord reads. The
// host's document carries more; naming only these three keeps the coupling to
// what the decision actually needs.
type hostAgentEntry struct {
	Mode    string `json:"mode"`
	Disable bool   `json:"disable"`
}

// hostConfigDocument is the shape Concord reads out of the host's resolved
// configuration: a map from registered handle to agent record.
type hostConfigDocument struct {
	Agent map[string]hostAgentEntry `json:"agent"`
}

// hostRegistrationError reports a handle the host cannot start as the session
// agent. It names the handle, what the registry showed, and how the registry
// was read, because the host answers an unstartable name by running the
// operator's default agent and exiting zero: nothing downstream can observe
// the substitution, so the diagnostic is the only place it becomes visible.
type hostRegistrationError struct {
	Handle   string
	Observed string
	Probe    string
	Cause    error
}

func (e *hostRegistrationError) Error() string {
	message := fmt.Sprintf(
		"host does not register the orchestrator handle: %s; observed: %s; read by: %s",
		e.Handle, e.Observed, e.Probe,
	)
	if e.Cause != nil {
		message += fmt.Sprintf("; cause: %v", e.Cause)
	}
	return message
}

func (e *hostRegistrationError) Unwrap() error { return e.Cause }

// hostRegistryProbeCommand is the argv Concord runs to read the host's
// resolved configuration. The command prints the merged document, so an agent
// disabled or renamed by configuration rather than by its definition file is
// visible here and nowhere else on disk.
var hostRegistryProbeCommand = []string{"opencode", "debug", "config"}

// probeHostAgentRegistry runs the host's configuration dump in cwd and returns
// its raw document. The host is not asked to interpret anything: it prints
// what it resolved, and Concord reads the agent map out of it.
//
// The document goes to a temporary file rather than a pipe. The host exits
// without draining stdout, so a pipe returns exactly one buffer — 65536 bytes
// of a document measured at 584613 here — and the truncation surfaces as a
// JSON parse error rather than as a short read. A regular file has no such
// boundary, and the parse failure would be indistinguishable from a host that
// genuinely printed nothing.
func probeHostAgentRegistry(ctx context.Context, cwd string) ([]byte, error) {
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
	cmd := exec.CommandContext(ctx, hostRegistryProbeCommand[0], hostRegistryProbeCommand[1:]...)
	cmd.Dir = cwd
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

// verifyHostRegistersHandle establishes that the host will resolve handle to a
// startable session agent before the session selects it.
//
// A resolvable definition file is not proof of registration. Frontmatter
// `name:` renames the handle rather than aliasing it, `disable: true` removes
// the agent, `mode: subagent` demotes it, and configuration layers can do any
// of these without touching the file. None of those states are visible on
// disk, and the host reports none of them at startup, so the registry is the
// only place the property can be established (issue #430).
//
// Every failure refuses. CD-0049 D4 admits no degraded start, and a probe that
// cannot be read leaves the property unestablished rather than satisfied.
func verifyHostRegistersHandle(ctx context.Context, probe hostRegistryProbeFunc, cwd, handle string) error {
	printed := strings.Join(hostRegistryProbeCommand, " ")
	document, err := probe(ctx, cwd)
	if err != nil {
		return &hostRegistrationError{Handle: handle, Observed: "registry unreadable", Probe: printed, Cause: err}
	}
	if len(document) == 0 {
		return &hostRegistrationError{Handle: handle, Observed: "registry unreadable", Probe: printed,
			Cause: fmt.Errorf("host printed no configuration document")}
	}
	var resolved hostConfigDocument
	if err := json.Unmarshal(document, &resolved); err != nil {
		return &hostRegistrationError{Handle: handle, Observed: "registry unreadable", Probe: printed, Cause: err}
	}
	if resolved.Agent == nil {
		return &hostRegistrationError{Handle: handle, Observed: "registry unreadable", Probe: printed,
			Cause: fmt.Errorf("configuration document declares no agent map")}
	}
	entry, ok := resolved.Agent[handle]
	if !ok {
		return &hostRegistrationError{Handle: handle, Observed: "not registered", Probe: printed}
	}
	if entry.Disable {
		return &hostRegistrationError{Handle: handle, Observed: "disabled", Probe: printed}
	}
	// The host records a subagent as `subagent` and a session-startable
	// agent as `primary` or `all`. A subagent cannot hold the session
	// authority the assertion claims for it.
	if entry.Mode == "subagent" {
		return &hostRegistrationError{Handle: handle, Observed: "registered as a subagent", Probe: printed}
	}
	return nil
}
