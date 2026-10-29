package agentcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestEnvelopeGoldenOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		make func() Envelope
	}{
		{"ok read", func() Envelope {
			return newOKReadForTest(NewBase("req-1", "concord_product_view", "resolve"), json.RawMessage(`{"product_id":"p-1","projects":[]}`))
		}},
		{"ok mutation", func() Envelope {
			return NewOKMutation(NewBase("req-2", "concord_work_define", "capture"), json.RawMessage(`{"changed_refs":[],"next_valid_intents":[]}`), []ChangedRef{{EntityKind: "work", ID: "w-1", Version: "2"}}, []NextIntent{{Tool: "concord_work_browse", Operation: "scope", QueryID: "PM1.Q6", ReasonCode: "created"}})
		}},
		{"pending", func() Envelope {
			return NewPending(NewBase("req-3", "concord_work_compact", "publish"), OperationRef{ID: "op-1", Kind: "publish", Version: "1", State: OperationPending, CurrentStep: "git", UpdatedAt: fixedTime()}, RecoveryAction{Kind: "reconcile_operation"})
		}},
		{"partial", func() Envelope {
			return NewPartial(NewBase("req-4", "concord_work_compact", "publish"), OperationRef{ID: "op-1", Kind: "publish", Version: "2", State: OperationPartial, CurrentStep: "sqlite", UpdatedAt: fixedTime()}, []string{"git"}, TypedError{Kind: "operation_conflict", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial})
		}},
		{"core error", func() Envelope {
			e := NewBase("req-5", "concord_work_browse", "list")
			e.Authority = AuthorityUnreachable
			return NewCoreError(e, TypedError{Kind: "unreachable", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone})
		}},
		{"adapter error", func() Envelope {
			e := Envelope{SchemaVersion: "1.0", ManifestDigest: ManifestDigest, RequestID: "req-6", Origin: OriginAdapter, Tool: "concord_product_view", Operation: "resolve", ResolvedScope: nil, Authority: AuthorityUnreachable, Freshness: nil, SourceVersionWatermark: []Watermark{}, OrderingKeys: []string{}, NextCursor: nil, Omissions: []Notice{}, Warnings: []Notice{}, EvidenceRefs: []EvidenceRef{}, Outcome: OutcomeError}
			for _, op := range ContractOperations {
				if op.Tool == e.Tool && op.Operation == e.Operation {
					e.QueryID = op.QueryID
					break
				}
			}
			e.Error = &TypedError{Kind: "malformed_response", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, AdapterReason: "malformed_core_response"}
			return e
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := tt.make().Encode()
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decodeEnvelopeForTest(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded, mustEncode(decoded)) {
				t.Fatalf("round trip changed JSON:\n%s\n%s", encoded, mustEncode(decoded))
			}
		})
	}
}

func TestMutationOKEnvelopeEmitsRequiredMetadataWhenEmpty(t *testing.T) {
	t.Parallel()
	// The generated envelope contract (contracts/agent-tool-envelope.schema.json,
	// ok def) requires changed_refs and next_valid_intents even when empty.
	// Slice omitempty would drop required arrays and make committed effects
	// undeliverable through the adapter.
	e := NewOKMutation(
		NewBase("req-702", "concord_work_transition", "lifecycle"),
		json.RawMessage(`{"changed_refs":[],"next_valid_intents":[]}`),
		nil,
		nil,
	)
	encoded, err := e.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"changed_refs", "next_valid_intents"} {
		raw, ok := decoded[key]
		if !ok {
			t.Fatalf("mutation ok envelope missing required key %q: %s", key, encoded)
		}
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
			t.Fatalf("mutation ok envelope key %q must be an array, got %s", key, raw)
		}
	}
}

func TestNonMutationEnvelopesOmitMutationMetadata(t *testing.T) {
	t.Parallel()
	read := newOKReadForTest(NewBase("req-702-r", "concord_product_view", "resolve"), json.RawMessage(`{"product_id":"p-1","projects":[]}`))
	readEncoded, err := read.Encode()
	if err != nil {
		t.Fatal(err)
	}
	coreError := NewCoreError(NewBase("req-702-e", "concord_work_browse", "list"), TypedError{Kind: "unreachable", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone})
	// The envelope law couples the unreachable error kind to unreachable
	// envelope authority; an authoritative envelope cannot carry it.
	coreError.Authority = AuthorityUnreachable
	errorEncoded, err := coreError.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for name, encoded := range map[string][]byte{"ok read": readEncoded, "core error": errorEncoded} {
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"changed_refs", "next_valid_intents"} {
			if _, present := decoded[key]; present {
				t.Fatalf("%s envelope must omit %q (schema forbids it outside ok): %s", name, key, encoded)
			}
		}
	}
}

func TestAdapterCLIInputRefusalMarshals(t *testing.T) {
	e := NewCoreError(NewBase("cli-input-refusal", "concord_work_define", "capture"), TypedError{Kind: "invalid_input", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "correct_request"}, EffectState: EffectNone, AdapterReason: "invalid_cli_input"})
	e.Origin, e.Authority = OriginAdapter, AuthorityUnreachable
	if _, err := e.Encode(); err != nil {
		t.Fatalf("adapter CLI input refusal cannot marshal: %v", err)
	}
	e.Error.EffectState = EffectPossible
	if err := e.Validate(); err == nil {
		t.Fatal("pre-effect adapter input refusal accepted a possible effect")
	}
}

func TestCorrectRequestRequiresPreEffectInputRefusal(t *testing.T) {
	for _, origin := range []Origin{OriginCore, OriginAdapter} {
		for _, test := range []struct {
			kind   string
			effect EffectState
			retry  bool
		}{
			{"invalid_input", EffectNone, false},
			{"invalid_input", EffectPossible, false},
			{"invalid_input", EffectPartial, false},
			{"invalid_input", EffectNone, true},
			{"unauthorized", EffectNone, false},
			{"approval_required", EffectNone, false},
		} {
			e := NewCoreError(NewBase("input-coupling", "concord_work_transition", "worker_abandon"), TypedError{Kind: test.kind, EffectState: test.effect, RetrySafe: test.retry, RecoveryAction: RecoveryAction{Kind: "correct_request"}})
			e.Origin = origin
			if origin == OriginAdapter {
				e.Authority, e.Error.AdapterReason = AuthorityUnreachable, "invalid_tool_input"
			}
			want := test.kind == "invalid_input" && test.effect == EffectNone && !test.retry
			if err := e.Validate(); (err == nil) != want {
				t.Errorf("origin=%s kind=%s effect=%s retry=%v: %v", origin, test.kind, test.effect, test.retry, err)
			}
		}
	}
}

func TestAdapterEnvelopeAdmitsSchemaVocabulary(t *testing.T) {
	type schemaNode struct {
		Enum       []string              `json:"enum"`
		Const      any                   `json:"const"`
		Defs       map[string]schemaNode `json:"$defs"`
		Properties map[string]schemaNode `json:"properties"`
		AllOf      []schemaNode          `json:"allOf"`
		If         *schemaNode           `json:"if"`
		Then       *schemaNode           `json:"then"`
	}
	var schema schemaNode
	if err := json.Unmarshal([]byte(payloadschema.GeneratedEnvelopeSchemaDocument), &schema); err != nil {
		t.Fatal(err)
	}
	reasons := schema.Defs["typedError"].Properties["adapter_reason"].Enum
	var kinds []string
	for _, branch := range schema.Defs["errorOutcome"].AllOf {
		if branch.If == nil || branch.Then == nil || branch.If.Properties["origin"].Const != "adapter" {
			continue
		}
		if values := branch.Then.Properties["error"].Properties["kind"].Enum; len(values) > 0 {
			if kinds != nil {
				t.Fatal("schema declares multiple adapter kind enums")
			}
			kinds = values
		}
	}
	if len(reasons) == 0 || len(kinds) == 0 {
		t.Fatal("schema declares no adapter vocabulary")
	}
	assertAdmitted := func(t *testing.T, typed TypedError) {
		t.Helper()
		e := NewCoreError(NewBase("adapter-vocabulary", "concord_work_transition", "worker_abandon"), typed)
		e.Origin, e.Authority = OriginAdapter, AuthorityUnreachable
		if err := e.Validate(); err != nil {
			t.Fatalf("schema-admitted adapter error refused: %v", err)
		}
	}
	for _, reason := range reasons {
		t.Run("reason/"+reason, func(t *testing.T) {
			typed := TypedError{Kind: "transport_failure", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, AdapterReason: reason}
			if reason == "invalid_cli_input" || reason == "invalid_request_wrapper" || reason == "invalid_tool_input" {
				typed.Kind, typed.RecoveryAction.Kind = "invalid_input", "correct_request"
			}
			assertAdmitted(t, typed)
		})
	}
	for _, kind := range kinds {
		t.Run("kind/"+kind, func(t *testing.T) {
			typed := TypedError{Kind: kind, AdapterReason: "malformed_core_response", EffectState: EffectNone, RecoveryAction: RecoveryAction{Kind: "contact_operator"}}
			switch kind {
			case "transport_failure":
				typed.AdapterReason, typed.RecoveryAction.Kind = "session_directory_unreadable", "retry_same_request"
			case "timeout":
				typed.AdapterReason, typed.RecoveryAction.Kind = "timeout_no_effect", "retry_same_request"
			case "cancelled":
				typed.AdapterReason, typed.RecoveryAction.Kind = "cancelled_no_effect", "retry_same_request"
			case "operation_conflict":
				typed.AdapterReason, typed.RecoveryAction.Kind, typed.EffectState = "unknown_effect", "reconcile_operation", EffectPossible
			case "invalid_input":
				typed.AdapterReason, typed.RecoveryAction.Kind = "invalid_cli_input", "correct_request"
			}
			assertAdmitted(t, typed)
		})
	}
}

func TestAdapterEnvelopeRetainsSchemaRejections(t *testing.T) {
	base := NewCoreError(NewBase("adapter-rejections", "concord_work_transition", "worker_abandon"), TypedError{Kind: "transport_failure", AdapterReason: "missing_binary", EffectState: EffectNone, RecoveryAction: RecoveryAction{Kind: "contact_operator"}})
	base.Origin, base.Authority = OriginAdapter, AuthorityUnreachable
	for name, mutate := range map[string]func(*Envelope){
		"unknown kind":             func(e *Envelope) { e.Error.Kind = "unknown_error_kind" },
		"unknown reason":           func(e *Envelope) { e.Error.AdapterReason = "unknown_adapter_reason" },
		"removed grant reason":     func(e *Envelope) { e.Error.AdapterReason = "grant_bootstrap_failed" },
		"removed occupancy reason": func(e *Envelope) { e.Error.AdapterReason = "session_occupancy_unreadable" },
		"core reason":              func(e *Envelope) { e.Origin = OriginCore },
		"core-only kind":           func(e *Envelope) { e.Error.Kind = "internal_error" },
		"partial effect":           func(e *Envelope) { e.Error.EffectState = EffectPartial },
		"authoritative adapter":    func(e *Envelope) { e.Authority = AuthorityAuthoritative },
		"bootstrap effect": func(e *Envelope) {
			e.Error.AdapterReason, e.Error.EffectState = "manifest_mismatch", EffectPossible
		},
		"bootstrap kind": func(e *Envelope) {
			e.Error.AdapterReason, e.Error.Kind = "manifest_mismatch", "malformed_response"
		},
		"bootstrap recovery": func(e *Envelope) {
			e.Error.AdapterReason, e.Error.RecoveryAction.Kind = "manifest_mismatch", "retry_same_request"
		},
		"timeout recovery": func(e *Envelope) { e.Error.Kind, e.Error.AdapterReason = "timeout", "timeout_no_effect" },
		"conflict recovery": func(e *Envelope) {
			e.Error.Kind, e.Error.AdapterReason = "operation_conflict", "unknown_effect"
		},
		"input recovery": func(e *Envelope) {
			e.Error.Kind, e.Error.AdapterReason = "invalid_input", "invalid_cli_input"
		},
		"input retry": func(e *Envelope) {
			e.Error.Kind, e.Error.AdapterReason, e.Error.RecoveryAction.Kind, e.Error.RetrySafe = "invalid_input", "invalid_request_wrapper", "correct_request", true
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := base
			typed := *base.Error
			e.Error = &typed
			mutate(&e)
			if err := e.Validate(); err == nil {
				t.Fatal("invalid adapter envelope admitted")
			}
		})
	}
}

func TestAdapterVacateGeneratedSchemaRecoveryScope(t *testing.T) {
	// Test the generated schema independently of Go's core-only recovery coupling.
	type schemaWire Envelope
	validate := func(e Envelope) error {
		t.Helper()
		data, err := json.Marshal(schemaWire(e))
		if err != nil {
			t.Fatal(err)
		}
		return payloadschema.ValidateEnvelope(data)
	}
	for _, recovery := range []string{"retry_same_request", "contact_operator"} {
		t.Run(recovery, func(t *testing.T) {
			e := NewCoreError(NewBase("vacate-schema", "concord_work_transition", "session_vacate"), TypedError{Kind: "operation_conflict", AdapterReason: "unknown_effect", EffectState: EffectPossible, RecoveryAction: RecoveryAction{Kind: recovery}})
			e.Origin, e.Authority = OriginAdapter, AuthorityUnreachable
			if err := validate(e); err != nil {
				t.Fatalf("adapter vacate recovery refused: %v", err)
			}
			e.Operation = "worker_abandon"
			if err := validate(e); err == nil {
				t.Fatal("vacate recovery admitted under another operation")
			}
			e.Operation, e.Origin, e.Authority, e.Error.AdapterReason = "session_vacate", OriginCore, AuthorityAuthoritative, ""
			if err := validate(e); err == nil {
				t.Fatal("vacate recovery admitted under core origin")
			}
			e.Error.RecoveryAction.Kind = "reconcile_operation"
			if err := validate(e); err != nil {
				t.Fatalf("core reconciliation refused: %v", err)
			}
			partial := NewPartial(NewBase("partial-schema", "concord_work_compact", "publish"), OperationRef{ID: "op-1", Kind: "publish", Version: "2", State: OperationPartial, CurrentStep: "sqlite", UpdatedAt: fixedTime()}, []string{"git"}, TypedError{Kind: "operation_conflict", EffectState: EffectPartial, RecoveryAction: RecoveryAction{Kind: recovery}})
			if err := validate(partial); err == nil {
				t.Fatal("partial conflict admitted without reconciliation")
			}
			partial.Error.RecoveryAction.Kind = "reconcile_operation"
			if err := validate(partial); err != nil {
				t.Fatalf("partial reconciliation refused: %v", err)
			}
		})
	}
}

func TestAdapterWrapperRefusalMarshals(t *testing.T) {
	e := NewCoreError(NewBase("wrapper-refusal", "concord_work_browse", "list"), TypedError{Kind: "invalid_input", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "correct_request"}, EffectState: EffectNone, AdapterReason: "invalid_request_wrapper"})
	e.Origin, e.Authority = OriginAdapter, AuthorityUnreachable
	if _, err := e.Encode(); err != nil {
		t.Fatalf("adapter wrapper refusal cannot marshal: %v", err)
	}
	e.Error.EffectState = EffectPossible
	if err := e.Validate(); err == nil {
		t.Fatal("pre-effect adapter input refusal accepted a possible effect")
	}
}

func TestAdapterWrapperRefusalUnresolvedOperation(t *testing.T) {
	e := NewCoreError(NewBase("wrapper-unresolved", "concord_work_browse", ""), TypedError{Kind: "invalid_input", RecoveryAction: RecoveryAction{Kind: "correct_request"}, EffectState: EffectNone, AdapterReason: "invalid_request_wrapper"})
	e.Origin, e.Authority = OriginAdapter, AuthorityUnreachable
	encoded, err := e.Encode()
	if err != nil {
		t.Fatalf("unresolved wrapper refusal cannot marshal: %v", err)
	}
	var decoded Envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unresolved wrapper refusal cannot decode: %v", err)
	}
	if decoded.Operation != "" || decoded.QueryID != "" {
		t.Fatal("unresolved wrapper refusal fabricated an operation or query")
	}
	for name, mutate := range map[string]func(*Envelope){
		"core origin":       func(e *Envelope) { e.Origin = OriginCore },
		"unknown operation": func(e *Envelope) { e.Operation = "unknown" },
		"query identity":    func(e *Envelope) { e.QueryID = "PM1.Q1" },
		"transport kind":    func(e *Envelope) { e.Error.Kind = "transport_failure" },
		"other reason":      func(e *Envelope) { e.Error.AdapterReason = "missing_binary" },
		"possible effect":   func(e *Envelope) { e.Error.EffectState = EffectPossible },
		"retry safe":        func(e *Envelope) { e.Error.RetrySafe = true },
		"same request":      func(e *Envelope) { e.Error.RecoveryAction.Kind = "retry_same_request" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid, invalidError := e, *e.Error
			invalid.Error = &invalidError
			mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatal("invalid unresolved refusal accepted")
			}
		})
	}
}

func TestAdapterWrapperRefusalAcceptedOperationCoupling(t *testing.T) {
	for _, pair := range []struct{ tool, operation string }{{"concord_work_browse", "list"}, {"concord_work_define", "capture"}} {
		e := NewCoreError(NewBase("wrapper-coupling", pair.tool, pair.operation), TypedError{Kind: "invalid_input", RecoveryAction: RecoveryAction{Kind: "correct_request"}, EffectState: EffectNone, AdapterReason: "invalid_request_wrapper"})
		e.Origin, e.Authority = OriginAdapter, AuthorityUnreachable
		for name, mutate := range map[string]func(*TypedError){
			"transport kind": func(e *TypedError) {
				e.Kind, e.RecoveryAction.Kind = "transport_failure", "contact_operator"
			},
			"other reason":    func(e *TypedError) { e.AdapterReason = "missing_binary" },
			"possible effect": func(e *TypedError) { e.EffectState = EffectPossible },
			"retry safe":      func(e *TypedError) { e.RetrySafe = true },
			"same request":    func(e *TypedError) { e.RecoveryAction.Kind = "retry_same_request" },
		} {
			t.Run(pair.tool+"/"+name, func(t *testing.T) {
				invalid, invalidError := e, *e.Error
				invalid.Error = &invalidError
				mutate(invalid.Error)
				if err := invalid.Validate(); err == nil {
					t.Fatal("contradictory wrapper refusal accepted")
				}
			})
		}
	}
}

func TestReadOKEnvelopeRejectsMutationMetadata(t *testing.T) {
	t.Parallel()
	for _, populated := range []bool{false, true} {
		for _, field := range []string{"changed_refs", "next_valid_intents", "both"} {
			t.Run(fmt.Sprintf("%s/populated=%t", field, populated), func(t *testing.T) {
				e := newOKReadForTest(NewBase("read-metadata", "concord_product_view", "resolve"), json.RawMessage(`{"product_id":"p-1","projects":[]}`))
				changed, intents := []ChangedRef{}, []NextIntent{}
				if populated {
					changed = append(changed, ChangedRef{EntityKind: "work", ID: "work-1", Version: "1"})
					intents = append(intents, NextIntent{Tool: "concord_work_browse", Operation: "list", QueryID: "PM1.Q3", ReasonCode: "inspect"})
				}
				if field != "next_valid_intents" {
					e.ChangedRefs = &changed
				}
				if field != "changed_refs" {
					e.NextValidIntents = &intents
				}
				if err := e.validateOK(); err == nil || !strings.Contains(err.Error(), "read ok envelope") {
					t.Fatalf("validateOK accepted read metadata or gave the wrong cause: %v", err)
				}
			})
		}
	}
}

func TestEnvelopeRejectsUnknownVariantsAndFields(t *testing.T) {
	t.Parallel()
	base := NewBase("req", "concord_product_view", "resolve")
	base.Outcome = Outcome("surprise")
	if err := base.Validate(); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	valid := newOKReadForTest(NewBase("req", "concord_product_view", "resolve"), json.RawMessage(`{"product_id":"p-1","projects":[]}`))
	raw, _ := valid.Encode()
	raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
	if _, err := decodeEnvelopeForTest(raw); err == nil {
		t.Fatal("unknown envelope field accepted")
	}
}

func TestOutcomeMismatchIsClosedAndCannotDowngrade(t *testing.T) {
	t.Parallel()
	base := NewBase("outcome-mismatch", "concord_work_transition", "workflow_action")
	valid := NewCoreError(base, TypedError{Kind: "outcome_mismatch", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone})
	if _, err := valid.Encode(); err != nil {
		t.Fatalf("closed outcome_mismatch rejected: %v", err)
	}
	invalid := NewCoreError(base, TypedError{Kind: "outcome_mismatch", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "retry_same_request"}, EffectState: EffectNone})
	if _, err := invalid.Encode(); err == nil {
		t.Fatal("outcome_mismatch was downgraded to retryable recovery")
	}
}

func TestStaleLawRevisionRequiresSHA256Proofs(t *testing.T) {
	t.Parallel()
	base := NewBase("stale-law", "concord_work_transition", "workflow_action")
	valid := NewCoreError(base, TypedError{
		Kind: "stale_law_revision", RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectNone,
		StaleLawRevision: &StaleLawRevision{OldLawID: "spec:old", OldContentHash: "sha256:" + strings.Repeat("a", 64), RecoveryActions: []string{"supersede_contract"}},
	})
	if _, err := valid.Encode(); err != nil {
		t.Fatalf("valid stale law revision rejected: %v", err)
	}
	valid.Error.StaleLawRevision.OldContentHash = "sha256:not-a-proof"
	if _, err := valid.Encode(); err == nil {
		t.Fatal("stale law revision accepted an invalid old content hash")
	}
}

func TestSessionPlacementRequiresCrossRepositoryKindAndCheckout(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"valid", "missing", "wrong-kind", "empty-work", "empty-project", "relative-path", "nul-path", "oversized-path"} {
		t.Run(scenario, func(t *testing.T) {
			placement := &SessionPlacement{WorkID: "work-1", ProjectID: "project-1", Directory: "/example/repository"}
			typed := TypedError{Kind: "cross_repository_claim", RecoveryAction: RecoveryAction{Kind: "use_declared_route", RequiredRefs: []string{"concord_work_start"}}, EffectState: EffectNone, SessionPlacement: placement}
			switch scenario {
			case "missing":
				typed.SessionPlacement = nil
			case "wrong-kind":
				typed.Kind = "invalid_input"
			case "empty-work":
				placement.WorkID = ""
			case "empty-project":
				placement.ProjectID = ""
			case "relative-path":
				placement.Directory = "example/repository"
			case "nul-path":
				placement.Directory = "/example/\x00"
			case "oversized-path":
				placement.Directory = "/" + strings.Repeat("a", 4096)
			}
			wantValid := scenario == "valid"
			if err := validateError(typed); (err == nil) != wantValid {
				t.Fatalf("Go error validation=%v, want valid=%v", err, wantValid)
			}
			base := NewCoreError(NewBase("placement", "concord_work_transition", "worktree_claim"), TypedError{Kind: "invalid_input", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone})
			encoded, err := base.Encode()
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]any
			if err := json.Unmarshal(encoded, &raw); err != nil {
				t.Fatal(err)
			}
			raw["error"] = typed
			encoded, err = json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := payloadschema.ValidateEnvelope(encoded); (err == nil) != wantValid {
				t.Fatalf("schema validation=%v, want valid=%v", err, wantValid)
			}
		})
	}
}

func TestDomainOverlapSequencedAndGloballyBoundedDetailsValidate(t *testing.T) {
	t.Parallel()
	base := NewBase("overlap", "concord_work_transition", "workflow_action")
	longID := strings.Repeat("d", 256)
	detail := DomainOverlapDetail{
		ProductID: "product", FromWorkID: "from", ToWorkID: "to", FromContractVersion: 4, ToContractVersion: 7,
		SharedAffectedDomainIDs: []string{longID}, SharedLawIDs: []string{longID}, SharedDomainModifications: []string{longID},
		SharedRelationTuples: []DomainOverlapRelationTuple{{SourceDomainID: longID, Kind: "depends_on", TargetDomainID: longID}},
		OverlapClasses:       []string{"architecture"}, ResolutionState: "sequenced", ResolutionKind: "depends_on",
		RecoveryActions: []string{"wait", "resolve_overlap", "terminal_work"}, SharedAffectedDomainCount: 20, SharedLawCount: 20,
		SharedDomainModificationCount: 20, SharedRelationTupleCount: 20, DetailTruncated: true,
	}
	err := NewCoreError(base, TypedError{Kind: "domain_overlap", RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectNone, DomainOverlap: &DomainOverlap{Overlaps: []DomainOverlapDetail{detail}, TotalOverlaps: 20, ReturnedOverlaps: 1, Truncated: true}})
	encoded, encodeErr := err.Encode()
	if encodeErr != nil {
		t.Fatalf("bounded sequenced overlap rejected: %v", encodeErr)
	}
	if len(encoded) >= MaxResultEnvelopeBytes {
		t.Fatalf("bounded overlap envelope is too close to the transport cap: %d", len(encoded))
	}
	detail.ResolutionState = "current"
	invalid := NewCoreError(base, TypedError{Kind: "domain_overlap", RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectNone, DomainOverlap: &DomainOverlap{Overlaps: []DomainOverlapDetail{detail}, TotalOverlaps: 1, ReturnedOverlaps: 1}})
	if _, encodeErr := invalid.Encode(); encodeErr == nil {
		t.Fatal("schema-invalid current resolution state was accepted")
	}
}

func TestEnvelopeAllowsBoundedListErrorDetails(t *testing.T) {
	t.Parallel()
	e := NewBase("req", "concord_work_define", "capture")
	e.Authority = AuthorityAuthoritative
	envelope := NewCoreError(e, TypedError{
		Kind:           "approval_required",
		RecoveryAction: RecoveryAction{Kind: "request_approval"},
		EffectState:    EffectNone,
		Details: map[string]any{
			"approval_ref":     "apr-0000000000000000000000000000000000000001",
			"scope":            []string{"product:PM1", "project:PM1.Q1"},
			"versions":         []string{"work:3"},
			"operation_digest": "sha256:abc",
		},
		ConsequenceSummary: &ConsequenceSummary{Tool: "concord_work_define", Operation: "capture", Consequence: "product_write", OperationDigest: "sha256:" + strings.Repeat("a", 64), Scope: []string{"product_id:PM1"}, Versions: []string{"work:3"}, ExpiresAt: "2026-08-20T00:00:00Z"},
	})
	if _, err := envelope.Encode(); err != nil {
		t.Fatalf("bounded approval details rejected: %v", err)
	}
}

func TestEnvelopeRejectsNestedErrorDetails(t *testing.T) {
	t.Parallel()
	e := NewBase("req", "concord_work_define", "capture")
	e.Authority = AuthorityAuthoritative
	envelope := NewCoreError(e, TypedError{
		Kind:           "approval_required",
		RecoveryAction: RecoveryAction{Kind: "request_approval"},
		EffectState:    EffectNone,
		Details:        map[string]any{"scope": map[string]any{"product_ids": []string{"PM1"}}},
	})
	if _, err := envelope.Encode(); err == nil {
		t.Fatal("nested error details accepted")
	}
}

// TestGoverningConflictOptionsAreClosedAndCoupled pins CD-0035 D1: options is a
// typed operator-choice list, permitted only on a governing-law conflict, and
// permitted rather than required so existing invariant_violation emitters are
// untouched.
func TestGoverningConflictOptionsAreClosedAndCoupled(t *testing.T) {
	t.Parallel()
	base := func() Envelope { return NewBase("req", "concord_work_define", "capture") }
	governing := []string{"clarify", "amend_contract", "accept_scope_cut"}

	valid := NewCoreError(base(), TypedError{Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: governing})
	if _, err := valid.Encode(); err != nil {
		t.Fatalf("governing-conflict options rejected: %v", err)
	}

	// Permitted, never required: an invariant_violation without options must
	// still validate, or every pre-existing emitter breaks.
	bare := NewCoreError(base(), TypedError{Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "reread_entities"}, EffectState: EffectNone})
	if _, err := bare.Encode(); err != nil {
		t.Fatalf("invariant_violation without options rejected: %v", err)
	}

	for name, bad := range map[string]TypedError{
		"wrong kind":      {Kind: "invalid_input", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: governing},
		"wrong recovery":  {Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "reread_entities"}, EffectState: EffectNone, Options: governing},
		"unknown option":  {Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: []string{"clarify", "make_it_smaller"}},
		"duplicate":       {Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: []string{"clarify", "clarify"}},
		"empty string":    {Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: []string{""}},
		"over vocabulary": {Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: []string{"clarify", "amend_contract", "accept_scope_cut", "clarify_again"}},
	} {
		if _, err := NewCoreError(base(), bad).Encode(); err == nil {
			t.Errorf("%s: invalid options accepted", name)
		}
	}
}

func TestEnvelopeRejectsUnknownFieldsAcrossEveryOutcome(t *testing.T) {
	t.Parallel()
	envelopes := []Envelope{
		newOKReadForTest(NewBase("ok", "concord_product_view", "resolve"), json.RawMessage(`{"product_id":"p-1","projects":[]}`)),
		NewPending(NewBase("pending", "concord_work_compact", "publish"), OperationRef{ID: "op-1", Kind: "publish", Version: "1", State: OperationPending, CurrentStep: "git", UpdatedAt: fixedTime()}, RecoveryAction{Kind: "reconcile_operation"}),
		NewPartial(NewBase("partial", "concord_work_compact", "publish"), OperationRef{ID: "op-1", Kind: "publish", Version: "1", State: OperationPartial, CurrentStep: "sqlite", UpdatedAt: fixedTime()}, []string{"git"}, TypedError{Kind: "operation_conflict", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial}),
		NewCoreError(NewBase("error", "concord_work_transition", "lifecycle"), TypedError{Kind: "invalid_input", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "reread_entities"}, EffectState: EffectNone}),
	}
	for _, envelope := range envelopes {
		raw, err := envelope.Encode()
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		object["unknown_top_level"] = true
		mutated, _ := json.Marshal(object)
		if _, err := decodeEnvelopeForTest(mutated); err == nil {
			t.Fatalf("unknown top-level field accepted for %s", envelope.Outcome)
		}
	}

	raw, _ := envelopes[0].Encode()
	var nested map[string]any
	_ = json.Unmarshal(raw, &nested)
	nested["resolved_scope"] = map[string]any{"product_id": "p-1", "unknown_nested": true}
	mutated, _ := json.Marshal(nested)
	if _, err := decodeEnvelopeForTest(mutated); err == nil {
		t.Fatal("unknown nested field accepted")
	}
}

func TestEnvelopeHasHardSerializedLimit(t *testing.T) {
	t.Parallel()
	if MaxResultEnvelopeBytes != 51200 {
		t.Fatalf("serialized envelope limit=%d, want 51200", MaxResultEnvelopeBytes)
	}
	payload := json.RawMessage(fmt.Sprintf(`{"value":%q}`, strings.Repeat("x", MaxResultEnvelopeBytes)))
	e := newOKReadForTest(NewBase("req", "concord_product_view", "resolve"), payload)
	if _, err := e.Encode(); err == nil {
		t.Fatal("oversize envelope accepted")
	}
}

func TestDecodeEnvelopeRejectsInvalidTrailingJSON(t *testing.T) {
	t.Parallel()
	valid := newOKReadForTest(NewBase("request-1", "concord_product_view", "resolve"), json.RawMessage(`{"product_id":"product-1","projects":[]}`))
	raw, err := valid.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{" {}", " garbage"} {
		if _, err := decodeEnvelopeForTest(append(raw, []byte(suffix)...)); err == nil {
			t.Fatalf("suffix %q was accepted", suffix)
		}
	}
}

// fixedTime matches the agent package fixture so golden wire bytes are
// unchanged by the model's owner.
func fixedTime() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }

func mustEncode(e Envelope) []byte {
	b, err := e.Encode()
	if err != nil {
		panic(err)
	}
	return b
}

// decodeEnvelopeForTest and newOKReadForTest are test-scope constructors. They
// were exported from the production package but no shipped path called either:
// production decodes through Envelope.UnmarshalJSON and builds read envelopes
// through runtime.resultEnvelope.
func decodeEnvelopeForTest(data []byte) (Envelope, error) {
	var e Envelope
	if len(data) > MaxResultEnvelopeBytes {
		return e, fmt.Errorf("agent envelope exceeds %d bytes", MaxResultEnvelopeBytes)
	}
	return e, json.Unmarshal(data, &e)
}

func newOKReadForTest(base Envelope, payload json.RawMessage) Envelope {
	base.Outcome = OutcomeOK
	base.QueryID = "PM1.Q1"
	base.Result = payload
	return base
}

// TestDomainOverlapRowWithoutSharedItemIsRefused holds the Go row validator to
// refuse the law-only overlap row the runtime delivers once its only shared
// item is removed.
func TestDomainOverlapRowWithoutSharedItemIsRefused(t *testing.T) {
	t.Parallel()
	row := DomainOverlapDetail{
		ProductID: "concord", FromWorkID: "work-a", ToWorkID: "work-b",
		FromContractVersion: 1, ToContractVersion: 1,
		SharedAffectedDomainIDs: []string{}, SharedLawIDs: []string{"spec:one"}, SharedDomainModifications: []string{}, SharedRelationTuples: []DomainOverlapRelationTuple{},
		SharedLawCount:  1,
		OverlapClasses:  []string{"law_write"},
		ResolutionState: "unresolved",
		RecoveryActions: []string{"wait", "resolve_overlap", "terminal_work", "supersede_contract"},
	}
	if err := validateDomainOverlapRow(row); err != nil {
		t.Fatalf("law-only overlap row rejected: %v", err)
	}
	empty := row
	empty.SharedLawIDs = []string{}
	empty.SharedLawCount = 0
	if err := validateDomainOverlapRow(empty); err == nil {
		t.Fatal("an overlap without any shared item must be refused")
	}
}
