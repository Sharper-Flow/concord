package store

import (
	"errors"
	"testing"
)

type nilStoreCase struct {
	name string
	call func(*Store) error
}

// assertUnopenedStoreTypedFailure pins the guard placement for methods
// that delegate to a queryer-taking core.
//
// The guard belongs on the method, not the core. A nil *sql.DB placed in a
// queryer parameter becomes a non-nil interface holding a nil pointer, so a
// core-side "q == nil" test cannot fire and the call panics inside the driver
// instead of returning a typed failure. A nil receiver panics even earlier,
// when the method evaluates s.db to build the argument.
func assertUnopenedStoreTypedFailure(t *testing.T, cases []nilStoreCase) {
	t.Helper()
	if len(cases) == 0 {
		t.Fatal("unopened store case family is empty")
	}
	for _, tc := range cases {
		for _, receiver := range []struct {
			label string
			store *Store
		}{
			{"nil receiver", nil},
			{"nil database handle", &Store{}},
		} {
			t.Run(tc.name+"/"+receiver.label, func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s panicked on an unopened store: %v", tc.name, r)
					}
				}()
				err := tc.call(receiver.store)
				if err == nil {
					t.Fatalf("%s returned no error on an unopened store", tc.name)
				}
				var failure *Failure
				if !errors.As(err, &failure) {
					t.Fatalf("%s returned an untyped error on an unopened store: %v", tc.name, err)
				}
			})
		}
	}
}
