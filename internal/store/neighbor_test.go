package store

import (
	"testing"

	"github.com/sharper-flow/concord/internal/store/storetest/neighbor"
)

func TestSQLiteNeighborProcess(t *testing.T) { neighbor.Child(t) }
