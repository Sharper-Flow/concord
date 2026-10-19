package store

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

type WorktreeGoSuiteResult struct {
	Closure      *WorktreeGoSourceClosure `json:"closure,omitempty"`
	Protocol     string                   `json:"protocol"`
	Command      []string                 `json:"command"`
	Environment  []string                 `json:"environment"`
	StreamDigest string                   `json:"stream_digest"`
	Packages     []WorktreeGoSuitePackage `json:"packages"`
	Complete     bool                     `json:"complete"`
	Detail       string                   `json:"detail,omitempty"`
}

const goSourceInputsUnproved GoSourceClosureReason = "source_inputs_unproved"

type WorktreeGoSuitePackage struct {
	Package           string   `json:"package"`
	PassedCount       int      `json:"passed_count"`
	SkippedCount      int      `json:"skipped_count"`
	ObservedTestNames []string `json:"observed_test_names"`
	Passed            bool     `json:"passed"`
}

type nativeGoTestEvent struct {
	Action, Package, Test, ImportPath string
}

type worktreeGoPackageState struct {
	WorktreeGoSuitePackage
	tests    map[string]byte
	terminal bool
}

func observeWorktreeGoSuite(reader io.Reader, wanted map[string]bool) (*WorktreeGoSuiteResult, error) {
	digest := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(reader, digest))
	scanner.Buffer(make([]byte, 4096), nativeOracleStreamLimit)
	packages := map[string]*worktreeGoPackageState{}
	tests := 0
	for scanner.Scan() {
		var event nativeGoTestEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("malformed Go JSON: %w", err)
		}
		if event.Action == "build-output" && event.ImportPath != "" && event.Package == "" && event.Test == "" {
			continue
		}
		if event.Action == "fail" || event.Action == "build-fail" {
			return nil, errors.New("the Go suite contains a test or build failure")
		}
		if event.Package == "" || len(event.Package) > 512 || len(event.Test) > 4096 {
			return nil, errors.New("the Go event has no bounded package/test identity")
		}
		pkg := packages[event.Package]
		if event.Action == "start" {
			if pkg != nil || event.Test != "" || len(packages) >= 1024 {
				return nil, errors.New("duplicate or unavailable Go package start")
			}
			packages[event.Package] = &worktreeGoPackageState{
				WorktreeGoSuitePackage: WorktreeGoSuitePackage{Package: event.Package}, tests: map[string]byte{},
			}
			continue
		}
		if pkg == nil || pkg.terminal {
			return nil, errors.New("the Go event precedes start or follows package termination")
		}
		if event.Action == "output" {
			continue
		}
		if event.Test == "" {
			if event.Action != "pass" && event.Action != "skip" || event.Action == "skip" && len(pkg.tests) > 0 {
				return nil, errors.New("conflicting Go package termination")
			}
			for _, state := range pkg.tests {
				if state != 2 {
					return nil, errors.New("the Go package terminated before its tests")
				}
			}
			pkg.Passed, pkg.terminal = event.Action == "pass", true
			continue
		}
		root, _, nested := strings.Cut(event.Test, "/")
		if nested && pkg.tests[root] != 1 {
			return nil, errors.New("the Go subtest has no running parent")
		}
		state := pkg.tests[event.Test]
		switch event.Action {
		case "run":
			if state != 0 || tests >= 65536 {
				return nil, errors.New("duplicate or unavailable Go test run")
			}
			pkg.tests[event.Test], tests = 1, tests+1
		case "pause":
			if state != 1 {
				return nil, errors.New("the Go test pauses without running")
			}
			pkg.tests[event.Test] = 3
		case "cont":
			if state != 3 {
				return nil, errors.New("the Go test continues without pausing")
			}
			pkg.tests[event.Test] = 1
		case "pass", "skip":
			if state != 1 {
				return nil, errors.New("unordered or duplicate Go test disposition")
			}
			pkg.tests[event.Test] = 2
			if !nested {
				if event.Action == "pass" {
					pkg.PassedCount++
					if wanted[event.Test] {
						pkg.ObservedTestNames = append(pkg.ObservedTestNames, event.Test)
					}
				} else {
					pkg.SkippedCount++
				}
			}
		default:
			return nil, errors.New("unsupported Go test action")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("incomplete Go JSON stream: %w", err)
	}
	if len(packages) == 0 {
		return nil, errors.New("the Go stream contains no packages")
	}
	result := &WorktreeGoSuiteResult{Protocol: "native_go_suite_v1", StreamDigest: fmt.Sprintf("sha256:%x", digest.Sum(nil)), Complete: true}
	for _, pkg := range packages {
		if !pkg.terminal {
			return nil, errors.New("the Go stream has an unfinished package")
		}
		slices.Sort(pkg.ObservedTestNames)
		result.Packages = append(result.Packages, pkg.WorktreeGoSuitePackage)
	}
	slices.SortFunc(result.Packages, func(a, b WorktreeGoSuitePackage) int { return strings.Compare(a.Package, b.Package) })
	return result, nil
}

func worktreeGoSuiteWanted(ctx context.Context, q queryer, workID string, command []string) (map[string]bool, bool, error) {
	record, _, err := readWorkSize(ctx, q, workID)
	if err != nil {
		return nil, false, err
	}
	checks, _ := workflowFootprintChecks(record.Footprint)
	wanted := map[string]bool{}
	selected := false
	for _, check := range checks {
		if derived, ok := workflowGoCheckCommand(check); ok && slices.Equal(derived, command) {
			selected = true
		}
		if check.Kind == "go_test" {
			if _, name, ok := workflowTestCheckParts(check.Value); ok {
				wanted[name] = true
			}
		}
	}
	return wanted, selected, nil
}

func runWorktreeGoSuite(ctx context.Context, dir string, command []string, wanted map[string]bool, maxOutput int) (WorktreeVerifyResult, error) {
	environment, fixed := worktreeGoSuiteEnvironment()
	packagePattern := command[len(command)-1]
	beforeClosure, beforeErr := observeWorktreeGoSources(ctx, dir, environment, packagePattern)
	reader, writer := io.Pipe()
	var observation *WorktreeGoSuiteResult
	var observeErr error
	done := make(chan struct{})
	go func() {
		observation, observeErr = observeWorktreeGoSuite(reader, wanted)
		if observeErr != nil {
			_, _ = io.Copy(io.Discard, reader)
		}
		_ = reader.Close()
		close(done)
	}()
	capture := &nativeStreamCapture{complete: true, stdoutObserver: writer, discardStdout: true}
	code, err := runNativeOracleStage(ctx, dir, command, environment, nil, capture, nil)
	_ = writer.Close()
	<-done
	if observation == nil {
		observation = &WorktreeGoSuiteResult{Protocol: "native_go_suite_v1"}
	}
	observation.Command, observation.Environment = command, fixed
	afterClosure, afterErr := observeWorktreeGoSources(ctx, dir, environment, packagePattern)
	observation.Closure = &WorktreeGoSourceClosure{Reason: goSourceInputsUnproved, Residual: goSourceRuntimeResidual}
	if beforeClosure != nil && !beforeClosure.Complete {
		observation.Closure = beforeClosure
	}
	if afterClosure != nil && !afterClosure.Complete {
		observation.Closure = afterClosure
	}
	if beforeErr == nil && afterErr == nil && beforeClosure != nil && afterClosure != nil && *beforeClosure == *afterClosure {
		observation.Closure = beforeClosure
	}
	if observeErr != nil || err != nil || code != 0 || !capture.complete {
		observation.Complete, observation.Packages = false, nil
		observation.Detail = "Go execution or observation did not complete successfully"
		if observeErr != nil {
			observation.Detail = observeErr.Error()
		}
	}
	output, truncated := boundText(string(capture.preview), maxOutput)
	return WorktreeVerifyResult{Command: command, ExitCode: code, Output: output,
		OutputTruncated: truncated || capture.previewTruncated, GoSuite: observation}, err
}
