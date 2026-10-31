package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDomainShapeCheckCommandIsAbsent(t *testing.T) {
	for _, args := range [][]string{{"domain-shape-check"}, {"domain", "shape-check"}} {
		if _, _, found := matchCommandSpec(args); found {
			t.Fatalf("retired command is registered: %v", args)
		}
	}
	var help bytes.Buffer
	writeUsage(&help)
	if strings.Contains(help.String(), "domain-shape-check") || strings.Contains(help.String(), "domain shape-check") {
		t.Fatal("help teaches the retired command")
	}
}
