package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// hostAgentEntry is the part of a resolved agent record Concord reads. The
// host's document carries more; naming only these three keeps the coupling to
// what the decision actually needs.
type hostAgentEntry struct {
	Mode    string `json:"mode"`
	Disable bool   `json:"disable"`
}

// hostConfigDocument is the shape Concord reads out of the host's resolved
// configuration: a map from registered handle to agent record, and the
// plugin list whose Concord tuple may carry the host_command option
// (CD-0189).
type hostConfigDocument struct {
	Agent  map[string]hostAgentEntry `json:"agent"`
	Plugin []json.RawMessage         `json:"plugin"`
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

// verifyHostRegistersHandle establishes that the host will resolve handle to a
// startable session agent before the session selects it. The registry it
// reads is the document the host-command resolution carried back: when a
// host_command is configured, that document is the one the configured
// command itself resolves, so the verification constrains the registry that
// governs the session (CD-0093 D2).
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
func verifyHostRegistersHandle(host hostCommandResolution, handle string) error {
	printed := strings.Join(hostProbeArgv(host.Command), " ")
	document := host.Registry
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
