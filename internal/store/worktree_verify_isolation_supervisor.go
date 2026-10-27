package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const nativeSupervisorArg = "--concord-private-native-isolation-supervisor"
const nativeSupervisorInputLimit = 1024 * 1024
const nativeSupervisorStatusLimit = 8192
const nativeSupervisorSetupTimeout = 10 * time.Second

// Argv is the producer's complete bwrap vector, without the launcher or the
// supervisor-owned status/gate options. Fingerprint binds the export/policy
// inventory; its construction and drift checks belong to the native owner.
type nativeStageIsolation struct {
	Launcher    string
	Argv        []string
	Fingerprint string
	Profile     string
}

type nativeSupervisorPlan struct {
	Isolation nativeStageIsolation
	Directory string
	Env       []string
	ParentPID int
}

type nativeSupervisorResult struct {
	Fingerprint string
	Ready       bool
	Exit        int
	Established bool
	Reaped      bool
	Detail      string
}

func nativeIsolationUnavailable(detail string) error {
	return oracleFailure(KindUnavailable, "required native isolation unavailable: "+detail, "restore the pinned isolation environment and prepare again")
}

func nativeIsolationSupervisorIdentity() ([]NativeOracleFile, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	return []NativeOracleFile{
		{Path: "isolation-supervisor", SHA256: nativeDigest(data)},
		{Path: "isolation-supervisor-path", SHA256: nativeDigest([]byte(exe))},
	}, nil
}

func validateNativeIsolation(p *nativeStageIsolation, argv []string) error {
	if p.Launcher != "/usr/bin/bwrap" || !strings.HasPrefix(p.Fingerprint, "sha256:") || len(p.Fingerprint) != 71 || p.Profile == "" || strings.TrimSpace(p.Profile) != p.Profile || p.Profile == "unconfined" || strings.ContainsAny(p.Profile, "\r\n\x00") {
		return errors.New("missing fixed launcher, policy fingerprint or intended active profile")
	}
	if _, err := hex.DecodeString(p.Fingerprint[7:]); err != nil {
		return errors.New("invalid isolation fingerprint")
	}
	// Parse option arity: a mount source named like a flag must not count as
	// a namespace option. Other export/layout validation belongs to its owner.
	arity := map[string]int{
		"--unshare-user": 0, "--unshare-pid": 0, "--unshare-net": 0,
		"--unshare-ipc": 0, "--unshare-uts": 0, "--disable-userns": 0,
		"--assert-userns-disabled": 0, "--new-session": 0, "--die-with-parent": 0,
		"--cap-drop": 1, "--uid": 1, "--gid": 1, "--hostname": 1,
		"--ro-bind": 2, "--bind": 2, "--symlink": 2, "--dir": 1,
		"--proc": 1, "--dev": 1, "--tmpfs": 1, "--remount-ro": 1,
		"--chdir": 1, "--clearenv": 0, "--setenv": 2, "--unsetenv": 1,
	}
	seen := map[string]bool{}
	for i := 0; i < len(p.Argv); {
		flag := p.Argv[i]
		if flag == "--" {
			if !slices.Equal(p.Argv[i+1:], argv) || len(argv) == 0 {
				return errors.New("isolation workload differs from the native stage")
			}
			for _, required := range []string{"--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--disable-userns", "--new-session", "--cap-drop=ALL"} {
				if !seen[required] {
					return fmt.Errorf("missing mandatory launcher option %s", required)
				}
			}
			return nil
		}
		n, ok := arity[flag]
		if !ok || i+n >= len(p.Argv) {
			return fmt.Errorf("unsupported or incomplete launcher option %q", flag)
		}
		seen[flag] = true
		if flag == "--cap-drop" {
			if p.Argv[i+1] != "ALL" {
				return errors.New("only complete capability dropping is supported")
			}
			seen["--cap-drop=ALL"] = true
		}
		i += n + 1
	}
	return errors.New("missing explicit workload separator")
}

// RunNativeIsolationSupervisor routes only the private same-executable role.
// Entrypoints call it before stdin, environment scrubbing or store access.
// It consumes only inherited producer pipes, never a public JSON command.
func RunNativeIsolationSupervisor() (int, bool) {
	if len(os.Args) != 2 || os.Args[1] != nativeSupervisorArg {
		return 0, false
	}
	for fd := 3; fd <= 5; fd++ {
		unix.CloseOnExec(fd)
	}
	planFile := os.NewFile(3, "native-plan")
	lifetime := os.NewFile(4, "native-parent-lifetime")
	resultFile := os.NewFile(5, "native-result")
	defer planFile.Close()
	defer lifetime.Close()
	defer resultFile.Close()
	result := nativeSupervisorResult{Exit: -1}
	if err := nativeSupervisorPipe(planFile); err != nil {
		return 125, true
	}
	if nativeSupervisorPipe(lifetime) != nil || nativeSupervisorPipe(resultFile) != nil {
		return 125, true
	}
	// A surviving supervisor must not retain the invoking shell's terminal or
	// signal ownership. The parent starts it in a fresh session without PDEATHSIG.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer cancel()
	go func() {
		var b [1]byte
		_, _ = lifetime.Read(b[:])
		cancel()
		_ = planFile.Close()
	}()
	_ = planFile.SetReadDeadline(time.Now().Add(nativeSupervisorSetupTimeout))
	raw, err := io.ReadAll(io.LimitReader(planFile, nativeSupervisorInputLimit+1))
	var plan nativeSupervisorPlan
	if err == nil && len(raw) <= nativeSupervisorInputLimit {
		err = decodeNativeSupervisorJSON(raw, &plan)
	} else if err == nil {
		err = errors.New("oversized supervisor plan")
	}
	if err == nil && (plan.ParentPID != os.Getppid() || ctx.Err() != nil) {
		err = errors.New("native caller lifetime lost before launch")
	}
	if err == nil {
		var workload []string
		for i, arg := range plan.Isolation.Argv {
			if arg == "--" {
				workload = plan.Isolation.Argv[i+1:]
				break
			}
		}
		err = validateNativeIsolation(&plan.Isolation, workload)
	}
	if err == nil {
		err = nativeNoInheritableDescriptors()
	}
	if err == nil {
		err = nativeIsolationLauncherVersion(plan.Isolation.Launcher)
	}
	if err == nil {
		signal.Reset(syscall.SIGCHLD)
		err = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
	}
	result.Fingerprint = plan.Isolation.Fingerprint
	if err == nil {
		result = superviseNativeIsolation(ctx, plan, bindNativeIsolationInit, observeNativeIsolation, func() error {
			return json.NewEncoder(resultFile).Encode(nativeSupervisorResult{Fingerprint: plan.Isolation.Fingerprint, Ready: true, Exit: -1})
		})
	} else {
		result.Detail = err.Error()
	}
	if err := json.NewEncoder(resultFile).Encode(result); err != nil {
		return 125, true
	}
	if result.Detail != "" || !result.Established || !result.Reaped {
		return 125, true
	}
	return 0, true
}

func nativeSupervisorPipe(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("supervisor control must be a pipe")
	}
	return nil
}

// Go's fork/exec remaps ExtraFiles but does not close arbitrary non-CLOEXEC
// descriptors. Refuse ambient inheritance instead of assuming exec scrubs it.
func nativeNoInheritableDescriptors() error {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd < 3 {
			continue
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err == unix.EBADF {
			continue
		} // the directory reader is already closed
		if err != nil {
			return err
		}
		if flags&unix.FD_CLOEXEC == 0 {
			return fmt.Errorf("ambient inheritable descriptor %d", fd)
		}
	}
	return nil
}

func validateNativeLauncherFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&(os.ModeSetuid|os.ModeSetgid|0022) != 0 || info.Mode()&0111 == 0 || st.Uid != 0 {
		return errors.New("launcher must be the root-owned non-setuid fixed regular executable")
	}
	return nil
}

func decodeNativeSupervisorJSON(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("extra supervisor JSON input")
	}
	return nil
}

func runNativeIsolatedStage(ctx context.Context, dir string, argv, env []string, stdin io.Reader, capture *nativeStreamCapture, launched func(), isolation *nativeStageIsolation) (int, error) {
	capture.isolationEstablished, capture.isolationReaped = false, false
	if err := validateNativeIsolation(isolation, argv); err != nil {
		return -1, nativeIsolationUnavailable(err.Error())
	}
	if err := nativeIsolationLauncherVersion(isolation.Launcher); err != nil {
		return -1, nativeIsolationUnavailable(err.Error())
	}
	// No arbitrary reader-copy goroutine may outlive the native owner. Native
	// isolated stages currently consume EOF only; a future input path needs its
	// own bounded producer-owned pipe and lifetime proof.
	if stdin != nil {
		return -1, nativeIsolationUnavailable("isolated stages require producer EOF stdin")
	}
	if err := nativeNoInheritableDescriptors(); err != nil {
		return -1, nativeIsolationUnavailable(err.Error())
	}
	var err error
	var files []*os.File
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	pipe := func() (*os.File, *os.File, error) {
		r, w, err := os.Pipe()
		if err == nil {
			files = append(files, r, w)
		}
		return r, w, err
	}
	planR, planW, err := pipe()
	if err != nil {
		return -1, err
	}
	lifeR, lifeW, err := pipe()
	if err != nil {
		return -1, err
	}
	resultR, resultW, err := pipe()
	if err != nil {
		return -1, err
	}
	outR, outW, err := pipe()
	if err != nil {
		return -1, err
	}
	errR, errW, err := pipe()
	if err != nil {
		return -1, err
	}
	plan, err := json.Marshal(nativeSupervisorPlan{Isolation: *isolation, Directory: dir, Env: env, ParentPID: os.Getpid()})
	if err != nil || len(plan) > nativeSupervisorInputLimit {
		return -1, nativeIsolationUnavailable("supervisor plan exceeds its bound")
	}
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	cmd := exec.Command("/proc/self/exe", nativeSupervisorArg) //nolint:gosec // same loaded executable, private fixed role.
	cmd.Env = []string{}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, outW, errW
	cmd.ExtraFiles = []*os.File{planR, lifeR, resultW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return -1, nativeIsolationUnavailable(err.Error())
	}
	for _, f := range []*os.File{planR, lifeR, resultW, outW, errW} {
		_ = f.Close()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	copied := make(chan error, 2)
	readers := []*os.File{outR, errR}
	for i, r := range readers {
		go func() {
			_, err := io.Copy(nativeCaptureWriter{capture: capture, stderr: i == 1, cancel: cancel}, r)
			if err != nil {
				cancel()
			}
			copied <- err
		}()
	}
	planDone := make(chan error, 1)
	go func() {
		_, err := planW.Write(plan)
		_ = planW.Close()
		planDone <- err
	}()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	type supervisorReply struct {
		result nativeSupervisorResult
		err    error
	}
	resultDone := make(chan supervisorReply, 1)
	go func() {
		r, err := readNativeSupervisorResult(resultR, isolation.Fingerprint, launched)
		if err != nil {
			cancel()
		}
		resultDone <- supervisorReply{r, err}
	}()
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-runCtx.Done():
		// EOF is a cancellation request to the surviving cleanup owner, not a
		// signal to bwrap's startup gate. Never SIGKILL the cleanup supervisor.
		_ = lifeW.Close()
		select {
		case waitErr = <-waited:
		case <-time.After(nativeSupervisorSetupTimeout + 2*worktreeVerifyFinalizeTimeout):
			for _, r := range readers {
				_ = r.Close()
			}
			_ = resultR.Close()
			_ = planW.Close()
			_ = drainNativeStreams(copied, readers)
			capture.complete = false
			return -1, nativeIsolationUnavailable("supervisor did not prove teardown before deadline; cleanup owner was not killed")
		}
	}
	_ = lifeW.Close()
	_ = planW.Close()
	planErr := <-planDone
	// No descendant receives the result descriptor; a missing EOF is a
	// controller/protocol failure, never successful native evidence.
	_ = resultR.SetReadDeadline(time.Now().Add(worktreeVerifyFinalizeTimeout))
	reply := <-resultDone
	cut := drainNativeStreams(copied, readers)
	result := reply.result
	if waitErr != nil || planErr != nil || reply.err != nil || !result.Established || !result.Reaped || result.Detail != "" || result.Exit < 0 || result.Exit > 255 {
		capture.complete = false
		return -1, nativeIsolationUnavailable(fmt.Sprintf("supervisor setup/lifetime/teardown proof failed: %s (controller: %v; protocol: %v)", result.Detail, waitErr, reply.err))
	}
	if cut != nil {
		capture.complete = false
		return -1, nativeIsolationUnavailable(cut.Error())
	}
	// Output overflow and caller cancellation remain unavailable evidence,
	// but must not erase independently observed teardown. The scratch owner
	// distinguishes them from a controller whose cleanup is still pending.
	nativeIsolationRecordProof(capture, result)
	if runCtx.Err() != nil {
		return -1, runCtx.Err()
	}
	return result.Exit, nil
}

func nativeIsolationRecordProof(capture *nativeStreamCapture, result nativeSupervisorResult) {
	if result.Established && result.Reaped && result.Detail == "" {
		capture.isolationEstablished = true
		capture.isolationReaped = true
	}
}

func readNativeSupervisorResult(r io.Reader, fingerprint string, launched func()) (nativeSupervisorResult, error) {
	limited := &io.LimitedReader{R: r, N: nativeSupervisorStatusLimit + 1}
	d := json.NewDecoder(limited)
	d.DisallowUnknownFields()
	var result nativeSupervisorResult
	ready, final := false, false
	for {
		var message nativeSupervisorResult
		err := d.Decode(&message)
		if err == io.EOF {
			if !final || limited.N <= 0 || (result.Established && !ready) {
				return result, errors.New("incomplete or excessive supervisor result")
			}
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if final || message.Fingerprint != fingerprint {
			return result, errors.New("unbound or extra supervisor result")
		}
		if message.Ready {
			if ready || message.Exit != -1 || message.Established || message.Reaped || message.Detail != "" {
				return result, errors.New("inconsistent supervisor gate notification")
			}
			ready = true
			if launched != nil {
				launched()
			}
		} else {
			result, final = message, true
		}
	}
}

type nativeLauncherStatus struct {
	Child      int
	Exit       *int
	Namespaces map[string]uint64
	Err        error
}

func parseNativeLauncherStatus(raw []byte) nativeLauncherStatus {
	s := nativeLauncherStatus{Namespaces: map[string]uint64{}}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		s.Err = errors.New("invalid launcher status object")
		return s
	}
	seen := map[string]bool{}
	for d.More() {
		t, err := d.Token()
		key, ok := t.(string)
		if err != nil || !ok || seen[key] {
			s.Err = errors.New("duplicate or malformed launcher status field")
			return s
		}
		seen[key] = true
		var value uint64
		if err := d.Decode(&value); err != nil {
			s.Err = err
			return s
		}
		switch key {
		case "child-pid":
			if value < 2 || value > 1<<31-1 {
				s.Err = errors.New("invalid launcher child PID")
				return s
			}
			s.Child = int(value)
		case "exit-code":
			if value > 255 {
				s.Err = errors.New("invalid launcher exit status")
				return s
			}
			n := int(value)
			s.Exit = &n
		case "cgroup-namespace", "ipc-namespace", "mnt-namespace", "net-namespace", "pid-namespace", "uts-namespace":
			if value == 0 {
				s.Err = errors.New("invalid namespace identity")
				return s
			}
			s.Namespaces[strings.TrimSuffix(key, "-namespace")] = value
		default:
			s.Err = fmt.Errorf("unsupported launcher status field %q", key)
			return s
		}
	}
	if _, err := d.Token(); err != nil || d.Decode(new(any)) != io.EOF {
		s.Err = errors.New("incomplete or extra launcher status")
		return s
	}
	if s.Exit != nil {
		if len(seen) != 1 {
			s.Err = errors.New("mixed launcher startup and exit status")
		}
	} else {
		if s.Child == 0 {
			s.Err = errors.New("missing namespace init identity")
		}
		for _, ns := range []string{"ipc", "mnt", "net", "pid", "uts"} {
			if s.Namespaces[ns] == 0 {
				s.Err = fmt.Errorf("missing %s namespace status", ns)
			}
		}
	}
	return s
}

func readNativeLauncherStatus(r io.Reader, out chan<- nativeLauncherStatus) {
	defer close(out)
	reader := bufio.NewReader(io.LimitReader(r, nativeSupervisorStatusLimit+1))
	total := 0
	for i := 0; ; i++ {
		line, err := reader.ReadString('\n')
		total += len(line)
		if err == io.EOF && len(line) == 0 {
			return
		}
		if err != nil || total > nativeSupervisorStatusLimit || i >= 2 {
			out <- nativeLauncherStatus{Err: errors.New("partial, excessive or unbounded launcher status")}
			return
		}
		s := parseNativeLauncherStatus([]byte(line))
		if (i == 0 && s.Child == 0) || (i == 1 && s.Exit == nil) {
			s.Err = errors.New("out-of-order launcher status")
		}
		out <- s
		if s.Err != nil {
			return
		}
	}
}

type nativeIsolationObserver func(int, int, nativeLauncherStatus, *os.File, nativeStageIsolation) (bool, error)

func superviseNativeIsolation(ctx context.Context, plan nativeSupervisorPlan, bindInit func(int, int) (int, error), observe nativeIsolationObserver, notify func() error) (result nativeSupervisorResult) {
	result = nativeSupervisorResult{Fingerprint: plan.Isolation.Fingerprint, Exit: -1}
	statusR, statusW, err := os.Pipe()
	if err != nil {
		result.Detail = err.Error()
		return
	}
	defer statusR.Close()
	defer statusW.Close()
	gateR, gateW, err := os.Pipe()
	if err != nil {
		result.Detail = err.Error()
		return
	}
	defer gateR.Close()
	// gateW closes only after the cleanup proof, including abort before the
	// first status byte. EOF on --block-fd otherwise starts the workload.
	args := append([]string{"--json-status-fd", "3", "--block-fd", "4"}, plan.Isolation.Argv...)
	cmd := exec.Command(plan.Isolation.Launcher, args...) //nolint:gosec // fixed producer launcher/argv, validated by private ingress.
	cmd.Dir, cmd.Env = plan.Directory, plan.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{statusW, gateR}
	outerFD := -1
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, PidFD: &outerFD}
	if err := cmd.Start(); err != nil {
		_ = gateW.Close()
		result.Detail = err.Error()
		return
	}
	defer cmd.Process.Release()
	defer unix.Close(outerFD)
	_ = statusW.Close()
	_ = gateR.Close()
	initFD := -1
	released := false
	defer func() {
		if initFD >= 0 {
			defer unix.Close(initFD)
		}
		// Before gate release, bwrap has not forked the workload or detached
		// sessions. Keep the outer PID unreaped through this group signal.
		// Afterwards, only the bound namespace init can close detached trees.
		if !released {
			_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		}
		if initFD >= 0 {
			_ = unix.PidfdSendSignal(initFD, unix.SIGKILL, nil, 0)
		}
		_ = unix.PidfdSendSignal(outerFD, unix.SIGKILL, nil, 0)
		reaped, outerExit, cleanupErr := reapNativeIsolation(cmd.Process.Pid)
		result.Reaped = reaped
		if cleanupErr != nil {
			result.Detail = fmt.Sprintf("%s; teardown: %v", result.Detail, cleanupErr)
		}
		if result.Exit >= 0 && outerExit != result.Exit {
			result.Detail = "launcher exit disagrees with successful-exec status"
		}
		// Reaped ECHILD, not the group signal or outer Wait, is the proof.
		// If it fails, closing this process would release the gate. Keep its
		// writer alive while the cleanup owner retries; report failure first.
		if !reaped {
			// All launch-group members have already received SIGKILL. Do not
			// authorize a gate release while an uninterruptible child remains.
			_ = gateW.SetWriteDeadline(time.Time{})
			for !reaped {
				time.Sleep(worktreeVerifyFinalizeTimeout)
				reaped, _, _ = reapNativeIsolation(cmd.Process.Pid)
			}
		}
		_ = gateW.Close()
	}()
	if outerFD < 0 {
		result.Detail = "launcher pidfd unavailable"
		return
	}
	statuses := make(chan nativeLauncherStatus, 3)
	go readNativeLauncherStatus(statusR, statuses)
	setup := time.NewTimer(nativeSupervisorSetupTimeout)
	defer setup.Stop()
	var start nativeLauncherStatus
	select {
	case <-ctx.Done():
		result.Detail = "caller lifetime ended before startup status"
		return
	case <-setup.C:
		result.Detail = "launcher startup status deadline"
		return
	case s, ok := <-statuses:
		if !ok || s.Err != nil {
			result.Detail = fmt.Sprintf("missing or invalid launcher startup status: %v", s.Err)
			return
		}
		start = s
	}
	initFD, err = bindInit(cmd.Process.Pid, start.Child)
	if err != nil {
		result.Detail = "namespace init pidfd: " + err.Error()
		return
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var pending error
	for !released {
		if ctx.Err() != nil {
			result.Detail = "caller lifetime ended before gate release"
			return
		}
		ready, err := observe(cmd.Process.Pid, start.Child, start, gateW, plan.Isolation)
		if errors.Is(err, errNativeIsolationSetupPending) {
			pending, ready = err, false
		} else if err != nil {
			result.Detail = err.Error()
			return
		}
		if ready {
			if notify != nil {
				if err := notify(); err != nil {
					result.Detail = "caller gate notification failed: " + err.Error()
					return
				}
			}
			if ctx.Err() != nil {
				result.Detail = "caller lifetime ended at gate release"
				return
			}
			if _, err := gateW.Write([]byte{1}); err != nil {
				result.Detail = "gate release failed: " + err.Error()
				return
			}
			released, result.Established = true, true
			break
		}
		select {
		case <-ctx.Done():
			result.Detail = "caller lifetime ended during setup"
			return
		case <-setup.C:
			result.Detail = fmt.Sprintf("launcher did not reach observed setup gate (last pending observation: %v)", pending)
			return
		case <-tick.C:
		}
	}
	var exit *int
	for {
		select {
		case <-ctx.Done():
			result.Detail = "caller cancellation or lifetime loss"
			return
		case s, ok := <-statuses:
			if !ok {
				if exit == nil {
					result.Detail = "launcher omitted successful-exec exit status"
					return
				}
				if err := waitNativePIDFD(ctx, outerFD); err != nil {
					result.Detail = err.Error()
					return
				}
				result.Exit = *exit
				return
			}
			if s.Err != nil || s.Exit == nil || exit != nil {
				result.Detail = "inconsistent launcher completion status"
				return
			}
			exit = s.Exit
		}
	}
}

func waitNativePIDFD(ctx context.Context, fd int) error {
	deadline := time.Now().Add(worktreeVerifyFinalizeTimeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(p, 10); err != nil && err != unix.EINTR {
			return err
		}
		if p[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("launcher did not terminate after exit status")
		}
	}
}

func reapNativeIsolation(outer int) (bool, int, error) {
	deadline := time.Now().Add(worktreeVerifyFinalizeTimeout)
	outerExit := -1
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if err == unix.EINTR {
			continue
		}
		if err == unix.ECHILD {
			return true, outerExit, nil
		}
		if err != nil {
			return false, outerExit, err
		}
		if pid == outer {
			if status.Exited() {
				outerExit = status.ExitStatus()
			} else if status.Signaled() {
				outerExit = 128 + int(status.Signal())
			}
		}
		if pid == 0 {
			if time.Now().After(deadline) {
				return false, outerExit, errors.New("descendants not reaped before teardown deadline")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func nativeProcStatus(pid int) (map[string]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return nil, err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			fields[k] = strings.TrimSpace(v)
		}
	}
	return fields, nil
}

func nativeInitAncestry(outer, init int) error {
	s, err := nativeProcStatus(init)
	if err != nil {
		return err
	}
	ids := strings.Fields(s["NSpid"])
	if s["PPid"] != strconv.Itoa(outer) || len(ids) < 2 || ids[len(ids)-1] != "1" {
		return errors.New("status child is not the unreaped owned namespace init")
	}
	return nil
}

func bindNativeIsolationInit(outer, init int) (int, error) {
	fd, err := unix.PidfdOpen(init, 0)
	if err != nil {
		return -1, err
	}
	// The pidfd is acquired while the fixed init is blocked and unreaped.
	// Revalidate ancestry after acquisition, so a stale/reused status PID
	// cannot authorize release or a signal to an unrelated task.
	if err := nativeInitAncestry(outer, init); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

var errNativeIsolationSetupPending = errors.New("launcher setup is not yet observable")

func observeNativeIsolation(outer, init int, start nativeLauncherStatus, gate *os.File, plan nativeStageIsolation) (bool, error) {
	if err := nativeInitAncestry(outer, init); err != nil {
		return false, err
	}
	// A read of the exact private gate after drop_privs is stronger than
	// early namespace IDs. Until this read, setup may still be incomplete.
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/syscall", init))
	if err != nil {
		// drop_privs() sets dumpable only at the end of setup. Access can
		// become available as the fixed launcher reaches its gate. Never
		// release on an unreadable observation; the setup deadline still
		// refuses a host where the final gate cannot be inspected.
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
			return false, fmt.Errorf("%w: %v", errNativeIsolationSetupPending, err)
		}
		return false, fmt.Errorf("cannot observe private setup gate: %w", err)
	}
	call := strings.Fields(string(b))
	if len(call) < 4 || call[0] != strconv.Itoa(unix.SYS_READ) || call[1] != "0x4" || call[3] != "0x1" {
		return false, nil
	}
	var expected, actual unix.Stat_t
	if err := unix.Fstat(int(gate.Fd()), &expected); err != nil {
		return false, err
	}
	if err := unix.Stat(fmt.Sprintf("/proc/%d/fd/4", init), &actual); err != nil {
		return false, err
	}
	if actual.Ino != expected.Ino || actual.Dev != expected.Dev {
		return false, errors.New("setup gate descriptor identity differs")
	}
	for _, ns := range []string{"user", "mnt", "pid", "net", "ipc", "uts"} {
		var child, self unix.Stat_t
		if err := unix.Stat(fmt.Sprintf("/proc/%d/ns/%s", init, ns), &child); err != nil {
			return false, err
		}
		if err := unix.Stat("/proc/self/ns/"+ns, &self); err != nil {
			return false, err
		}
		if child.Ino == self.Ino || (ns != "user" && child.Ino != start.Namespaces[ns]) {
			return false, fmt.Errorf("unestablished or inconsistent %s namespace", ns)
		}
	}
	for _, pid := range []int{outer, init} {
		label, err := os.ReadFile(fmt.Sprintf("/proc/%d/attr/apparmor/current", pid))
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(string(label)) != plan.Profile {
			return false, errors.New("active intended AppArmor profile differs")
		}
		exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil || filepath.Clean(exe) != plan.Launcher {
			return false, errors.New("owned setup executable differs from launcher")
		}
	}
	s, err := nativeProcStatus(init)
	if err != nil {
		return false, err
	}
	if s["NoNewPrivs"] != "1" {
		return false, errors.New("no_new_privs is not active")
	}
	for _, cap := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		n, err := strconv.ParseUint(s[cap], 16, 64)
		if err != nil || n != 0 {
			return false, fmt.Errorf("setup retained capabilities: %s", cap)
		}
	}
	return true, nil
}
