package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The workflow-surface binary-skew refusal retired with the definition
// authority: migration 183 deletes the workflow rows of runtime_state_writers
// and no production path records or checks them (workflow-engine-contract
// §§2.1, 5.1 and 11). The knowledge-surface schema guard keeps its own
// refusal paths.
