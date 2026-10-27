package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func nativeIsolationTestPlan(argv []string) nativeStageIsolation {
	return nativeStageIsolation{
		Launcher: "/usr/bin/bwrap", Fingerprint: nativeDigest([]byte("synthetic export")),
		Profile: "synthetic-bwrap (enforce)",
		Argv:    append([]string{"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--disable-userns", "--new-session", "--cap-drop", "ALL", "--"}, argv...),
	}
}

func TestNativeIsolationSupervisorRequiredNeverFallsBack(t *testing.T) {
	argv := []string{"sh", "-c", "printf unconfined"}
	for _, tc := range []struct {
		name       string
		isolation  []*nativeStageIsolation
		wantOutput string
		wantErr    bool
	}{
		{"absent", nil, "unconfined", false},
		{"nil", []*nativeStageIsolation{nil}, "unconfined", false},
		{"required invalid", []*nativeStageIsolation{{}}, "", true},
		{"multiple", []*nativeStageIsolation{nil, nil}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &nativeStreamCapture{complete: true}
			started := false
			exit, err := runNativeOracleStage(context.Background(), t.TempDir(), argv, nil, nil, capture, func() { started = true }, tc.isolation...)
			if (err != nil) != tc.wantErr || string(capture.stdout) != tc.wantOutput || started == tc.wantErr || (!tc.wantErr && exit != 0) {
				t.Fatalf("exit=%d err=%v output=%q started=%v", exit, err, capture.stdout, started)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "required native isolation unavailable") {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeIsolationSupervisorPlanIsClosed(t *testing.T) {
	argv := []string{"/synthetic/node", "test.mjs"}
	valid := nativeIsolationTestPlan(argv)
	if err := validateNativeIsolation(&valid, argv); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--share-net", "--unshare-user-try", "--unshare-all", "--as-pid-1", "--sync-fd", "--info-fd", "--json-status-fd", "--block-fd", "--userns", "--pidns", "--args", "--cap-add", "--not-a-security-boundary"} {
		t.Run(flag, func(t *testing.T) {
			p := valid
			p.Argv = append([]string{flag, "3"}, valid.Argv...)
			if err := validateNativeIsolation(&p, argv); err == nil {
				t.Fatal("accepted forbidden option")
			}
		})
	}
	for _, flag := range []string{"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--disable-userns", "--new-session"} {
		t.Run("missing "+flag, func(t *testing.T) {
			p := valid
			p.Argv = slices.DeleteFunc(slices.Clone(valid.Argv), func(s string) bool { return s == flag })
			if err := validateNativeIsolation(&p, argv); err == nil {
				t.Fatal("accepted missing boundary")
			}
		})
	}
	t.Run("flag-like mount source is not a namespace", func(t *testing.T) {
		p := valid
		p.Argv = append([]string{"--ro-bind", "--unshare-net", "/synthetic"}, slices.DeleteFunc(slices.Clone(valid.Argv), func(s string) bool { return s == "--unshare-net" })...)
		if err := validateNativeIsolation(&p, argv); err == nil {
			t.Fatal("counted mount operand as isolation flag")
		}
	})
	for _, change := range []func(*nativeStageIsolation){
		func(p *nativeStageIsolation) { p.Launcher = "/synthetic/bwrap" },
		func(p *nativeStageIsolation) { p.Fingerprint = "" },
		func(p *nativeStageIsolation) { p.Profile = "unconfined" },
		func(p *nativeStageIsolation) { p.Profile += "\n" },
		func(p *nativeStageIsolation) { p.Argv = append(slices.Clone(p.Argv), "extra") },
		func(p *nativeStageIsolation) { p.Argv = slices.Clone(p.Argv); p.Argv[8] = "CHOWN" },
	} {
		p := valid
		change(&p)
		if err := validateNativeIsolation(&p, argv); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}

func TestNativeIsolationSupervisorStatusProtocol(t *testing.T) {
	start := `{"child-pid":1234,"ipc-namespace":1,"mnt-namespace":2,"net-namespace":3,"pid-namespace":4,"uts-namespace":5}`
	if s := parseNativeLauncherStatus([]byte(start)); s.Err != nil || s.Child != 1234 || len(s.Namespaces) != 5 {
		t.Fatalf("startup: %+v", s)
	}
	for _, raw := range []string{
		`{}`, `null`, `[]`, `{"child-pid":1234}`, strings.Replace(start, "1234", "1", 1),
		strings.Replace(start, `"child-pid":1234`, `"child-pid":1234,"child-pid":2345`, 1),
		strings.Replace(start, `"net-namespace":3`, `"net-namespace":0`, 1),
		strings.Replace(start, `"net-namespace":3`, `"other-namespace":3`, 1),
		`{"exit-code":256}`, `{"exit-code":-1}`, `{"exit-code":0,"child-pid":1234}`,
		`{"exit-code":0,"exit-code":1}`, `{"exit-code":0.0}`, `{"exit-code":0} {}`,
	} {
		if s := parseNativeLauncherStatus([]byte(raw)); s.Err == nil {
			t.Fatalf("accepted %s: %+v", raw, s)
		}
	}
	for _, tc := range []struct {
		name, stream string
		valid        bool
	}{
		{"complete", start + "\n{\"exit-code\":7}\n", true},
		{"partial startup", start, false},
		{"partial completion", start + "\n{\"exit-code\":0}", false},
		{"completion first", "{\"exit-code\":0}\n", false},
		{"duplicate startup", start + "\n" + start + "\n", false},
		{"extra", start + "\n{\"exit-code\":0}\n{}\n", false},
		{"oversized", strings.Repeat(" ", nativeSupervisorStatusLimit) + start + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan nativeLauncherStatus, 3)
			readNativeLauncherStatus(strings.NewReader(tc.stream), ch)
			bad := false
			count := 0
			for s := range ch {
				count++
				bad = bad || s.Err != nil
			}
			if tc.valid == bad || (tc.valid && count != 2) {
				t.Fatalf("valid=%v bad=%v count=%d", tc.valid, bad, count)
			}
		})
	}
}

func TestNativeIsolationSupervisorIdentity(t *testing.T) {
	files, err := nativeIsolationSupervisorIdentity()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "isolation-supervisor" || files[0].SHA256 != nativeDigest(b) || files[1].Path != "isolation-supervisor-path" {
		t.Fatalf("%+v", files)
	}
}

// The mock only waits on a pipe. It never creates a sandbox or executes any
// installed launcher. Its marker detects an abort that releases gate EOF.
func runNativeIsolationSupervisorTestRole() (int, bool) {
	if len(os.Args) == 8 && os.Args[1] == "--json-status-fd" && os.Args[5] == "--native-isolation-mock" {
		mode, marker := os.Args[6], os.Args[7]
		status, gate := os.NewFile(3, "mock-status"), os.NewFile(4, "mock-gate")
		switch mode {
		case "partial":
			_, _ = status.WriteString(`{"child-pid":`)
			_ = status.Close()
		case "foreign-init":
			_, _ = fmt.Fprintf(status, "{\"child-pid\":%d,\"ipc-namespace\":1,\"mnt-namespace\":2,\"net-namespace\":3,\"pid-namespace\":4,\"uts-namespace\":5}\n", os.Getppid())
		case "normal", "nonzero", "cancel-after-gate", "missing-completion", "mismatch":
			_, _ = fmt.Fprintf(status, "{\"child-pid\":%d,\"ipc-namespace\":1,\"mnt-namespace\":2,\"net-namespace\":3,\"pid-namespace\":4,\"uts-namespace\":5}\n", os.Getpid())
		}
		var b [1]byte
		_, _ = gate.Read(b[:])
		_ = os.WriteFile(marker, []byte("gate released"), 0600)
		if mode == "cancel-after-gate" {
			time.Sleep(time.Hour)
		}
		exit := 0
		if mode == "nonzero" || mode == "mismatch" {
			exit = 7
		}
		if mode != "missing-completion" {
			_, _ = fmt.Fprintf(status, "{\"exit-code\":%d}\n", exit)
		}
		_ = status.Close()
		if mode == "mismatch" {
			return 0, true
		}
		return exit, true
	}
	if len(os.Args) == 4 && os.Args[1] == "--native-isolation-mock-controller" {
		if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
			return 90, true
		}
		mode, marker := os.Args[2], os.Args[3]
		if mode == "caller-death" {
			return nativeIsolationMockCallerDeath(), true
		}
		budget := 3 * time.Second
		if mode == "cancel-before-status" {
			budget = 200 * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		plan := nativeSupervisorPlan{Isolation: nativeStageIsolation{Launcher: os.Args[0], Argv: []string{"--native-isolation-mock", mode, marker}, Fingerprint: "synthetic"}}
		bind, observe := bindNativeIsolationInit, nativeIsolationObserver(observeNativeIsolation)
		if slices.Contains([]string{"normal", "nonzero", "cancel-after-gate", "missing-completion", "mismatch"}, mode) {
			// This mocks the namespace proof, not an isolation qualification.
			// The real observer is never replaced at the production ingress.
			bind = func(outer, init int) (int, error) {
				if outer != init {
					return -1, fmt.Errorf("mock identity differs")
				}
				return unix.PidfdOpen(init, 0)
			}
			observe = func(int, int, nativeLauncherStatus, *os.File, nativeStageIsolation) (bool, error) {
				if mode == "cancel-after-gate" {
					time.AfterFunc(50*time.Millisecond, cancel)
				}
				return true, nil
			}
		}
		result := superviseNativeIsolation(ctx, plan, bind, observe, nil)
		_ = json.NewEncoder(os.Stdout).Encode(result)
		return 0, true
	}
	if len(os.Args) == 2 && os.Args[1] == "--native-isolation-mock-caller-dies" {
		unix.CloseOnExec(3)
		result := os.NewFile(3, "caller-death-result")
		planR, planW, err := os.Pipe()
		if err != nil {
			return 90, true
		}
		lifeR, lifeW, err := os.Pipe()
		if err != nil {
			return 90, true
		}
		cmd := exec.Command(os.Args[0], nativeSupervisorArg)
		cmd.ExtraFiles = []*os.File{planR, lifeR, result}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return 90, true
		}
		// Keep the plan incomplete until caller exit. No launcher can run.
		time.Sleep(50 * time.Millisecond)
		_, _ = planW, lifeW
		return 0, true
	}
	if len(os.Args) == 2 && os.Args[1] == "--native-isolation-inheritable-fd" {
		fd, err := unix.Open("/dev/null", unix.O_RDONLY, 0)
		if err != nil {
			return 90, true
		}
		defer unix.Close(fd)
		if err := nativeNoInheritableDescriptors(); err == nil {
			return 91, true
		}
		return 0, true
	}
	return 0, false
}

func nativeIsolationMockCallerDeath() int {
	r, w, err := os.Pipe()
	if err != nil {
		return 90
	}
	defer r.Close()
	defer w.Close()
	_ = r.SetReadDeadline(time.Now().Add(3 * time.Second))
	caller := exec.Command(os.Args[0], "--native-isolation-mock-caller-dies")
	caller.ExtraFiles = []*os.File{w}
	if err := caller.Start(); err != nil {
		return 90
	}
	_ = w.Close()
	if err := caller.Wait(); err != nil {
		return 91
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return 92
	}
	var result nativeSupervisorResult
	if err := json.Unmarshal(b, &result); err != nil || result.Detail == "" || result.Established {
		return 93
	}
	reaped, _, err := reapNativeIsolation(caller.Process.Pid)
	if !reaped || err != nil {
		return 94
	}
	return 0
}

func TestNativeIsolationSupervisorCompletionStateMachine(t *testing.T) {
	for _, mode := range []string{"normal", "nonzero", "cancel-after-gate", "missing-completion", "mismatch"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "released")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "--native-isolation-mock-controller", mode, marker)
			b, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("mock: %v %s", err, b)
			}
			var result nativeSupervisorResult
			if err := json.Unmarshal(b, &result); err != nil {
				t.Fatalf("%v %s", err, b)
			}
			if !result.Established || !result.Reaped {
				t.Fatalf("lost mocked setup/reap: %+v", result)
			}
			good := mode == "normal" || mode == "nonzero"
			if (result.Detail == "") != good {
				t.Fatalf("mock result: %+v", result)
			}
			if mode == "nonzero" && result.Exit != 7 {
				t.Fatalf("lost workload failure: %+v", result)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("mock never crossed gate: %v", err)
			}
		})
	}
}

func TestNativeIsolationSupervisorSurvivesCallerDeath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "--native-isolation-mock-controller", "caller-death", "unused")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("caller-death controller: %v %s", err, b)
	}
}

func TestNativeIsolationSupervisorResultProtocol(t *testing.T) {
	fingerprint := nativeDigest([]byte("synthetic"))
	ready, _ := json.Marshal(nativeSupervisorResult{Fingerprint: fingerprint, Ready: true, Exit: -1})
	final, _ := json.Marshal(nativeSupervisorResult{Fingerprint: fingerprint, Exit: 7, Established: true, Reaped: true})
	for _, tc := range []struct {
		name, stream string
		valid        bool
		started      int
	}{
		{"complete", string(ready) + string(final), true, 1},
		{"no gate proof", string(final), false, 0},
		{"no completion", string(ready), false, 1},
		{"duplicate gate", string(ready) + string(ready) + string(final), false, 1},
		{"extra completion", string(ready) + string(final) + string(final), false, 1},
		{"fingerprint differs", strings.Replace(string(ready)+string(final), fingerprint, "different", 1), false, 0},
		{"partial", string(ready) + "{", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := 0
			result, err := readNativeSupervisorResult(strings.NewReader(tc.stream), fingerprint, func() { started++ })
			if (err == nil) != tc.valid || started != tc.started || (tc.valid && result.Exit != 7) {
				t.Fatalf("result=%+v err=%v started=%d", result, err, started)
			}
		})
	}
}

func TestNativeIsolationSupervisorAbortHoldsGateUntilReaped(t *testing.T) {
	for _, mode := range []string{"cancel-before-status", "partial", "foreign-init"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "gate-released")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "--native-isolation-mock-controller", mode, marker)
			b, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("mock controller: %v\n%s", err, b)
			}
			var result nativeSupervisorResult
			if err := json.Unmarshal(b, &result); err != nil {
				t.Fatalf("result: %v\n%s", err, b)
			}
			if !result.Reaped || result.Established || result.Detail == "" || result.Exit != -1 {
				t.Fatalf("%+v", result)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("abort released gate before termination: %v", err)
			}
		})
	}
}

func TestNativeIsolationSupervisorRejectsAmbientFD(t *testing.T) {
	cmd := exec.Command(os.Args[0], "--native-isolation-inheritable-fd")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FD check: %v\n%s", err, b)
	}
}

func TestNativeIsolationSupervisorPrivateRoleRejectsMalformedInput(t *testing.T) {
	for _, raw := range []string{`{}`, `{"unknown":true}`, strings.Repeat("x", nativeSupervisorInputLimit+1)} {
		t.Run(strconv.Itoa(len(raw)), func(t *testing.T) {
			planR, planW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer planR.Close()
			defer planW.Close()
			lifeR, lifeW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer lifeR.Close()
			defer lifeW.Close()
			resultR, resultW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer resultR.Close()
			defer resultW.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], nativeSupervisorArg)
			cmd.ExtraFiles = []*os.File{planR, lifeR, resultW}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = planR.Close()
			_ = lifeR.Close()
			_ = resultW.Close()
			_, _ = io.WriteString(planW, raw)
			_ = planW.Close()
			b, _ := io.ReadAll(resultR)
			err = cmd.Wait()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 125 {
				t.Fatalf("private role err=%v stdout=%s stderr=%s", err, b, &stderr)
			}
			var result nativeSupervisorResult
			if err := json.Unmarshal(b, &result); err != nil {
				t.Fatalf("result %v %s", err, b)
			}
			if result.Reaped || result.Established || result.Detail == "" {
				t.Fatalf("qualified malformed plan: %+v", result)
			}
		})
	}
}
