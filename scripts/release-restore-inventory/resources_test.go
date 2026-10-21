package main

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func TestInventoryResourceBudget(t *testing.T) {
	if workers != 1 {
		t.Fatalf("inventory starts %d concurrent release probes; want 1", workers)
	}
	oldMemory := debug.SetMemoryLimit(2 << 30)
	defer debug.SetMemoryLimit(oldMemory)
	oldCPU := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(oldCPU)
	limitResources()
	if got := debug.SetMemoryLimit(-1); got != 512<<20 {
		t.Fatalf("memory target = %d; want 512 MiB", got)
	}
	if got := runtime.GOMAXPROCS(0); got != 2 {
		t.Fatalf("GOMAXPROCS = %d; want 2", got)
	}
	debug.SetMemoryLimit(128 << 20)
	runtime.GOMAXPROCS(1)
	limitResources()
	if debug.SetMemoryLimit(-1) != 128<<20 || runtime.GOMAXPROCS(0) != 1 {
		t.Fatal("inventory raised a tighter inherited resource target")
	}
}

func TestInventoryChildResourceBudget(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "environment")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nprintf '%s\\n' \"$GOMEMLIMIT\" \"$GOMAXPROCS\" \"$GOFLAGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOMEMLIMIT", "8GiB")
	t.Setenv("GOMAXPROCS", "16")
	t.Setenv("GOFLAGS", "-p=16")
	out, err := command(dir, program)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "256MiB\n2\n-mod=readonly -trimpath -p=1" {
		t.Fatalf("child resource settings = %q", got)
	}
}
