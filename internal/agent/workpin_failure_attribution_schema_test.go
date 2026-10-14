package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The work pin attempt read schema carries the failure attribution read
// surface this repository authors. These tests hold the authored payload
// schema (contracts/agent-tool-surface-payloads.schema.json, the source
// owner of every response def) to the store's recorded shapes and bounds:
// the read def the work pin attempt references, the closed origin union,
// the closed identity union with its exclusive-or members, and the store's
// byte bounds. The generated projections are the generator's output; the
// parent slice regenerates them.

func failureAttributionSchema(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	data, err := os.ReadFile("../../contracts/agent-tool-surface-payloads.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("payload schema does not parse: %v", err)
	}
	defs, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("payload schema carries no $defs")
	}
	return document, defs
}

func failureAttributionDef(t *testing.T, defs map[string]any, name string) map[string]any {
	t.Helper()
	def, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("payload schema carries no $defs/%s", name)
	}
	return def
}

func failureAttributionProperty(t *testing.T, def map[string]any, name string) map[string]any {
	t.Helper()
	properties, ok := def["properties"].(map[string]any)
	if !ok {
		t.Fatalf("def carries no properties: %v", def)
	}
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("def carries no %s property", name)
	}
	return property
}

func TestWorkPinAttemptSchemaExposesFailureAttribution(t *testing.T) {
	t.Parallel()
	_, defs := failureAttributionSchema(t)
	attempt := failureAttributionDef(t, defs, "work_pin_attempt")
	if attempt["additionalProperties"] != false {
		t.Fatal("work_pin_attempt is not closed")
	}
	required, _ := attempt["required"].([]any)
	wantRequired := []string{"id", "epoch", "lane", "state"}
	if len(required) != len(wantRequired) {
		t.Fatalf("work_pin_attempt required=%v, want %v", required, wantRequired)
	}
	for index, name := range wantRequired {
		if required[index] != name {
			t.Fatalf("work_pin_attempt required=%v, want %v", required, wantRequired)
		}
	}
	property := failureAttributionProperty(t, attempt, "failure_attribution")
	if property["$ref"] != "#/$defs/worker_failure_attribution_read" {
		t.Fatalf("work_pin_attempt.failure_attribution = %v, want the read def reference", property)
	}
}

func TestWorkerFailureAttributionReadDefShape(t *testing.T) {
	t.Parallel()
	_, defs := failureAttributionSchema(t)
	read := failureAttributionDef(t, defs, "worker_failure_attribution_read")
	if read["additionalProperties"] != false {
		t.Fatal("worker_failure_attribution_read is not closed")
	}
	required, _ := read["required"].([]any)
	if len(required) != 3 || required[0] != "event_id" || required[1] != "source_version" || required[2] != "attribution" {
		t.Fatalf("worker_failure_attribution_read required=%v, want event_id, source_version, attribution", required)
	}
	attribution := failureAttributionProperty(t, read, "attribution")
	branches, ok := attribution["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("attribution=%v, want the nullable block union", attribution)
	}
	if ref, _ := branches[0].(map[string]any); ref["$ref"] != "#/$defs/worker_failure_attribution" {
		t.Fatalf("attribution branch=%v, want the block reference", branches[0])
	}
	if nulled, _ := branches[1].(map[string]any); nulled["type"] != "null" {
		t.Fatalf("attribution branch=%v, want the null arm", branches[1])
	}
}

func TestWorkerFailureAttributionSchemaMirrorsStoreBounds(t *testing.T) {
	t.Parallel()
	_, defs := failureAttributionSchema(t)
	// The identity string bound is the store's own
	// workerFailureIdentityRefMaxBytes guard (internal/store/
	// worker_failure_attribution.go).
	attemptIdentity := failureAttributionDef(t, defs, "worker_failure_attempt_identity")
	attemptID := failureAttributionProperty(t, attemptIdentity, "attempt_id")
	for keyword, want := range map[string]float64{"minLength": 1, "maxLength": 256, "x-maxBytes": 256} {
		if got, ok := attemptID[keyword].(float64); !ok || got != want {
			t.Fatalf("attempt_id %s=%v, want %v", keyword, attemptID[keyword], want)
		}
	}
	lanes, _ := attemptIdentity["properties"].(map[string]any)["lane_id"].(map[string]any)["enum"].([]any)
	wantLanes := []string{"research", "implement", "design", "review", "verify"}
	if len(lanes) != len(wantLanes) {
		t.Fatalf("lane_id enum=%v, want the registered lane ids %v", lanes, wantLanes)
	}
	for index, lane := range wantLanes {
		if lanes[index] != lane {
			t.Fatalf("lane_id enum=%v, want %v", lanes, wantLanes)
		}
	}
	// The subject locator and result_ref max bounds are the store's
	// WorkerFailureSourceRefMaxBytes bound; they equal the observed source
	// locator bound rather than restating a number. The minimums differ on
	// purpose: a source may record a digest without a locator, a subject
	// reference and a result reference may not.
	subject := failureAttributionDef(t, defs, "worker_failure_subject")
	source := failureAttributionDef(t, defs, "worker_failure_source")
	subjectLocator := failureAttributionProperty(t, subject, "locator")
	sourceLocator := failureAttributionProperty(t, source, "locator")
	if subjectLocator["maxLength"] != sourceLocator["maxLength"] || subjectLocator["x-maxBytes"] != sourceLocator["x-maxBytes"] {
		t.Fatalf("subject locator=%v, want the observed source locator bound %v", subjectLocator, sourceLocator)
	}
	integration := failureAttributionDef(t, defs, "worker_failure_integration_identity")
	integrationProperties, _ := integration["properties"].(map[string]any)
	inputItems, _ := integrationProperties["input_results"].(map[string]any)["items"].(map[string]any)
	resultRef := failureAttributionProperty(t, inputItems, "result_ref")
	if resultRef["maxLength"] != sourceLocator["maxLength"] || resultRef["x-maxBytes"] != sourceLocator["x-maxBytes"] {
		t.Fatalf("result_ref=%v, want the observed source locator bound %v", resultRef, sourceLocator)
	}
	// The job binding identity pattern is the recorded job identity pattern
	// the packet's own worker_job already carries.
	binding := failureAttributionDef(t, defs, "worker_failure_job_binding")
	jobID := failureAttributionProperty(t, binding, "job_id")
	packetJob := failureAttributionDef(t, defs, "worker_packet_worker_job")
	packetJobID := failureAttributionProperty(t, packetJob, "job_id")
	if jobID["pattern"] != packetJobID["pattern"] || jobID["minLength"] != packetJobID["minLength"] || jobID["maxLength"] != packetJobID["maxLength"] {
		t.Fatalf("job binding job_id=%v, want the recorded packet job identity bounds %v", jobID, packetJobID)
	}
	// Origin, identity kinds, and subject kinds are the store's closed
	// vocabularies.
	block := failureAttributionDef(t, defs, "worker_failure_attribution")
	blockBranches, _ := block["oneOf"].([]any)
	recordedOrigin, _ := blockBranches[0].(map[string]any)["properties"].(map[string]any)["origin"].(map[string]any)
	legacyOrigin, _ := blockBranches[1].(map[string]any)["properties"].(map[string]any)["origin"].(map[string]any)
	if recordedOrigin["const"] != "recorded" || legacyOrigin["const"] != "legacy_unavailable" {
		t.Fatalf("origin consts=%v/%v, want recorded and legacy_unavailable", recordedOrigin, legacyOrigin)
	}
	identity := failureAttributionDef(t, defs, "worker_failure_identity")
	identityBranches, _ := identity["oneOf"].([]any)
	attemptKind, _ := identityBranches[0].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)
	integrationKind, _ := identityBranches[1].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)
	if attemptKind["const"] != "attempt" || integrationKind["const"] != "integration" {
		t.Fatalf("identity kind consts=%v/%v, want attempt and integration", attemptKind, integrationKind)
	}
	subjectKinds, _ := failureAttributionProperty(t, subject, "kind")["enum"].([]any)
	if len(subjectKinds) != 2 || subjectKinds[0] != "observed_delivery_event" || subjectKinds[1] != "native_run" {
		t.Fatalf("subject kinds=%v, want observed_delivery_event and native_run", subjectKinds)
	}
	// The integration's native run pins the native_run kind.
	nativeRun, _ := integration["properties"].(map[string]any)["native_run"].(map[string]any)
	nativeRunAllOf, _ := nativeRun["allOf"].([]any)
	kindPin, _ := nativeRunAllOf[1].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)
	if kindPin["const"] != "native_run" {
		t.Fatalf("integration native_run kind pin=%v, want native_run", kindPin)
	}
}

func TestWorkerFailureAttributionSchemaValidatesRecords(t *testing.T) {
	t.Parallel()
	document, defs := failureAttributionSchema(t)
	block := failureAttributionDef(t, defs, "worker_failure_attribution")
	read := failureAttributionDef(t, defs, "worker_failure_attribution_read")
	valid := map[string]bool{
		// A recorded attempt block with the job branch.
		`{"origin":"recorded","observation":{"observer":"host_adapter","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"evt-1","digest":"sha256:` + zeroes + `"}]},"identity":{"kind":"attempt","attempt":{"attempt_id":"attempt:w1","lane_id":"implement","epoch":1,"packet_digest":"sha256:` + zeroes + `","job":{"job_id":"job:w1","revision":1,"digest":"sha256:` + zeroes + `"}}}}`: true,
		// The explicit no_job branch.
		`{"origin":"recorded","observation":{"observer":"worker_report","phase":"worker_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"admitted_report","locator":"report-1"}]},"identity":{"kind":"attempt","attempt":{"attempt_id":"attempt:w2","lane_id":"research","epoch":2,"packet_digest":"sha256:` + zeroes + `","no_job":true}}}`: true,
		// A subject reference and an integration population.
		`{"origin":"recorded","observation":{"observer":"coordinator","phase":"integration","cause":"implementation","basis":"supported","sources":[{"kind":"native_run","locator":"lease-1"}],"diagnosis":"d"},"identity":{"kind":"integration","integration":{"native_run":{"kind":"native_run","locator":"lease-1","digest":"sha256:` + zeroes + `"},"input_results":[{"job":{"job_id":"job:w3","revision":1,"digest":"sha256:` + zeroes + `"},"result_ref":"result:1"}]}}}`: true,
		`{"origin":"recorded","observation":{"observer":"coordinator","phase":"integration","cause":"unknown","basis":"unknown","sources":[{"kind":"native_run","locator":"lease-2"}]},"identity":{"kind":"integration","integration":{"native_run":{"kind":"native_run","locator":"lease-2"},"inputs_unavailable":true}}}`:                                                                                                                                                     true,
		// The bare legacy marker.
		`{"origin":"legacy_unavailable"}`: true,
	}
	invalid := map[string]string{
		// Job and no_job are mutually exclusive.
		`{"origin":"recorded","observation":{"observer":"host_adapter","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"e"}]},"identity":{"kind":"attempt","attempt":{"attempt_id":"a","lane_id":"implement","epoch":1,"packet_digest":"sha256:` + zeroes + `","job":{"job_id":"job:x","revision":1,"digest":"sha256:` + zeroes + `"},"no_job":true}}}`: "job and no_job together",
		// Neither job nor no_job.
		`{"origin":"recorded","observation":{"observer":"host_adapter","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"e"}]},"identity":{"kind":"attempt","attempt":{"attempt_id":"a","lane_id":"implement","epoch":1,"packet_digest":"sha256:` + zeroes + `"}}}`: "no job and no no_job",
		// A recorded block without identity.
		`{"origin":"recorded","observation":{"observer":"host_adapter","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"e"}]}}`: "recorded without identity",
		// The legacy marker with content.
		`{"origin":"legacy_unavailable","observation":{"observer":"host_adapter","phase":"host_execution","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"e"}]}}`: "legacy with observation",
		// Both input population members.
		`{"origin":"recorded","observation":{"observer":"coordinator","phase":"integration","cause":"unknown","basis":"unknown","sources":[{"kind":"native_run","locator":"l"}]},"identity":{"kind":"integration","integration":{"native_run":{"kind":"native_run","locator":"l"},"input_results":[{"job":{"job_id":"job:y","revision":1,"digest":"sha256:` + zeroes + `"},"result_ref":"r"}],"inputs_unavailable":true}}}`: "input_results and inputs_unavailable together",
		// An integration native run of the wrong subject kind.
		`{"origin":"recorded","observation":{"observer":"coordinator","phase":"integration","cause":"unknown","basis":"unknown","sources":[{"kind":"native_run","locator":"l"}]},"identity":{"kind":"integration","integration":{"native_run":{"kind":"observed_delivery_event","locator":"l"},"inputs_unavailable":true}}}`: "non-native integration subject",
	}
	assertSchemaAdmission(t, document, block, valid, invalid)

	readValid := map[string]bool{
		`{"event_id":"failed-1","source_version":4,"attribution":{"origin":"legacy_unavailable"}}`: true,
		`{"event_id":"failed-2","source_version":1,"attribution":null}`:                            true,
		`{"event_id":"born-1","source_version":6,"attribution":{"origin":"recorded","observation":{"observer":"host_adapter","phase":"dispatch","cause":"unknown","basis":"unknown","sources":[{"kind":"host_event","locator":"e"}]},"identity":{"kind":"attempt","attempt":{"attempt_id":"a","lane_id":"review","epoch":3,"packet_digest":"sha256:` + zeroes + `","no_job":true}}}}`: true,
	}
	readInvalid := map[string]string{
		`{"event_id":"failed-3","source_version":4}`:                                                            "attribution member absent",
		`{"event_id":"failed-4","source_version":4,"attribution":{"origin":"recorded"}}`:                        "recorded without content",
		`{"event_id":"failed-5","source_version":4,"attribution":{"origin":"legacy_unavailable"},"extra":true}`: "undeclared member",
	}
	assertSchemaAdmission(t, document, read, readValid, readInvalid)
}

func assertSchemaAdmission(t *testing.T, document, schema map[string]any, valid map[string]bool, invalid map[string]string) {
	t.Helper()
	for raw := range valid {
		if err := payloadschema.ValidateValue(attributionDecode(t, raw), schema, document, "$"); err != nil {
			t.Fatalf("schema refused a record the store records: %v\n%s", err, raw)
		}
	}
	for raw, why := range invalid {
		if err := payloadschema.ValidateValue(attributionDecode(t, raw), schema, document, "$"); err == nil {
			t.Fatalf("schema admitted %s:\n%s", why, raw)
		}
	}
}

func attributionDecode(t *testing.T, raw string) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// zeroes is the 64-zero hex body the schema fixtures concatenate after the
// "sha256:" prefix for well-formed digests.
var zeroes = strings.Repeat("0", 64)
