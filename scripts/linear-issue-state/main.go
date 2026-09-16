package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sharper-flow/concord/internal/linearclient"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: linear-issue-state <issue-identifier> [...]")
		os.Exit(2)
	}
	client, err := linearclient.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	states := make(map[string]string, len(os.Args)-1)
	for _, identifier := range os.Args[1:] {
		issue, err := client.GetIssue(context.Background(), identifier)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", identifier, err)
			os.Exit(1)
		}
		switch issue.StateType {
		case "completed", "canceled":
			states[identifier] = "closed"
		case "backlog", "unstarted", "started", "triage":
			states[identifier] = "open"
		default:
			fmt.Fprintf(os.Stderr, "%s: unexpected Linear state type %q\n", identifier, issue.StateType)
			os.Exit(1)
		}
	}

	if err := json.NewEncoder(os.Stdout).Encode(states); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
