// This host fixture observes the launch environment without a Go TestMain.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func main() {
	if err := record(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func record() error {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	branch, err := exec.Command("git", "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return err
	}
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	child := map[string]any{
		"Directory": directory, "Branch": strings.TrimSpace(string(branch)),
		"Head": strings.TrimSpace(string(head)), "Input": string(input),
		"Client": os.Getenv("CONCORD_CLIENT_REF"), "Release": os.Getenv("CONCORD_BIN"),
		"Secret": os.Getenv("OUTSIDE_REPAIR_TEST_SECRET"),
	}
	managed := []string{}
	for _, name := range []string{"CONCORD_SELECTED_WORK_ID", "CONCORD_SESSION_REF", "CONCORD_LEASE_ID", "CONCORD_DB_PATH"} {
		if value := os.Getenv(name); value != "" {
			managed = append(managed, name+"="+value)
		}
	}
	child["ManagedIdentity"] = managed
	data, err := json.Marshal(child)
	if err != nil {
		return err
	}
	file, err := os.OpenFile("outside-repair-child.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, "child-tty-output")
	return err
}
