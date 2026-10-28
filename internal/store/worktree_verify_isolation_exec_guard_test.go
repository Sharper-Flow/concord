package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The tracer runs in a separate test process so wait4 cannot consume other
// tests' children. It stops every exec syscall before execution and follows
// forks/clones. An accidental --version/ldd/probe cannot run the vulnerable
// host launcher, even when testing a regression in its static identity reader.
func TestNativeIsolationLauncherExecGuard(t *testing.T) {
	const modeKey = "TEST_CONCORD_NATIVE_ISOLATION_EXEC_GUARD"
	switch os.Getenv(modeKey) {
	case "read":
		marker := os.Getenv(modeKey + "_MARKER")
		if err := os.WriteFile(marker, []byte("armed"), 0600); err != nil {
			t.Fatal(err)
		}
		_ = nativeIsolationLauncherVersion(nativeIsolationLauncher)
		if err := os.WriteFile(marker+"-done", []byte("done"), 0600); err != nil {
			t.Fatal(err)
		}
		return
	case "trace":
		if err := nativeIsolationTraceStaticReader(modeKey); err != nil {
			t.Fatal(err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/proc/self/exe", "-test.run=^TestNativeIsolationLauncherExecGuard$", "-test.timeout=45s", "-test.v")
	cmd.Env = append(os.Environ(), modeKey+"=trace")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("static-reader exec observation failed: %v\n%s", err, output)
	}
}

func nativeIsolationTraceStaticReader(modeKey string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	root, err := os.MkdirTemp("", "concord-static-reader-guard-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	marker := filepath.Join(root, "armed")
	cmd := exec.Command("/proc/self/exe", "-test.run=^TestNativeIsolationLauncherExecGuard$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), modeKey+"=read", modeKey+"_MARKER="+marker)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Ptrace: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	tracees := map[int]bool{cmd.Process.Pid: true}
	defer func() {
		for pid := range tracees {
			_ = unix.Kill(pid, unix.SIGKILL)
		}
		for {
			var status unix.WaitStatus
			_, err := unix.Wait4(-1, &status, unix.WALL, nil)
			if err == unix.ECHILD {
				break
			}
			if err != nil && err != unix.EINTR {
				break
			}
		}
		_ = cmd.Process.Release()
	}()
	timer := time.AfterFunc(20*time.Second, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	var status unix.WaitStatus
	if _, err := unix.Wait4(cmd.Process.Pid, &status, 0, nil); err != nil {
		return err
	}
	if !status.Stopped() {
		return fmt.Errorf("reader was not stopped before startup")
	}
	options := unix.PTRACE_O_TRACESYSGOOD | unix.PTRACE_O_TRACECLONE | unix.PTRACE_O_TRACEFORK | unix.PTRACE_O_TRACEVFORK | unix.PTRACE_O_EXITKILL
	if err := unix.PtraceSetOptions(cmd.Process.Pid, options); err != nil {
		return err
	}
	if err := unix.PtraceSyscall(cmd.Process.Pid, 0); err != nil {
		return err
	}
	stops := 0
	for {
		pid, err := unix.Wait4(-1, &status, unix.WALL, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if status.Exited() || status.Signaled() {
			delete(tracees, pid)
			if pid == cmd.Process.Pid {
				if !status.Exited() || status.ExitStatus() != 0 || stops < 2 {
					return fmt.Errorf("guarded reader exit=%v syscall_stops=%d", status, stops)
				}
				if _, err := os.Stat(marker + "-done"); err != nil {
					return fmt.Errorf("reader did not reach the observed scope: %w", err)
				}
				return nil
			}
			continue
		}
		if !status.Stopped() {
			return fmt.Errorf("unknown reader wait state")
		}
		tracees[pid] = true
		if status.TrapCause() == unix.PTRACE_EVENT_CLONE || status.TrapCause() == unix.PTRACE_EVENT_FORK || status.TrapCause() == unix.PTRACE_EVENT_VFORK {
			child, err := unix.PtraceGetEventMsg(pid)
			if err != nil {
				return err
			}
			tracees[int(child)] = true
		}
		signal := 0
		if status.StopSignal() == unix.SIGTRAP|0x80 {
			stops++
			var regs unix.PtraceRegs
			if err := unix.PtraceGetRegs(pid, &regs); err != nil {
				return err
			}
			if regs.Orig_rax == unix.SYS_EXECVE || regs.Orig_rax == unix.SYS_EXECVEAT {
				// PTRACE_GET_SYSCALL_INFO distinguishes entry from the
				// successful initial test-executable exec's exit stop.
				var info [8]byte
				_, _, errno := unix.Syscall6(unix.SYS_PTRACE, unix.PTRACE_GET_SYSCALL_INFO, uintptr(pid), uintptr(len(info)), uintptr(unsafe.Pointer(&info[0])), 0, 0)
				if errno != 0 {
					return errno
				}
				if info[0] == 1 {
					if regs.Orig_rax == unix.SYS_EXECVEAT {
						return fmt.Errorf("unexpected execveat blocked before execution")
					}
					var path []byte
					for i := 0; i < 4096; i++ {
						var b [1]byte
						if _, err := unix.PtracePeekData(pid, uintptr(regs.Rdi)+uintptr(i), b[:]); err != nil {
							return err
						}
						if b[0] == 0 {
							break
						}
						path = append(path, b[0])
					}
					_, armedErr := os.Stat(marker)
					_, doneErr := os.Stat(marker + "-done")
					if string(path) == nativeIsolationLauncher || (armedErr == nil && os.IsNotExist(doneErr)) {
						return fmt.Errorf("static identity reader attempted exec %q; blocked before execution", path)
					}
				}
			}
		} else if status.StopSignal() != unix.SIGTRAP && status.StopSignal() != unix.SIGSTOP {
			signal = int(status.StopSignal())
		}
		if err := unix.PtraceSyscall(pid, signal); err != nil && err != unix.ESRCH {
			return err
		}
	}
}
