package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// hostAgentEntry contains only the resolved agent fields Concord reads.
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

// verifyHostRegistersHandle checks the host's resolved registry. A definition
// alone cannot prove registration: configuration can rename, disable or demote
// an agent without changing its file. Unreadable or unsuitable registrations
// refuse; the host otherwise silently substitutes its default agent.
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
