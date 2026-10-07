package store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// boundedGitWaitDelay bounds pipe cleanup only after context cancellation.
const boundedGitWaitDelay = 100 * time.Millisecond

type gitIOPipe struct {
	parent *os.File
	child  *os.File
	copy   func() error
}

// gitPipe owns the I/O copying instead of Cmd.Wait. A nil input selects an
// output pipe; otherwise the pipe feeds the command's standard input.
func gitPipe(input io.Reader, output io.Writer) (gitIOPipe, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return gitIOPipe{}, err
	}
	if input != nil {
		return gitIOPipe{parent: w, child: r, copy: func() error {
			defer func() { _ = w.Close() }()
			_, err := io.Copy(w, input)
			// Like os/exec, a command may exit without consuming all stdin.
			if errors.Is(err, syscall.EPIPE) {
				return nil
			}
			return err
		}}, nil
	}
	return gitIOPipe{parent: r, child: w, copy: func() error {
		defer func() { _ = r.Close() }()
		_, err := io.Copy(output, r)
		return err
	}}, nil
}

// runBoundedGitOutput drains normal output to EOF without a timer. Cancellation
// kills the process group and bounds the remaining I/O, including descriptors
// held by escaped descendants. The watcher stays active after the direct child
// exits, until all I/O completes. Cmd receives only files, so its Wait has no
// copy goroutines or normal-exit WaitDelay to race against output completion.
func runBoundedGitOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var stdout, stderr bytes.Buffer
	var pipes []gitIOPipe
	defer func() {
		for _, p := range pipes {
			_ = p.parent.Close()
			_ = p.child.Close()
		}
	}()
	for _, stream := range []struct {
		input  io.Reader
		output io.Writer
		assign func(*os.File)
	}{
		{output: &stdout, assign: func(f *os.File) { cmd.Stdout = f }},
		{output: &stderr, assign: func(f *os.File) { cmd.Stderr = f }},
		{input: cmd.Stdin, assign: func(f *os.File) { cmd.Stdin = f }},
	} {
		if stream.input == nil && stream.output == nil {
			continue
		}
		p, err := gitPipe(stream.input, stream.output)
		if err != nil {
			return nil, nil, err
		}
		pipes = append(pipes, p)
		stream.assign(p.child)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	copyErrors := make(chan error, len(pipes))
	for _, p := range pipes {
		_ = p.child.Close()
		go func() { copyErrors <- p.copy() }()
	}
	finished := make(chan struct{})
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		select {
		case <-finished:
			return
		case <-ctx.Done():
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		timer := time.NewTimer(boundedGitWaitDelay)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			for _, p := range pipes {
				_ = p.parent.Close()
			}
		}
	}()
	err := cmd.Wait()
	var ioErr error
	for range pipes {
		if copyErr := <-copyErrors; ioErr == nil {
			ioErr = copyErr
		}
	}
	close(finished)
	<-cleanupDone
	if err == nil {
		err = ctx.Err()
		if err == nil {
			err = ioErr
		}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitErr.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), stderr.Bytes(), err
}
