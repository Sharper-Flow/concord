package store

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// historicalWitnessLogEnv names the file a release restore coverage run
// collects historical corpus rows into. Unset, recording does nothing.
const historicalWitnessLogEnv = "TEST_CONCORD_HISTORICAL_WITNESS_LOG"

var (
	historicalWitnessMu sync.Mutex
	// historicalWitnessPending holds each test's queued rows by store until the
	// test ends.
	historicalWitnessPending = map[*testing.T]map[*Store][]historicalWitnessRow{}
)

type historicalWitnessRow struct {
	eventID, kind  string
	payloadVersion int
	payload        []byte
	line           []byte
}

// recordHistoricalWitness queues one historical row a corpus test wrote into
// s. When the test passes, the recorder replays s through the candidate's
// RebuildFromLog; the row is a witness only if that replay succeeds and the
// row's stored bytes are unchanged. A failing or skipped test, a refused
// replay, or a removed or rewritten row records nothing.
func recordHistoricalWitness(t *testing.T, s *Store, eventID, kind string, payloadVersion int, payload []byte) {
	t.Helper()
	path := os.Getenv(historicalWitnessLogEnv)
	if path == "" {
		return
	}
	line, err := json.Marshal(struct {
		Test           string `json:"test"`
		Kind           string `json:"kind"`
		PayloadVersion int    `json:"payload_version"`
		Payload        string `json:"payload"`
	}{t.Name(), kind, payloadVersion, string(payload)})
	if err != nil {
		t.Fatal(err)
	}
	historicalWitnessMu.Lock()
	defer historicalWitnessMu.Unlock()
	stores, registered := historicalWitnessPending[t]
	if !registered {
		stores = map[*Store][]historicalWitnessRow{}
		historicalWitnessPending[t] = stores
		// A test's deferred Close runs before this cleanup, so the recorder
		// replays a closed store through a fresh handle on the same file.
		t.Cleanup(func() { admitHistoricalWitnesses(t, path) })
	}
	stores[s] = append(stores[s], historicalWitnessRow{eventID, kind, payloadVersion, payload, line})
}

func admitHistoricalWitnesses(t *testing.T, path string) {
	historicalWitnessMu.Lock()
	stores := historicalWitnessPending[t]
	delete(historicalWitnessPending, t)
	historicalWitnessMu.Unlock()
	if t.Failed() || t.Skipped() {
		return
	}
	ctx := context.Background()
	var admitted []byte
	for s, rows := range stores {
		if s.db.PingContext(ctx) != nil {
			reopened, err := Open(ctx, s.path)
			if err != nil {
				t.Fatalf("historical witness store %s: %v", s.path, err)
			}
			defer func() { _ = reopened.Close() }()
			s = reopened
		}
		if RebuildFromLog(ctx, s) != nil {
			continue
		}
		for _, row := range rows {
			var present int
			if s.db.QueryRowContext(ctx, `SELECT 1 FROM domain_events WHERE event_id=? AND kind=? AND payload_version=? AND CAST(payload AS BLOB)=CAST(? AS BLOB)`,
				row.eventID, row.kind, row.payloadVersion, row.payload).Scan(&present) == nil {
				admitted = append(admitted, append(row.line, '\n')...)
			}
		}
	}
	if len(admitted) == 0 {
		return
	}
	historicalWitnessMu.Lock()
	defer historicalWitnessMu.Unlock()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("historical witness log: %v", err)
	}
	_, err = file.Write(admitted)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("historical witness log: %v", err)
	}
}

func TestHistoricalWitnessRecorderIsInertWhenUnset(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(historicalWitnessLogEnv, "")
	passed := t.Run("unset", func(t *testing.T) {
		recordHistoricalWitness(t, nil, "e", "work.created", 2, []byte(`{"title":"x"}`))
	})
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !passed || len(entries) != 0 {
		t.Fatalf("unset recorder: subtest passed=%v, wrote %d entries", passed, len(entries))
	}
}

// The census sets the log path on the go test command line, so it must survive
// TestMain's environment scrub.
func TestHistoricalWitnessLogSurvivesTheScrub(t *testing.T) {
	if len(witnessRowsOf(t, "TestRecordedHistoricalProjectionMigrationAndReplayAgree")) == 0 {
		t.Fatal("corpus child recorded no witness rows")
	}
}

// witnessRowsOf runs one corpus test in a child process with the witness log
// set and returns the rows it recorded.
func witnessRowsOf(t *testing.T, test string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "witnesses.jsonl")
	command := exec.Command(os.Args[0], "-test.v", "-test.run=^"+test+"$")
	command.Env = append(os.Environ(), historicalWitnessLogEnv+"="+path)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--- PASS: "+test+" ") {
		t.Fatalf("corpus child %s did not pass: %v\n%s", test, err, output)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	// One row per line: a recorded payload may itself contain spaces.
	var rows []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			rows = append(rows, line)
		}
	}
	return rows
}

// A row is a witness only when the candidate admits it: a passing test whose
// historical row the candidate refuses to replay records nothing.
func TestHistoricalWitnessRecordsOnlyAdmittedRows(t *testing.T) {
	admitted := witnessRowsOf(t, "TestLegacySessionClaimLandingReplaysAsProcessIdentityFreeRow")
	if len(admitted) != 1 || !strings.Contains(admitted[0], `"kind":"work.session_claim_landed"`) {
		t.Fatalf("admitted landing recorded %q, want its one row", admitted)
	}
	if rows := witnessRowsOf(t, "TestRecordedOccupancyRowSetAbsentTargetKeepsOtherRow"); len(rows) != 2 {
		t.Fatalf("a passing corpus test that closes its store before cleanup recorded %q, want its creation and release rows", rows)
	}
	for _, refusal := range []string{"TestLegacySessionClaimLandingAbsentPathStillRefuses", "TestCurrentShapeClaimLandingGenerationCheckPreserved"} {
		if rows := witnessRowsOf(t, refusal); len(rows) != 0 {
			t.Fatalf("%s asserts a replay refusal but recorded %q", refusal, rows)
		}
	}
	// The retired planning kinds are corpus witnesses: their rows predate the
	// retirement, and the passing replay proves the candidate still folds
	// every one unchanged.
	retired := witnessRowsOf(t, "TestRetiredPlanningEventsReplayWithoutInitiativeState")
	kinds := map[string]bool{}
	for _, row := range retired {
		for _, kind := range []string{"product.planning_mode_set", "initiative_entry.added", "initiative_entry.reordered",
			"initiative_entry.requiredness_changed", "initiative.narrative_revised", "initiative_entry.removed"} {
			if strings.Contains(row, `"kind":"`+kind+`"`) {
				kinds[kind] = true
			}
		}
	}
	if len(retired) != 6 || len(kinds) != 6 {
		t.Fatalf("retired planning corpus test recorded %d rows covering %v, want one row per retired kind", len(retired), kinds)
	}
	// The early worker terminal history records one row per released
	// generation: five completions (v1 and the four v2 shapes) and the basic
	// v1 failure, whose honest form predates the observed-directories member.
	early := witnessRowsOf(t, "TestRecordedEarlyWorkerTerminalHistoryRebuildsAndRecords")
	var completions, plainV1, directory, comparison, predicates, basicFailure int
	for _, row := range early {
		switch {
		case strings.Contains(row, `"kind":"worker.completed"`):
			completions++
			// The generation members live inside the row's embedded payload
			// string, so the markers are bare member names, not quoted keys.
			switch {
			case !strings.Contains(row, "evidence_origin"):
				plainV1++
			case strings.Contains(row, "base_comparison"):
				comparison++
			case strings.Contains(row, "predicate_ids"):
				predicates++
			case strings.Contains(row, "worker_directory"):
				directory++
			}
		case strings.Contains(row, `"kind":"worker.failed"`):
			if strings.Contains(row, "observed_session_directories") {
				t.Fatalf("basic failure row carries observed_session_directories: %s", row)
			}
			basicFailure++
		}
	}
	if completions != 5 || plainV1 != 1 || directory != 1 || comparison != 1 || predicates != 1 || basicFailure != 1 {
		t.Fatalf("early worker corpus recorded %d rows (completions=%d plain=%d dir=%d cmp=%d pred=%d failure=%d), want one per released generation",
			len(early), completions, plainV1, directory, comparison, predicates, basicFailure)
	}
	// The outside-repair history records its emitted disposition, reconciled
	// and resumed rows; the reconciled row must carry the complete receipt.
	repair := witnessRowsOf(t, "TestOutsideRepairHistoryRecordsCorpusWitnesses")
	var dispositions, reconciled, resumed int
	for _, row := range repair {
		switch {
		case strings.Contains(row, `"kind":"workflow.outside_repair_disposition_set"`):
			dispositions++
		case strings.Contains(row, `"kind":"workflow.outside_repair_reconciled"`):
			reconciled++
			if !strings.Contains(row, "required_checks") || !strings.Contains(row, "check_run_id") {
				t.Fatalf("reconciled row lacks the complete receipt members: %s", row)
			}
		case strings.Contains(row, `"kind":"workflow.outside_repair_resumed"`):
			resumed++
		}
	}
	if dispositions != 2 || reconciled != 1 || resumed != 1 {
		t.Fatalf("outside repair corpus recorded %d rows (dispositions=%d reconciled=%d resumed=%d), want 2/1/1", len(repair), dispositions, reconciled, resumed)
	}
	// The retired planning journal records the two Domain-observation rows,
	// the recorded one with nonempty refs and tags.
	domains := witnessRowsOf(t, "TestRetiredPlanningEventsReplay")
	var observedRow, dismissedRow int
	for _, row := range domains {
		switch {
		case strings.Contains(row, `"kind":"domain.observation_recorded"`):
			observedRow++
			// Pin the markers to the embedded array forms: the statement also
			// contains the word "historical", so a bare name would pass on an
			// empty tags list.
			if !strings.Contains(row, `refs\":[\"work:replay-link-a\"]`) || !strings.Contains(row, `tags\":[\"historical\"]`) {
				t.Fatalf("recorded observation row lacks nonempty refs/tags: %s", row)
			}
		case strings.Contains(row, `"kind":"domain.observation_dismissed"`):
			dismissedRow++
		}
	}
	if observedRow != 1 || dismissedRow != 1 {
		t.Fatalf("domain observation corpus recorded %d rows (recorded=%d dismissed=%d), want one each", len(domains), observedRow, dismissedRow)
	}
	// The same journal now records its full-endpoint relation pair: the added
	// row carries both to_* version members, and the removed row records too.
	var relationAdded, relationRemoved int
	for _, row := range domains {
		switch {
		case strings.Contains(row, `"kind":"relation.added"`):
			relationAdded++
			if !strings.Contains(row, "to_resulting_version") {
				t.Fatalf("recorded relation row lacks the target version members: %s", row)
			}
		case strings.Contains(row, `"kind":"relation.removed"`):
			relationRemoved++
		}
	}
	if relationAdded != 1 || relationRemoved != 1 {
		t.Fatalf("relation corpus recorded %d rows (added=%d removed=%d), want one each", len(domains), relationAdded, relationRemoved)
	}
	// The managed-resource update records its complete replacement form.
	managed := witnessRowsOf(t, "TestManagedResourceHistoricalUpdateRebuildsAndRecords")
	if len(managed) != 1 || !strings.Contains(managed[0], `"kind":"managed_resource.updated"`) || !strings.Contains(managed[0], "metadata_schema_version") {
		t.Fatalf("managed resource corpus recorded %q, want one complete updated row", managed)
	}
}
