package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// hostAgentEntry contains only the resolved agent fields Concord reads.
type hostAgentEntry struct {
	ID   *string `json:"id"`
	Mode *string `json:"mode"`
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
// refuse; the host otherwise silently substitutes its default agent. The host
// omits disabled agents from the registry. `hidden` only removes an agent from
// host pickers, so it does not refuse.
func verifyHostRegistersHandle(host hostCommandResolution, handle string) error {
	printed := strings.Join(hostProbeArgv(host.Command), " ")
	document := host.Registry
	if len(document) == 0 {
		return &hostRegistrationError{Handle: handle, Observed: "registry unreadable", Probe: printed,
			Cause: fmt.Errorf("host printed no registry document")}
	}
	var resolved []hostAgentEntry
	if err := json.Unmarshal(document, &resolved); err != nil {
		return &hostRegistrationError{Handle: handle, Observed: "registry unreadable", Probe: printed, Cause: err}
	}
	if resolved == nil {
		return &hostRegistrationError{Handle: handle, Observed: "registry unreadable", Probe: printed,
			Cause: fmt.Errorf("registry document is not an agent array")}
	}
	seen := make(map[string]bool, len(resolved))
	mode := ""
	for _, entry := range resolved {
		if entry.ID == nil || *entry.ID == "" || entry.Mode == nil || (*entry.Mode != "primary" && *entry.Mode != "all" && *entry.Mode != "subagent") || seen[*entry.ID] {
			return &hostRegistrationError{Handle: handle, Observed: "registry unreadable", Probe: printed,
				Cause: fmt.Errorf("registry contains an invalid agent identity or mode")}
		}
		seen[*entry.ID] = true
		if *entry.ID == handle {
			mode = *entry.Mode
		}
	}
	if mode == "" {
		return &hostRegistrationError{Handle: handle, Observed: "not registered (missing or disabled)", Probe: printed}
	}
	if mode == "subagent" {
		return &hostRegistrationError{Handle: handle, Observed: "registered as a subagent", Probe: printed}
	}
	return nil
}
