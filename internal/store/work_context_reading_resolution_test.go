package store

import "testing"

func mustTxExec(t *testing.T, tx *Transaction, sqlText string, args ...any) {
	t.Helper()
	sqlTx, err := transactionSQL(tx, "fixture_sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlTx.Exec(sqlText, args...); err != nil {
		t.Fatal(err)
	}
}

func currentFixtureView(t *testing.T, f workContextFixture) *WorkContextView {
	t.Helper()
	view, err := readWorkContextView(t.Context(), f.store.DatabaseForTesting(), f.workID)
	if err != nil || view == nil {
		t.Fatalf("current view = %+v err = %v, want a present view", view, err)
	}
	return view
}
