package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type sessionHandoff struct {
	ProductID string
	WorkID    string
	Prompt    string
	// ProjectID selects a member Project; empty keeps the primary landing.
	ProjectID string
	Agent     string
}

const defaultSessionAgent = "concord-1"

var executablePath = os.Executable

// sessionCommand starts the core bootstrap, which owns landing and continuity.
// It never constructs a host command or terminal placement request.
func sessionCommand(handoff sessionHandoff) (*exec.Cmd, error) {
	executable, err := executablePath()
	if err != nil || executable == "" {
		return nil, fmt.Errorf("cannot identify the running Concord binary")
	}
	cmd := exec.Command(executable, "session") //nolint:gosec // os.Executable and fixed argv; no shell.
	cmd.Env = handoffEnv(handoff)
	return cmd, nil
}

func handoffEnv(handoff sessionHandoff) []string {
	env := make([]string, 0, len(os.Environ())+5)
	inheritedAgent := ""
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, selectedAgentEnv+"=") {
			inheritedAgent = strings.TrimPrefix(value, selectedAgentEnv+"=")
			env = append(env, value)
			continue
		}
		if strings.HasPrefix(value, selectedProductEnv+"=") || strings.HasPrefix(value, selectedWorkEnv+"=") || strings.HasPrefix(value, selectedPromptEnv+"=") || strings.HasPrefix(value, selectedProjectEnv+"=") || strings.HasPrefix(value, selectedProjectIDEnv+"=") {
			continue
		}
		env = append(env, value)
	}
	if inheritedAgent == "" && handoff.Agent != "" {
		env = append(env, selectedAgentEnv+"="+handoff.Agent)
	}
	env = append(env, selectedProductEnv+"="+handoff.ProductID)
	if handoff.ProjectID != "" {
		env = append(env, selectedProjectIDEnv+"="+handoff.ProjectID)
	}
	if handoff.WorkID != "" {
		env = append(env, selectedWorkEnv+"="+handoff.WorkID)
	}
	if handoff.Prompt != "" {
		env = append(env, selectedPromptEnv+"="+handoff.Prompt)
	}
	return env
}
