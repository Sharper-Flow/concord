package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type removalInventoryRunner struct {
	root, inventory string
	err             error
}

func (r removalInventoryRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if args[0] == "rev-parse" {
		return []byte(r.root + "\n"), nil
	}
	return []byte(r.inventory), r.err
}

func TestDefaultRemovalNativeAbsence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "retired-checkout")
	state := defaultRemovalState{root: root, claims: []defaultRemovalClaim{{path: path}}}
	main := "worktree " + root + "\nHEAD " + strings.Repeat("a", 40) + "\nbranch refs/heads/main\n\n"
	for _, tc := range []struct {
		name, inventory string
		err             error
		want            bool
	}{
		{"complete", main, nil, true},
		{"empty", "", nil, false},
		{"truncated", strings.TrimSuffix(main, "\n"), nil, false},
		{"missing HEAD", "worktree " + root + "\nbranch refs/heads/main\n\n", nil, false},
		{"unknown attribute", strings.Replace(main, "\n\n", "\nunknown value\n\n", 1), nil, false},
		{"listed retired checkout", main + "worktree " + path + "\nHEAD " + strings.Repeat("a", 40) + "\ndetached\n\n", nil, false},
		{"quoted path", main + "worktree \"quoted-checkout\"\nHEAD " + strings.Repeat("a", 40) + "\ndetached\n\n", nil, false},
		{"unreachable", main, errors.New("native inventory unavailable"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := observeDefaultRemovalAbsence(t.Context(), removalInventoryRunner{root, tc.inventory, tc.err}, state)
			if (err == nil) != tc.want {
				t.Fatalf("absence error=%v want success=%v", err, tc.want)
			}
		})
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := observeDefaultRemovalAbsence(t.Context(), removalInventoryRunner{root, main, nil}, state); err == nil {
		t.Fatal("inventory alone authorized a surviving path")
	}
}

func TestDefaultRemovalProofCannotBeSupplied(t *testing.T) {
	s := openTemp(t)
	if err := s.Transact(t.Context(), func(tx *Transaction) error {
		if err := ValidateDefaultCheckoutRemovalTx(t.Context(), tx, nil, "project", "product"); err == nil {
			t.Fatal("missing native proof passed")
		}
		if err := ValidateDefaultCheckoutRemovalTx(t.Context(), tx, &DefaultCheckoutRemovalProof{}, "project", "product"); err == nil {
			t.Fatal("empty native proof passed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
