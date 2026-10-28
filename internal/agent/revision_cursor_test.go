package agent

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func assertStoreRevisionWatermark(t *testing.T, s *store.Store, response agentcontract.Envelope) {
	t.Helper()
	var revision int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT revision FROM store_revision WHERE id=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if got := watermarkString(response); got != strconv.FormatInt(revision, 10) {
		t.Fatalf("response source=%q, want store revision %d", got, revision)
	}
}

func TestPaginationCursorAcrossStoreReopen(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")
	first := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_work_browse", Operation: "list",
		Input: json.RawMessage(`{"product_id":"prod-alpha","limit":1}`),
	}, env)
	if first.Outcome != agentcontract.OutcomeOK || first.NextCursor == nil {
		t.Fatalf("first page: outcome=%s error=%+v cursor=%v", first.Outcome, first.Error, first.NextCursor)
	}
	assertStoreRevisionWatermark(t, s, first)
	var before int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT revision FROM store_revision WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service.Store = reopened
	var after int64
	if err := reopened.DatabaseForTesting().QueryRow(`SELECT revision FROM store_revision WHERE id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("unchanged reopen advanced revision from %d to %d", before, after)
	}
	second := dispatchRead(t, reopened, service, InvokeRequest{
		Tool: "concord_work_browse", Operation: "list",
		Input: json.RawMessage(`{"product_id":"prod-alpha","limit":1,"page":{"cursor":` + quoteCursor(*first.NextCursor) + `}}`),
	}, env)
	if second.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("cursor after reopen: outcome=%s error=%+v", second.Outcome, second.Error)
	}
	var firstPage, secondPage struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(first.Result, &firstPage); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Result, &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Items) != 1 || len(secondPage.Items) != 1 || firstPage.Items[0].ID == secondPage.Items[0].ID {
		t.Fatalf("reopened continuation repeated or lost rows: first=%+v second=%+v", firstPage.Items, secondPage.Items)
	}
}
