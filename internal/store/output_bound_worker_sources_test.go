package store

// Worker-family rows for the structural output bound ledger. Every finite
// row cites a real producer bound: a store guard constant, a guard call
// argument with a field literal, an inline comparison guard read from the
// live source, a traced derivation or pattern-fixed admission grammar, the
// admitted input shape of a copied value, or a full-string SQLite CHECK.
// Retained TEXT with only a NUL-prefix CHECK supplies no finite capacity.
// No row freezes a numeric copy of its own output bound, and no row
// cites an output-only definition as its own source.
//
// Family-local derived evaluators below inspect the exact live source shape
// of one writer and fail closed when it changes: a renamed, duplicated,
// bodyless, or rewritten writer stops the derivation instead of admitting a
// stale bound. Printed-form comparisons are space-normalized, so pure
// formatting churn cannot break a shape pin.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"regexp/syntax"
	"strconv"
	"strings"
)

func init() {
	// Guard constants cited by more than one row.
	oracleDescription := goConstantBound("internal/store/worker_oracle.go", "oracleDescriptionMaxBytes", boundBytes)
	repoPathBytes := goConstantBound("internal/store/work_context.go", "workContextPathMaxBytes", boundBytes)
	failureHostRef := goConstantBound("internal/store/worker_failure_attribution.go", "workerFailureHostRefMaxBytes", boundBytes)

	const (
		defAcceptanceOracle = "/$defs/worker_acceptance_oracle"
		defOracleCase       = "/$defs/worker_oracle_case"
		defOracleControl    = "/$defs/worker_oracle_control"
		defOracleOwner      = "/$defs/worker_oracle_owner"
		defOracleMechanism  = "/$defs/worker_oracle_mechanism"
		defOracleFinding    = "/$defs/worker_oracle_finding"
		defOracleReceipt    = "/$defs/worker_oracle_receipt"
		defAttemptIdentity  = "/$defs/worker_failure_attempt_identity"
		defHostObservation  = "/$defs/worker_failure_host_observation"
		defIntegration      = "/$defs/worker_failure_integration_identity"
		defFailureBinding   = "/$defs/worker_failure_job_binding"
		defFailureObs       = "/$defs/worker_failure_observation"
		defFailureSource    = "/$defs/worker_failure_source"
		defFailureSubject   = "/$defs/worker_failure_subject"
		defPacket2          = "/$defs/worker_packet2"
		defPacket3          = "/$defs/worker_packet3"
		defPacket4          = "/$defs/worker_packet4"
		defPacket5          = "/$defs/worker_packet5"
		defPacket5Job       = "/$defs/worker_packet5_worker_job"
		defMeasuredPath     = "/$defs/worker_job_measured_path"
		defMeasurementBasis = "/$defs/worker_job_measurement_basis"
		defCallerAllowance  = "/$defs/worker_job_direct_caller_allowance"
		defPacketBinding    = "/$defs/worker_packet_binding"
		defWorkRecord       = "/$defs/worker_packet_work_record"
		defPacketJob        = "/$defs/worker_packet_worker_job"
		defRepairJob        = "/$defs/worker_packet_checks_repair_job"
		defChecksRepair     = "/$defs/worker_checks_repair"
		defRecovery         = "/$defs/worker_recovery_context"
	)

	// Acceptance oracle graph: recorded once through the authored input,
	// validated by validateAcceptanceOracle on append and replay, and
	// re-emitted verbatim on ready_worker_jobs and the retry packet copy.
	registerOutputBound(defAcceptanceOracle+"/properties/owners/maxItems",
		goConstantBound("internal/store/worker_oracle.go", "OracleOwnersMax", boundItems))
	registerOutputBound(defAcceptanceOracle+"/properties/cases/maxItems",
		goConstantBound("internal/store/worker_oracle.go", "OracleCasesMax", boundItems))
	registerOutputBound(defAcceptanceOracle+"/properties/controls/maxItems",
		goConstantBound("internal/store/worker_oracle.go", "OracleControlsMax", boundItems))

	// Oracle case entries: identities ride the admitted authoring shape
	// (the store's identity grammar is looser than the admission bound, so
	// the admission is the binding derivation); prose rides the shared
	// description constant the guard helper measures with len().
	registerOutputBound(defOracleCase+"/properties/case_id/maxLength",
		inputBound(defOracleCase+"/properties/case_id/maxLength"))
	registerOutputBound(defOracleCase+"/properties/owner_id/maxLength",
		inputBound(defOracleCase+"/properties/owner_id/maxLength"))
	registerOutputBound(defOracleCase+"/properties/entry_point/maxLength", oracleDescription)
	registerOutputBound(defOracleCase+"/properties/input_class/maxLength", oracleDescription)
	registerOutputBound(defOracleCase+"/properties/expected_state/maxLength", oracleDescription)
	registerOutputBound(defOracleCase+"/properties/control_ids/maxItems",
		goConstantBound("internal/store/worker_oracle.go", "OracleCaseControlsMax", boundItems))
	registerOutputBound(defOracleCase+"/properties/control_ids/items/maxLength",
		inputBound(defOracleCase+"/properties/control_ids/items/maxLength"))

	// Oracle control entries: same split between the admitted identity
	// shapes and the store's own byte and count constants.
	registerOutputBound(defOracleControl+"/properties/control_id/maxLength",
		inputBound(defOracleControl+"/properties/control_id/maxLength"))
	registerOutputBound(defOracleControl+"/properties/owner_id/maxLength",
		inputBound(defOracleControl+"/properties/owner_id/maxLength"))
	registerOutputBound(defOracleControl+"/properties/argv/maxItems",
		goConstantBound("internal/store/worker_oracle.go", "OracleArgvMax", boundItems))
	registerOutputBound(defOracleControl+"/properties/argv/items/maxLength",
		goConstantBound("internal/store/worker_oracle.go", "oracleArgMaxBytes", boundBytes))
	registerOutputBound(defOracleControl+"/properties/case_ids/maxItems",
		goConstantBound("internal/store/worker_oracle.go", "OracleControlCasesMax", boundItems))
	registerOutputBound(defOracleControl+"/properties/case_ids/items/maxLength",
		inputBound(defOracleControl+"/properties/case_ids/items/maxLength"))
	registerOutputBound(defOracleControl+"/properties/cwd/maxLength", repoPathBytes)
	registerOutputBound(defOracleControl+"/properties/predicate_ids/maxItems",
		goConstantBound("internal/store/worker_oracle.go", "OracleControlPredicatesMax", boundItems))
	registerOutputBound(defOracleControl+"/properties/predicate_ids/items/maxLength",
		inputBound(defOracleControl+"/properties/predicate_ids/items/maxLength"))
	registerOutputBound(defOracleControl+"/properties/readiness_evidence_refs/maxItems",
		goConstantBound("internal/store/worker_oracle.go", "OracleReadinessRefsMax", boundItems))
	registerOutputBound(defOracleControl+"/properties/readiness_evidence_refs/items/maxLength",
		inputBound(defOracleControl+"/properties/readiness_evidence_refs/items/maxLength"))

	// Oracle owner entries.
	registerOutputBound(defOracleOwner+"/properties/domain_id/maxLength",
		goConstantBound("internal/store/worker_oracle.go", "oracleDomainIDMaxBytes", boundBytes))
	registerOutputBound(defOracleOwner+"/properties/law_bindings/maxItems",
		goConstantBound("internal/store/worker_oracle.go", "OracleLawBindingsMax", boundItems))
	registerOutputBound(defOracleOwner+"/properties/obligation/maxLength", oracleDescription)
	registerOutputBound(defOracleOwner+"/properties/owner_id/maxLength",
		inputBound(defOracleOwner+"/properties/owner_id/maxLength"))
	registerOutputBound(defOracleOwner+"/properties/predicate_ids/maxItems",
		goConstantBound("internal/store/worker_oracle.go", "OracleOwnerPredicatesMax", boundItems))
	registerOutputBound(defOracleOwner+"/properties/predicate_ids/items/maxLength",
		inputBound(defOracleOwner+"/properties/predicate_ids/items/maxLength"))

	// Oracle mechanism and law-binding clause.
	registerOutputBound(defOracleMechanism+"/properties/entry_point/maxLength", oracleDescription)
	registerOutputBound(defOracleMechanism+"/properties/path/maxLength", repoPathBytes)
	registerOutputBound(defOracleMechanism+"/properties/project_id/maxLength",
		goConstantBound("internal/store/worker_oracle.go", "oracleProjectIDMaxBytes", boundBytes))
	registerOutputBound("/$defs/worker_oracle_law_binding/properties/clause/maxLength", oracleDescription)

	// Oracle findings ride the worker's terminal report; the store's shape
	// validator (validateWorkerOracleFindingShape) owns their bounds, and
	// the case/control identities additionally join the dispatched oracle,
	// whose own ids were admitted at the authoring input.
	registerOutputBoundWithTwin(defOracleFinding+"/properties/case_ids/items/maxLength",
		inputBound(defOracleCase+"/properties/case_id/maxLength"))
	registerOutputBoundWithTwin(defOracleFinding+"/properties/case_ids/maxItems",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleFindingCaseMax", boundItems))
	registerOutputBoundWithTwin(defOracleFinding+"/properties/continues_finding_id/maxLength", workerRankedFindingIDBound())
	registerOutputBoundWithTwin(defOracleFinding+"/properties/control_ids/items/maxLength",
		inputBound(defOracleControl+"/properties/control_id/maxLength"))
	registerOutputBoundWithTwin(defOracleFinding+"/properties/control_ids/maxItems",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleFindingControlMax", boundItems))
	registerOutputBoundWithTwin(defOracleFinding+"/properties/entry_point/maxLength",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleFindingEntryPointMaxBytes", boundBytes))
	registerOutputBoundWithTwin(defOracleFinding+"/properties/evidence_refs/items/maxLength",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleFindingEvidenceRefBytes", boundBytes))
	registerOutputBoundWithTwin(defOracleFinding+"/properties/evidence_refs/maxItems",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleFindingEvidenceRefsMax", boundItems))
	registerOutputBoundWithTwin(defOracleFinding+"/properties/failure_family/maxLength",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleFindingFamilyMaxBytes", boundBytes))
	registerOutputBoundWithTwin(defOracleFinding+"/properties/law_bindings/maxItems",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleFindingLawBindingsMax", boundItems))
	// A follow_up finding escapes the owner join, so the writer bound is
	// the owner: grammar itself, live-derived from the anchored pattern.
	// The retained twin admits the full pattern; the packet keeps its cap.
	registerRetainedTwinBound(defOracleFinding+"/properties/owner_id/maxLength",
		goPatternBound("internal/store/worker_oracle.go", "oracleOwnerIDPattern"))
	// validateWorkerOracleFindingShape bounds every predicate id through
	// validWorkerPredicateID's inline comparison.
	registerOutputBoundWithTwin(defOracleFinding+"/properties/predicate_ids/items/maxLength",
		goInlineBound("internal/store/worker_lanes.go", "validWorkerPredicateID", "len(id)", boundBytes))
	registerOutputBoundWithTwin(defOracleFinding+"/properties/predicate_ids/maxItems",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleFindingPredicateMax", boundItems))
	registerOutputBoundWithTwin(defOracleFinding+"/properties/variant_of/maxLength", workerRankedFindingIDBound())

	// Oracle receipts: shape validator plus the receipt join, which
	// resolves every named control and case against the dispatched oracle.
	// The subject commit is the core work-context subject byte-compared at
	// the receipt join, so it rides the worktree OID grammar.
	registerOutputBound(defOracleReceipt+"/properties/case_ids/items/maxLength",
		inputBound(defOracleCase+"/properties/case_id/maxLength"))
	registerOutputBound(defOracleReceipt+"/properties/case_ids/maxItems",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleReceiptCasesMax", boundItems))
	registerOutputBound(defOracleReceipt+"/properties/control_ids/items/maxLength",
		inputBound(defOracleControl+"/properties/control_id/maxLength"))
	registerOutputBound(defOracleReceipt+"/properties/control_ids/maxItems",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleReceiptControlsMax", boundItems))
	registerOutputBound(defOracleReceipt+"/properties/evidence_refs/items/maxLength",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleReceiptEvidenceRefBytes", boundBytes))
	registerOutputBound(defOracleReceipt+"/properties/evidence_refs/maxItems",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleReceiptEvidenceRefsMax", boundItems))
	registerOutputBound(defOracleReceipt+"/properties/run_ref/maxLength",
		goConstantBound("internal/store/worker_oracle_findings.go", "oracleReceiptRunRefMaxBytes", boundBytes))
	registerOutputBound(defOracleReceipt+"/properties/subject_commit/maxLength",
		goPatternBound("internal/store/worktrees.go", "worktreeSHAPattern"))

	// Failure attribution. Guard call sites carry the limit as argument 3
	// of workerFailureBoundedText (len-based, so bytes) behind a literal
	// field name; fields inside the host-observation loop carry a variable
	// name, so they cite the shared constant instead.
	attemptGuard := goCallBound("internal/store/worker_failure_attribution.go", "validateWorkerFailureAttemptIdentity", "workerFailureBoundedText", "attempt_id", 3, boundBytes)
	hostEventGuard := goCallBound("internal/store/worker_failure_attribution.go", "validateWorkerFailureHostObservation", "workerFailureBoundedText", "host event_type", 3, boundBytes)
	subjectLocatorGuard := goCallBound("internal/store/worker_failure_attribution.go", "validateWorkerFailureSubjectRef", "workerFailureBoundedText", "subject reference locator", 3, boundBytes)
	resultRefGuard := goCallBound("internal/store/worker_failure_attribution.go", "validateWorkerFailureIntegrationIdentity", "workerFailureBoundedText", "input result_ref", 3, boundBytes)
	sourceLocatorGuard := goCallBound("internal/store/worker_failure_attribution.go", "validateWorkerFailureSource", "workerFailureBoundedText", "source locator", 3, boundBytes)
	sourceMessageGuard := goCallBound("internal/store/worker_failure_attribution.go", "validateWorkerFailureSource", "workerFailureBoundedText", "source message", 3, boundBytes)
	diagnosisGuard := goCallBound("internal/store/worker_failure_attribution.go", "validateWorkerFailureObservation", "workerFailureBoundedText", "diagnosis", 3, boundBytes)

	// Digest rows: every failure-attribution digest is admitted only
	// through workerFailureDigestPattern, whose sole path is the anchored
	// sha256 provenance pattern fixed at 71 ASCII characters.
	digestSource := workerDigestFieldBound("validateWorkerFailureSource", "source digest")
	digestSubject := workerDigestFieldBound("validateWorkerFailureSubjectRef", "subject reference digest")
	digestAttempt := workerDigestFieldBound("validateWorkerFailureAttemptIdentity", "attempt packet_digest")
	digestBinding := workerDigestFieldBound("validateWorkerFailureJobBinding", "job binding digest")
	digestIntegration := workerDigestSubjectBound("validateWorkerFailureIntegrationIdentity")
	recordedJobDigest := workerRecordedJobDigestBound()

	registerOutputBound(defAttemptIdentity+"/oneOf/0/properties/attempt_id/maxLength", attemptGuard)
	registerOutputBound(defAttemptIdentity+"/oneOf/0/properties/attempt_id/x-maxBytes", attemptGuard)
	registerOutputBound(defAttemptIdentity+"/oneOf/0/properties/packet_digest/maxLength", digestAttempt)
	registerOutputBound(defAttemptIdentity+"/oneOf/1/properties/attempt_id/maxLength", attemptGuard)
	registerOutputBound(defAttemptIdentity+"/oneOf/1/properties/attempt_id/x-maxBytes", attemptGuard)
	registerOutputBound(defAttemptIdentity+"/oneOf/1/properties/packet_digest/maxLength", digestAttempt)

	registerOutputBound(defHostObservation+"/properties/event_type/maxLength", hostEventGuard)
	registerOutputBound(defHostObservation+"/properties/event_type/x-maxBytes", hostEventGuard)
	registerOutputBound(defHostObservation+"/properties/session_id/maxLength", failureHostRef)
	registerOutputBound(defHostObservation+"/properties/session_id/x-maxBytes", failureHostRef)
	registerOutputBound(defHostObservation+"/properties/message_id/maxLength", failureHostRef)
	registerOutputBound(defHostObservation+"/properties/message_id/x-maxBytes", failureHostRef)
	registerOutputBound(defHostObservation+"/properties/error_name/maxLength", failureHostRef)
	registerOutputBound(defHostObservation+"/properties/error_name/x-maxBytes", failureHostRef)
	registerOutputBound(defHostObservation+"/properties/error_code/maxLength", failureHostRef)
	registerOutputBound(defHostObservation+"/properties/error_code/x-maxBytes", failureHostRef)

	registerOutputBound(defIntegration+"/oneOf/0/properties/input_results/items/properties/result_ref/maxLength", resultRefGuard)
	registerOutputBound(defIntegration+"/oneOf/0/properties/input_results/items/properties/result_ref/x-maxBytes", resultRefGuard)
	registerOutputBound(defIntegration+"/oneOf/0/properties/native_run/properties/digest/maxLength", digestIntegration)
	registerOutputBound(defIntegration+"/oneOf/0/properties/native_run/properties/locator/maxLength", subjectLocatorGuard)
	registerOutputBound(defIntegration+"/oneOf/0/properties/native_run/properties/locator/x-maxBytes", subjectLocatorGuard)
	registerOutputBound(defIntegration+"/oneOf/1/properties/native_run/properties/digest/maxLength", digestIntegration)
	registerOutputBound(defIntegration+"/oneOf/1/properties/native_run/properties/locator/maxLength", subjectLocatorGuard)
	registerOutputBound(defIntegration+"/oneOf/1/properties/native_run/properties/locator/x-maxBytes", subjectLocatorGuard)

	registerOutputBound(defFailureBinding+"/properties/digest/maxLength", digestBinding)
	registerOutputBound(defFailureBinding+"/properties/job_id/maxLength",
		inputBound(defPacketJob+"/properties/job_id/maxLength"))

	registerOutputBound(defFailureObs+"/properties/diagnosis/maxLength", diagnosisGuard)
	registerOutputBound(defFailureObs+"/properties/diagnosis/x-maxBytes", diagnosisGuard)
	registerOutputBound(defFailureObs+"/properties/sources/maxItems",
		goConstantBound("internal/store/worker_failure_attribution.go", "WorkerFailureAttributionMaxSources", boundItems))

	registerOutputBound(defFailureSource+"/properties/digest/maxLength", digestSource)
	registerOutputBound(defFailureSource+"/properties/digest/x-maxBytes", digestSource)
	registerOutputBound(defFailureSource+"/properties/locator/maxLength", sourceLocatorGuard)
	registerOutputBound(defFailureSource+"/properties/locator/x-maxBytes", sourceLocatorGuard)
	registerOutputBound(defFailureSource+"/properties/message/maxLength", sourceMessageGuard)
	registerOutputBound(defFailureSource+"/properties/message/x-maxBytes", sourceMessageGuard)

	registerOutputBound(defFailureSubject+"/properties/digest/maxLength", digestSubject)
	registerOutputBound(defFailureSubject+"/properties/locator/maxLength", subjectLocatorGuard)
	registerOutputBound(defFailureSubject+"/properties/locator/x-maxBytes", subjectLocatorGuard)

	// Retry lane packets: the packet builder copies recorded members
	// verbatim, so each copied member rides the bound of the record it
	// copies. The proposal constraints ride the admitted record_proposal
	// shape; the retry derivation never authors constraints of its own.
	proposalConstraints := inputBound("/$defs/work_transition_action_variant_record_proposal/properties/fields/properties/constraints/maxItems")
	proposalConstraintText := inputBound("/$defs/proposal_text/maxLength")
	for _, packet := range []string{defPacket2, defPacket3, defPacket4, defPacket5} {
		registerOutputBound(packet+"/properties/inputs/properties/constraints/items/maxLength", proposalConstraintText)
		registerOutputBound(packet+"/properties/inputs/properties/constraints/maxItems", proposalConstraints)
	}
	// Dispatch schema admission bounds task, and recovery preserves the
	// already-admitted context (new dispatches refuse prose context).
	// packet2 and packet4 reach only retained output; they carry packet3's
	// task and legacy prose context, so packet3's admitted input bounds them.
	for _, packet := range []string{defPacket2, defPacket3, defPacket4, defPacket5} {
		source := packet
		if packet == defPacket2 || packet == defPacket4 {
			source = defPacket3
		}
		for _, member := range []string{"context", "task"} {
			pointer := packet + "/properties/inputs/properties/" + member + "/maxLength"
			registerOutputBound(pointer, inputBound(source+"/properties/inputs/properties/"+member+"/maxLength"))
		}
	}

	// The assigned result is one fixed token of the generated lane map,
	// byte-compared into the packet binding at dispatch.
	registerOutputBound(defPacketBinding+"/properties/assigned_result/maxLength", workerGeneratedResultsBound())

	registerOutputBound("/$defs/worker_packet_outcome_predicates/maxItems",
		maxBound(
			inputBound("/$defs/work_transition_action_variant_approve_contract/properties/fields/oneOf/0/properties/outcome_predicates/maxItems"),
			inputBound("/$defs/work_transition_action_variant_approve_contract/properties/fields/oneOf/1/properties/outcome_predicates/maxItems")))

	// Packet work record: copied verbatim from work_items. Dispatch
	// admission validates the packet that embeds it, so the narrative and
	// value statement ride their admitted caps whatever the retained row
	// holds.
	registerOutputBound(defWorkRecord+"/properties/narrative/maxLength",
		inputBound(defWorkRecord+"/properties/narrative/maxLength"))
	registerOutputBound(defWorkRecord+"/properties/task/maxLength",
		workerRecordedTaskBound())
	registerOutputBound(defWorkRecord+"/properties/value_statement/maxLength",
		inputBound(defWorkRecord+"/properties/value_statement/maxLength"))

	// The packet worker_job must byte-equal the recorded revision view at
	// dispatch (validateWorkerPacketJob), and the recorded revision was
	// admitted through the authoring input that carries this same
	// definition, so the admitted shape is the copied value's bound. The
	// digest is core-derived over the content and re-derived equal at the
	// fold, so it rides the recorded derivation bound. The packet5 job def
	// is the size-capable mirror: the same shape under a versioned name,
	// with the dispatch-time caller exclusions rows registered beside the
	// loop below. The checks-repair mirror is output-only, but its common
	// members copy the same packetJobFromView revision as the admitted job.
	for _, job := range []string{defPacketJob, defRepairJob, defPacket5Job} {
		registerOutputBound(job+"/properties/digest/maxLength", recordedJobDigest)
		source := job
		if job == defRepairJob {
			source = defPacketJob
		}
		for _, member := range []string{
			"checks/items/maxLength", "checks/maxItems", "job_id/maxLength", "objective/maxLength",
			"path_scope/items/maxLength", "path_scope/maxItems", "predicate_ids/items/maxLength", "predicate_ids/maxItems",
			"prerequisites/items/properties/job_id/maxLength", "prerequisites/items/properties/result_ref/maxLength", "prerequisites/maxItems",
			"project_scope/maxLength", "reserved_integration/maxLength", "stopping_condition/maxLength",
			"unresolved_refs/items/maxLength", "unresolved_refs/maxItems",
		} {
			pointer := job + "/properties/" + member
			registerOutputBound(pointer, inputBound(source+"/properties/"+member))
		}
	}

	// The size surface (CD-0227 D1): the recorded basis and
	// allowance ride the field guards validateWorkerJobSizeShape enforces
	// on append and replay alike; the measured inventory paths and generator
	// references ride the shared normalized-path grammar through their one
	// routing site; and the packet5 job's dispatch-time caller exclusions
	// are the core's own computed set.
	registerOutputBound(defCallerAllowance+"/properties/project_id/maxLength",
		goInlineBound("internal/store/worker_job_caller_scope.go", "validateWorkerJobAllowanceShape", "len(allowance.ProjectID)", boundBytes))
	registerOutputBound(defCallerAllowance+"/properties/mechanisms/maxItems",
		goInlineBound("internal/store/worker_job_caller_scope.go", "validateWorkerJobAllowanceShape", "len(allowance.Mechanisms)", boundItems))
	registerOutputBound(defCallerAllowance+"/properties/domain_ids/maxItems",
		goInlineBound("internal/store/worker_job_caller_scope.go", "validateWorkerJobAllowanceShape", "len(allowance.DomainIDs)", boundItems))
	// Each allowance Domain id is admitted through the shared oracle Domain
	// bound; the comparison rides validateUniqueOracleReferences' admission
	// closure, so the row cites the constant the closure compares against,
	// exactly like the oracle owner's domain row.
	registerOutputBound(defCallerAllowance+"/properties/domain_ids/items/maxLength",
		goConstantBound("internal/store/worker_oracle.go", "oracleDomainIDMaxBytes", boundBytes))
	registerOutputBound(defCallerAllowance+"/properties/law_bindings/maxItems",
		goInlineBound("internal/store/worker_job_caller_scope.go", "validateWorkerJobAllowanceShape", "len(allowance.LawBindings)", boundItems))

	// The caller exclusions are computed once at dispatch admission and
	// bound to the packet, never the job digest. The packet's declared
	// bounds — the count and the per-path Unicode character width — are
	// enforced by one shared guard both producing sites route through: the
	// dispatch computation owner, and the continuity ready projection whose
	// over-bound set is omitted whole so the read stays available.
	registerOutputBound(defPacket5Job+"/properties/caller_exclusions/maxItems",
		workerCallerExclusionGuardBound("len(paths)", boundItems))
	registerOutputBound(defPacket5Job+"/properties/caller_exclusions/items/maxLength",
		workerCallerExclusionGuardBound("utf8.RuneCountInString(path)", boundCharacters))

	// Repair metadata is guarded on append and replay before a packet copies it.
	registerOutputBound(defChecksRepair+"/properties/reason/maxLength",
		goInlineBound("internal/store/worker_jobs.go", "validateWorkerJobRecordedPayload", "len(payload.ChecksRepair.Reason)", boundBytes))
	registerOutputBound(defChecksRepair+"/properties/revisions/maxItems",
		goInlineBound("internal/store/worker_jobs.go", "validateWorkerJobRecordedPayload", "len(entries)", boundItems))
	registerOutputBound(defChecksRepair+"/properties/revisions/items/properties/checks/maxItems",
		goInlineBound("internal/store/worker_jobs.go", "validateWorkerJobRecordedPayload", "len(entry.Checks)", boundItems))
	// Old checks copy the named revision's admitted checks verbatim.
	registerOutputBound(defChecksRepair+"/properties/revisions/items/properties/checks/items/maxLength",
		inputBound(defPacketJob+"/properties/checks/items/maxLength"))

	// Recovery context re-marshals the retained dispatch payload verbatim;
	// its provenance was admitted by ValidateWorkerHostProvenance, whose
	// bounds are inline guards, and its worker_job binding joins the
	// recorded revision.
	registerOutputBound(defRecovery+"/properties/dispatch/properties/host_provenance/properties/sources/items/properties/path/maxLength",
		goInlineBound("internal/store/worker_lanes.go", "ValidateWorkerHostProvenance", "len(source.Path)", boundBytes))
	registerOutputBound(defRecovery+"/properties/dispatch/properties/host_provenance/properties/sources/maxItems",
		goInlineBound("internal/store/worker_lanes.go", "ValidateWorkerHostProvenance", "len(p.Sources)", boundItems))
	registerOutputBound(defRecovery+"/properties/dispatch/properties/worker_job/properties/digest/maxLength", recordedJobDigest)
	registerOutputBound(defRecovery+"/properties/dispatch/properties/worker_job/properties/job_id/maxLength",
		inputBound(defPacketJob+"/properties/job_id/maxLength"))
}

// ---------------------------------------------------------------------------
// Family-local derived evaluators
// ---------------------------------------------------------------------------

// workerUniqueFunc loads file and returns the single declaration of the
// named function, failing closed on absence, duplication, or a missing
// body.
func workerUniqueFunc(rt *ledgerRuntime, file, name string) (*ast.FuncDecl, error) {
	goS, err := rt.loadGo(file)
	if err != nil {
		return nil, err
	}
	fns := ledgerFuncDeclsNamed(goS.file, name)
	if len(fns) == 0 {
		return nil, fmt.Errorf("worker bound: %s is not declared in %s", name, file)
	}
	if len(fns) > 1 || fns[0].Body == nil {
		return nil, fmt.Errorf("worker bound: %s is ambiguous or bodyless in %s", name, file)
	}
	return fns[0], nil
}

// workerCountCalls counts the calls whose callee matches name.
func workerCountCalls(node ast.Node, name string) int {
	count := 0
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && ledgerCalleeMatches(call, name) {
			count++
		}
		return true
	})
	return count
}

// workerCountAllCalls counts every call expression.
func workerCountAllCalls(node ast.Node) int {
	count := 0
	ast.Inspect(node, func(n ast.Node) bool {
		if _, ok := n.(*ast.CallExpr); ok {
			count++
		}
		return true
	})
	return count
}

// workerCountComparisons counts the comparison expressions whose printed
// form, space-normalized, equals want.
func workerCountComparisons(node ast.Node, want string) int {
	count := 0
	ast.Inspect(node, func(n ast.Node) bool {
		binary, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		switch binary.Op {
		case token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ:
			if workerCondensed(ledgerExprText(binary)) == workerCondensed(want) {
				count++
			}
		}
		return true
	})
	return count
}

// workerCondensed strips spaces from a printed expression so shape pins
// compare structure, not formatting.
func workerCondensed(s string) string {
	return strings.ReplaceAll(s, " ", "")
}

// workerStringLiterals collects every unquotable string literal in node.
func workerStringLiterals(node ast.Node) []string {
	var found []string
	ast.Inspect(node, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err == nil {
			found = append(found, value)
		}
		return true
	})
	return found
}

// workerASCIIWidth emits equal byte and character constraints for a value
// whose writer alphabet was just proven pure ASCII.
func workerASCIIWidth(chars int64, describe string) evaluatedBound {
	return evaluatedBound{
		kind:     "derived",
		describe: describe,
		constraints: []boundConstraint{
			{unit: boundCharacters, value: chars, from: describe},
			{unit: boundBytes, value: chars, from: describe + " (proven ASCII)"},
		},
	}
}

// workerProvenancePatternText reads the single regexp.MustCompile string
// literal behind workerProvenancePattern.
func workerProvenancePatternText(goS *goSource) (string, error) {
	const symbol = "workerProvenancePattern"
	found, count := "", 0
	for _, decl := range goS.file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range valueSpec.Names {
				if name.Name != symbol || i >= len(valueSpec.Values) {
					continue
				}
				call, ok := valueSpec.Values[i].(*ast.CallExpr)
				if !ok || !ledgerCalleeMatches(call, "regexp.MustCompile") || len(call.Args) != 1 {
					return "", fmt.Errorf("%s is no longer one regexp.MustCompile call", symbol)
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return "", fmt.Errorf("the %s argument is not a string literal", symbol)
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					return "", err
				}
				count++
				found = value
			}
		}
	}
	switch {
	case count == 0:
		return "", fmt.Errorf("%s is not declared", symbol)
	case count > 1:
		return "", fmt.Errorf("%s is declared %d times", symbol, count)
	}
	return found, nil
}

// workerPatternASCIIOnly verifies the parsed pattern admits no non-ASCII
// character, so its derived character width is also its byte width.
func workerPatternASCIIOnly(re *syntax.Regexp) error {
	switch re.Op {
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if r > 0x7f {
				return fmt.Errorf("the pattern admits the non-ASCII character %q", r)
			}
		}
		return nil
	case syntax.OpCharClass:
		for i := 0; i+1 < len(re.Rune); i += 2 {
			if re.Rune[i+1] > 0x7f {
				return fmt.Errorf("the pattern admits non-ASCII characters in one class")
			}
		}
		return nil
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return fmt.Errorf("the pattern admits any character, including non-ASCII")
	case syntax.OpCapture, syntax.OpQuest, syntax.OpStar, syntax.OpPlus, syntax.OpRepeat,
		syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			if err := workerPatternASCIIOnly(sub); err != nil {
				return err
			}
		}
		return nil
	case syntax.OpEmptyMatch, syntax.OpNoMatch, syntax.OpBeginLine, syntax.OpEndLine,
		syntax.OpBeginText, syntax.OpEndText:
		return nil
	default:
		return fmt.Errorf("unsupported pattern element %v", re.Op)
	}
}

// workerDigestGrammarWidth proves the shared digest admission grammar and
// returns its fixed width. Three live facts must hold together: the
// anchored provenance pattern derives 71 characters, the same live pattern
// admits only ASCII so one character is one byte, and
// workerFailureDigestPattern admits a digest value through that pattern
// and nothing else.
func workerDigestGrammarWidth(rt *ledgerRuntime, census *boundCensus) (int64, error) {
	evaluated, err := rt.evaluateSource(goPatternBound("internal/store/worker_lanes.go", "workerProvenancePattern"), census)
	if err != nil {
		return 0, err
	}
	characters, ok := evaluated.capacity(boundCharacters)
	if !ok || characters <= 0 {
		return 0, fmt.Errorf("the provenance pattern carries no character capacity")
	}
	goS, err := rt.loadGo("internal/store/worker_lanes.go")
	if err != nil {
		return 0, err
	}
	pattern, err := workerProvenancePatternText(goS)
	if err != nil {
		return 0, err
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return 0, fmt.Errorf("the provenance pattern does not parse: %w", err)
	}
	if err := workerPatternASCIIOnly(parsed); err != nil {
		return 0, err
	}
	guard, err := workerUniqueFunc(rt, "internal/store/worker_failure_attribution.go", "workerFailureDigestPattern")
	if err != nil {
		return 0, err
	}
	if workerCountAllCalls(guard) != 2 ||
		workerCountCalls(guard, "workerProvenancePattern.MatchString") != 1 ||
		workerCountCalls(guard, "workerFailureBoundFailure") != 1 {
		return 0, fmt.Errorf("workerFailureDigestPattern no longer refuses exactly on the provenance pattern match")
	}
	return characters, nil
}

// workerDigestFieldBound closes one digest row through its admission site:
// the named validator must route the named field literal through
// workerFailureDigestPattern exactly once, and the shared grammar fixes the
// width.
func workerDigestFieldBound(siteFunction, siteField string) outputBoundSource {
	const siteFile = "internal/store/worker_failure_attribution.go"
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		site, err := workerUniqueFunc(rt, siteFile, siteFunction)
		if err != nil {
			return evaluatedBound{}, err
		}
		wired := 0
		ast.Inspect(site, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, "workerFailureDigestPattern") || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if value, err := strconv.Unquote(lit.Value); err == nil && value == siteField {
				wired++
			}
			return true
		})
		if wired != 1 {
			return evaluatedBound{}, fmt.Errorf("%s routes %q through workerFailureDigestPattern %d times", siteFunction, siteField, wired)
		}
		width, err := workerDigestGrammarWidth(rt, census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("worker digest bound: %w", err)
		}
		describe := fmt.Sprintf("worker digest bound: %s:%s admits %q only through the provenance pattern, fixed at %d ASCII characters", siteFile, siteFunction, siteField, width)
		return workerASCIIWidth(width, describe), nil
	}}
}

// workerDigestSubjectBound closes the integration native-run digest rows:
// the integration identity must hand its one subject reference to
// validateWorkerFailureSubjectRef, whose digest guard is the shared
// grammar.
func workerDigestSubjectBound(routeFunction string) outputBoundSource {
	const file = "internal/store/worker_failure_attribution.go"
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		route, err := workerUniqueFunc(rt, file, routeFunction)
		if err != nil {
			return evaluatedBound{}, err
		}
		if workerCountCalls(route, "validateWorkerFailureSubjectRef") != 1 {
			return evaluatedBound{}, fmt.Errorf("%s no longer routes its subject reference through validateWorkerFailureSubjectRef exactly once", routeFunction)
		}
		return workerDigestFieldBound("validateWorkerFailureSubjectRef", "subject reference digest").derive(rt, census)
	}}
}

// workerRecordedJobDigestBound closes the recorded worker-job digest rows.
// Three live facts must hold together: DeriveWorkerJobDigest still derives
// exactly one sha256-prefixed digest (one sum, one hex encoding, the sole
// "sha256:" literal, one digest-shaped return), the worker.job_recorded
// fold still pattern-checks and re-derives the digest equal, the dispatch
// binding still byte-compares the packet worker_job against the recorded
// revision, and the packet projection still copies the recorded digest.
func workerRecordedJobDigestBound() outputBoundSource {
	const file = "internal/store/worker_jobs.go"
	const digestReturn = `"sha256:" + hex.EncodeToString(sum[:])`
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		deriveFn, err := workerUniqueFunc(rt, file, "DeriveWorkerJobDigest")
		if err != nil {
			return evaluatedBound{}, err
		}
		if workerCountCalls(deriveFn, "sha256.Sum256") != 1 || workerCountCalls(deriveFn, "hex.EncodeToString") != 1 {
			return evaluatedBound{}, fmt.Errorf("DeriveWorkerJobDigest no longer derives exactly one sha256-prefixed digest")
		}
		prefixLiterals := 0
		for _, value := range workerStringLiterals(deriveFn) {
			if value == "sha256:" {
				prefixLiterals++
				continue
			}
			if strings.Contains(value, "sha256") {
				return evaluatedBound{}, fmt.Errorf("DeriveWorkerJobDigest carries the unexpected digest literal %q", value)
			}
		}
		if prefixLiterals != 1 {
			return evaluatedBound{}, fmt.Errorf("DeriveWorkerJobDigest carries %d \"sha256:\" literals", prefixLiterals)
		}
		digestReturns := 0
		ast.Inspect(deriveFn, func(n ast.Node) bool {
			results, ok := n.(*ast.ReturnStmt)
			if !ok || len(results.Results) != 1 || workerCondensed(ledgerExprText(results.Results[0])) != workerCondensed(digestReturn) {
				return true
			}
			digestReturns++
			return true
		})
		if digestReturns != 1 {
			return evaluatedBound{}, fmt.Errorf("DeriveWorkerJobDigest no longer returns %s", digestReturn)
		}
		fold, err := workerUniqueFunc(rt, file, "validateWorkerJobRecordedPayload")
		if err != nil {
			return evaluatedBound{}, err
		}
		if workerCountCalls(fold, "workerProvenancePattern.MatchString") != 1 || workerCountCalls(fold, "DeriveWorkerJobDigest") != 1 {
			return evaluatedBound{}, fmt.Errorf("the worker.job_recorded fold no longer pattern-checks and re-derives the digest")
		}
		join, err := workerUniqueFunc(rt, file, "validateWorkerPacketJob")
		if err != nil {
			return evaluatedBound{}, err
		}
		if workerCountComparisons(join, "view.Binding != binding") != 1 {
			return evaluatedBound{}, fmt.Errorf("validateWorkerPacketJob no longer byte-compares the packet worker_job against the recorded revision")
		}
		projection, err := workerUniqueFunc(rt, file, "packetJobFromView")
		if err != nil {
			return evaluatedBound{}, err
		}
		copied := 0
		ast.Inspect(projection, func(n ast.Node) bool {
			keyValue, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			if ident, ok := keyValue.Key.(*ast.Ident); ok && ident.Name == "Digest" && ledgerExprText(keyValue.Value) == "view.Binding.Digest" {
				copied++
			}
			return true
		})
		if copied != 1 {
			return evaluatedBound{}, fmt.Errorf("packetJobFromView no longer copies the recorded digest verbatim")
		}
		width, err := workerDigestGrammarWidth(rt, census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("worker recorded job digest bound: %w", err)
		}
		describe := fmt.Sprintf("worker recorded job digest bound: the digest is the DeriveWorkerJobDigest output, re-derived equal and pattern-checked at the fold, byte-compared at dispatch, and copied verbatim (%d ASCII characters)", width)
		return workerASCIIWidth(width, describe), nil
	}}
}

// workerRankedFindingIDBound derives the ranked finding identity width from
// the live formatter and the live per-event finding bounds. The formatter
// must still be the single fixed return of fmt.Sprintf("finding:%d:%d",
// seq, contextCount+reviewIndex); the ordinal budget is the live sum of the
// context-finding and review-finding guards, and each %d verb costs at most
// the full int64 decimal width including a sign.
func workerRankedFindingIDBound() outputBoundSource {
	const file = "internal/store/worker_oracle_findings.go"
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		fn, err := workerUniqueFunc(rt, file, "workerRankedFindingID")
		if err != nil {
			return evaluatedBound{}, err
		}
		params := fn.Type.Params.List
		if len(params) != 2 || len(params[0].Names) != 1 || len(params[1].Names) != 2 {
			return evaluatedBound{}, fmt.Errorf("workerRankedFindingID no longer takes the sequence and the two count parameters")
		}
		if seq, ok := params[0].Type.(*ast.Ident); !ok || seq.Name != "int64" {
			return evaluatedBound{}, fmt.Errorf("the workerRankedFindingID sequence parameter is no longer int64")
		}
		if counts, ok := params[1].Type.(*ast.Ident); !ok || counts.Name != "int" {
			return evaluatedBound{}, fmt.Errorf("the workerRankedFindingID count parameters are no longer int")
		}
		returns, shaped := 0, false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			results, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			returns++
			if len(results.Results) != 1 {
				return true
			}
			call, ok := results.Results[0].(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, "fmt.Sprintf") || len(call.Args) != 3 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			format, err := strconv.Unquote(lit.Value)
			if err != nil || format != "finding:%d:%d" {
				return true
			}
			if workerCondensed(ledgerExprText(call.Args[1])) != workerCondensed(params[0].Names[0].Name) {
				return true
			}
			if workerCondensed(ledgerExprText(call.Args[2])) != workerCondensed(params[1].Names[0].Name+"+"+params[1].Names[1].Name) {
				return true
			}
			shaped = true
			return true
		})
		if returns != 1 || !shaped {
			return evaluatedBound{}, fmt.Errorf("workerRankedFindingID no longer formats the fixed finding:<seq>:<ordinal> identity from the sequence and the ordinal sum")
		}
		contextBound, err := rt.evaluateSource(goInlineBound("internal/store/worker_context_findings.go", "ValidateWorkerContextFindings", "len(findings)", boundItems), census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("worker ranked finding bound: %w", err)
		}
		reviewBound, err := rt.evaluateSource(goInlineBound("internal/store/worker_lanes.go", "validateWorkerReviewBlock", "len(review.Findings)", boundItems), census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("worker ranked finding bound: %w", err)
		}
		contextItems, ok := contextBound.capacity(boundItems)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("worker ranked finding bound: the context finding guard carries no item capacity")
		}
		reviewItems, ok := reviewBound.capacity(boundItems)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("worker ranked finding bound: the review finding guard carries no item capacity")
		}
		if contextItems > math.MaxInt64-reviewItems {
			return evaluatedBound{}, fmt.Errorf("worker ranked finding bound: the ordinal budget overflows")
		}
		ordinal := contextItems + reviewItems
		width := int64(len("finding:")) + int64(len(strconv.FormatInt(math.MaxInt64, 10))+1) + int64(len(":")) + int64(len(strconv.FormatInt(ordinal, 10)))
		describe := fmt.Sprintf("worker ranked finding bound: workerRankedFindingID formats finding:, at most %d int64 decimal characters, :, and the ordinal bounded by %d context + %d review findings, at %d ASCII characters", len(strconv.FormatInt(math.MaxInt64, 10))+1, contextItems, reviewItems, width)
		return workerASCIIWidth(width, describe), nil
	}}
}

// workerGeneratedResultsBound derives the widest assigned-result token the
// generated lane map declares and proves both routes through it: the reader
// still indexes the map, and the dispatch binding still byte-compares the
// packet's assigned result against the reader's answer.
func workerGeneratedResultsBound() outputBoundSource {
	const file = "internal/store/generated_agent_lanes.go"
	const routeFile = "internal/store/worker_packet_binding.go"
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		literal, count := (*ast.CompositeLit)(nil), 0
		for _, decl := range goS.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range valueSpec.Names {
					if name.Name != "generatedWorkerScopeAssignments" || i >= len(valueSpec.Values) {
						continue
					}
					elt, ok := valueSpec.Values[i].(*ast.CompositeLit)
					if !ok {
						return evaluatedBound{}, fmt.Errorf("generatedWorkerScopeAssignments is no longer a composite literal")
					}
					if _, ok := elt.Type.(*ast.MapType); !ok {
						return evaluatedBound{}, fmt.Errorf("generatedWorkerScopeAssignments is no longer a map literal")
					}
					count++
					literal = elt
				}
			}
		}
		if count == 0 {
			return evaluatedBound{}, fmt.Errorf("generatedWorkerScopeAssignments is not declared in %s", file)
		}
		if count > 1 {
			return evaluatedBound{}, fmt.Errorf("generatedWorkerScopeAssignments is declared %d times", count)
		}
		widest, entries := int64(0), 0
		for _, elt := range literal.Elts {
			keyValue, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				return evaluatedBound{}, fmt.Errorf("generatedWorkerScopeAssignments carries an unkeyed map element")
			}
			if _, ok := keyValue.Key.(*ast.BasicLit); !ok {
				return evaluatedBound{}, fmt.Errorf("generatedWorkerScopeAssignments carries a non-literal lane key")
			}
			value, ok := keyValue.Value.(*ast.BasicLit)
			if !ok || value.Kind != token.STRING {
				return evaluatedBound{}, fmt.Errorf("generatedWorkerScopeAssignments no longer holds only fixed result tokens")
			}
			text, err := strconv.Unquote(value.Value)
			if err != nil {
				return evaluatedBound{}, err
			}
			for _, r := range text {
				if r > 0x7f {
					return evaluatedBound{}, fmt.Errorf("the assigned result token %q carries a non-ASCII character", text)
				}
			}
			entries++
			if int64(len(text)) > widest {
				widest = int64(len(text))
			}
		}
		if entries == 0 {
			return evaluatedBound{}, fmt.Errorf("generatedWorkerScopeAssignments declares no result tokens")
		}
		reader, err := workerUniqueFunc(rt, routeFile, "WorkerScopeAssignedResult")
		if err != nil {
			return evaluatedBound{}, err
		}
		indexes := 0
		ast.Inspect(reader, func(n ast.Node) bool {
			if index, ok := n.(*ast.IndexExpr); ok && ledgerExprText(index.X) == "generatedWorkerScopeAssignments" {
				indexes++
			}
			return true
		})
		if indexes != 1 {
			return evaluatedBound{}, fmt.Errorf("WorkerScopeAssignedResult no longer reads generatedWorkerScopeAssignments exactly once")
		}
		validator, err := workerUniqueFunc(rt, routeFile, "validateWorkerPacketBinding")
		if err != nil {
			return evaluatedBound{}, err
		}
		if workerCountComparisons(validator, "*binding.AssignedResult != assigned") != 1 {
			return evaluatedBound{}, fmt.Errorf("validateWorkerPacketBinding no longer byte-compares the assigned result against the generated token")
		}
		describe := fmt.Sprintf("worker assigned result bound: the widest of the %d fixed generatedWorkerScopeAssignments tokens (%d ASCII characters), byte-compared at dispatch", entries, widest)
		return workerASCIIWidth(widest, describe), nil
	}}
}

// workerCallerExclusionGuardBound closes one caller-exclusions row through
// the shared packet-bounds guard: the dispatch computation owner and the
// continuity ready projection must each hand their set to
// validateWorkerJobPacketExclusions exactly once, and the guard's own
// refusing comparisons fix the count and the per-path Unicode character
// width. A rewritten guard or a lost route stops the derivation instead of
// admitting a stale bound.
func workerCallerExclusionGuardBound(measurement string, unit boundUnit) outputBoundSource {
	const (
		guardFile = "internal/store/worker_job_caller_scope.go"
		guardFunc = "validateWorkerJobPacketExclusions"
		ownerFunc = "workerJobCallerExclusionsTx"
		projFile  = "internal/store/workflow_continuity.go"
		projFunc  = "continuityReadReadyWorkerJobsTx"
	)
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		for _, route := range []struct{ file, fn string }{{guardFile, ownerFunc}, {projFile, projFunc}} {
			site, err := workerUniqueFunc(rt, route.file, route.fn)
			if err != nil {
				return evaluatedBound{}, err
			}
			if workerCountCalls(site, guardFunc) != 1 {
				return evaluatedBound{}, fmt.Errorf("worker caller exclusion bound: %s no longer routes its exclusions through %s exactly once", route.fn, guardFunc)
			}
		}
		evaluated, err := rt.evaluateSource(goInlineBound(guardFile, guardFunc, measurement, unit), census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("worker caller exclusion bound: %w", err)
		}
		describe := fmt.Sprintf("worker caller exclusion bound: %s and %s route their sets through %s exactly once (%s)", ownerFunc, projFunc, guardFunc, evaluated.describe)
		evaluated.describe = describe
		for i := range evaluated.constraints {
			evaluated.constraints[i].from = describe
		}
		return evaluated, nil
	}}
}

// ---------------------------------------------------------------------------
// Family-local inline refuse guards
// ---------------------------------------------------------------------------

// workerGuardMode is the claim a comparison sits under: workerRefuse means
// the enclosing condition being true rejects the value, workerAdmit means
// the enclosing expression being true admits it, and workerOther means the
// comparison's truth does not gate admission where it sits.
type workerGuardMode uint8

const (
	workerOther workerGuardMode = iota
	workerRefuse
	workerAdmit
)

// workerUpper is one upper-bounding comparison operand.
type workerUpper struct {
	operand ast.Expr
	strict  bool // the admitted maximum is operand-1 rather than operand
	body    *ast.BlockStmt
}

// workerRefuseScan derives one upper bound from the refusing comparisons on
// one measured len() expression inside a scanned region. It refines the
// shared inline-guard rule for the two store guard shapes this family
// cites. A refusing conjunction keeps a side's refusing claim only when
// its sibling is the measured argument's implied non-emptiness or
// non-nilness. Any other conjunction descends without a claim and fails
// measured comparisons inside.
type workerRefuseScan struct {
	measured    string
	argText     string
	bareText    string
	uppers      []workerUpper
	lower       int
	unsupported []string
}

func (w *workerRefuseScan) fail(message string) {
	w.unsupported = append(w.unsupported, message)
}

func workerIsComparison(op token.Token) bool {
	switch op {
	case token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ:
		return true
	}
	return false
}

func (w *workerRefuseScan) stmt(s ast.Stmt) {
	switch node := s.(type) {
	case *ast.BlockStmt:
		for _, inner := range node.List {
			w.stmt(inner)
		}
	case *ast.IfStmt:
		if node.Init != nil {
			w.stmt(node.Init)
		}
		before := len(w.uppers)
		w.expr(node.Cond, workerRefuse)
		for i := before; i < len(w.uppers); i++ {
			w.uppers[i].body = node.Body
		}
		w.stmt(node.Body)
		if node.Else != nil {
			w.stmt(node.Else)
		}
	case *ast.ForStmt:
		if node.Init != nil {
			w.stmt(node.Init)
		}
		if node.Cond != nil {
			w.expr(node.Cond, workerOther)
		}
		if node.Post != nil {
			w.stmt(node.Post)
		}
		w.stmt(node.Body)
	case *ast.RangeStmt:
		if node.Key != nil {
			w.expr(node.Key, workerOther)
		}
		if node.Value != nil {
			w.expr(node.Value, workerOther)
		}
		w.expr(node.X, workerOther)
		w.stmt(node.Body)
	case *ast.SwitchStmt:
		if node.Init != nil {
			w.stmt(node.Init)
		}
		if node.Tag != nil {
			w.expr(node.Tag, workerOther)
		}
		w.stmt(node.Body)
	case *ast.TypeSwitchStmt:
		if node.Init != nil {
			w.stmt(node.Init)
		}
		w.stmt(node.Assign)
		w.stmt(node.Body)
	case *ast.CaseClause:
		for _, e := range node.List {
			w.expr(e, workerOther)
		}
		for _, inner := range node.Body {
			w.stmt(inner)
		}
	case *ast.SelectStmt:
		w.stmt(node.Body)
	case *ast.CommClause:
		if node.Comm != nil {
			w.stmt(node.Comm)
		}
		for _, inner := range node.Body {
			w.stmt(inner)
		}
	case *ast.ReturnStmt:
		for _, result := range node.Results {
			w.expr(result, workerOther)
		}
	case *ast.LabeledStmt:
		w.stmt(node.Stmt)
	case *ast.GoStmt:
		w.expr(node.Call, workerOther)
	case *ast.DeferStmt:
		w.expr(node.Call, workerOther)
	case *ast.SendStmt:
		w.expr(node.Chan, workerOther)
		w.expr(node.Value, workerOther)
	case *ast.AssignStmt:
		for _, rhs := range node.Rhs {
			w.expr(rhs, workerOther)
		}
	case *ast.ExprStmt:
		w.expr(node.X, workerOther)
	case *ast.IncDecStmt:
		w.expr(node.X, workerOther)
	case *ast.DeclStmt, *ast.BranchStmt, *ast.EmptyStmt:
	default:
		w.fail(fmt.Sprintf("unhandled statement %T", s))
	}
}

func (w *workerRefuseScan) expr(e ast.Expr, mode workerGuardMode) {
	switch node := e.(type) {
	case nil:
		return
	case *ast.ParenExpr:
		w.expr(node.X, mode)
	case *ast.UnaryExpr:
		inner := mode
		if node.Op == token.NOT {
			switch inner {
			case workerRefuse:
				inner = workerAdmit
			case workerAdmit:
				inner = workerRefuse
			}
		}
		w.expr(node.X, inner)
	case *ast.BinaryExpr:
		switch node.Op {
		case token.LAND:
			w.conjunction(node, mode)
		case token.LOR:
			inner := workerOther
			if mode == workerRefuse {
				inner = workerRefuse
			}
			w.expr(node.X, inner)
			w.expr(node.Y, inner)
		case token.LSS, token.LEQ, token.GTR, token.GEQ, token.EQL, token.NEQ:
			w.comparison(node, mode)
		default:
			w.expr(node.X, workerOther)
			w.expr(node.Y, workerOther)
		}
	case *ast.CallExpr:
		w.expr(node.Fun, workerOther)
		for _, arg := range node.Args {
			w.expr(arg, workerOther)
		}
	case *ast.StarExpr:
		w.expr(node.X, workerOther)
	case *ast.SelectorExpr:
		w.expr(node.X, workerOther)
	case *ast.IndexExpr:
		w.expr(node.X, workerOther)
		w.expr(node.Index, workerOther)
	case *ast.IndexListExpr:
		w.expr(node.X, workerOther)
		for _, index := range node.Indices {
			w.expr(index, workerOther)
		}
	case *ast.SliceExpr:
		w.expr(node.X, workerOther)
		if node.Low != nil {
			w.expr(node.Low, workerOther)
		}
		if node.High != nil {
			w.expr(node.High, workerOther)
		}
		if node.Max != nil {
			w.expr(node.Max, workerOther)
		}
	case *ast.TypeAssertExpr:
		w.expr(node.X, workerOther)
	case *ast.CompositeLit:
		for _, elt := range node.Elts {
			w.expr(elt, workerOther)
		}
	case *ast.KeyValueExpr:
		w.expr(node.Key, workerOther)
		w.expr(node.Value, workerOther)
	case *ast.FuncLit:
		// Audited separately: a measured comparison inside a closure fails
		// the derivation instead of being classified in unknown context.
	default:
	}
}

func (w *workerRefuseScan) conjunction(node *ast.BinaryExpr, mode workerGuardMode) {
	switch mode {
	case workerAdmit:
		w.expr(node.X, workerAdmit)
		w.expr(node.Y, workerAdmit)
	case workerRefuse:
		// An oversized value implies the optional-value conjunct. Any
		// other conjunct can make the refusal false even above the limit.
		switch {
		case w.impliedNonEmpty(ledgerStripParens(node.X)):
			w.expr(node.Y, workerRefuse)
		case w.impliedNonEmpty(ledgerStripParens(node.Y)):
			w.expr(node.X, workerRefuse)
		default:
			w.expr(node.X, workerOther)
			w.expr(node.Y, workerOther)
		}
	default:
		w.expr(node.X, workerOther)
		w.expr(node.Y, workerOther)
	}
}

// impliedNonEmpty reports whether the side is exactly <measured argument>
// != "" or != nil, with one leading dereference on the argument allowed:
// len(x) > n with n > 0 implies x != "", and evaluating len(*p) at all
// implies p != nil.
func (w *workerRefuseScan) impliedNonEmpty(side ast.Expr) bool {
	binary, ok := side.(*ast.BinaryExpr)
	if !ok || binary.Op != token.NEQ {
		return false
	}
	other := binary.Y
	switch text := ledgerExprText(binary.X); text {
	case w.argText, w.bareText:
	default:
		if witnessed := ledgerExprText(binary.Y); witnessed == w.argText || witnessed == w.bareText {
			other = binary.X
		} else {
			return false
		}
	}
	switch witness := other.(type) {
	case *ast.Ident:
		return witness.Name == "nil"
	case *ast.BasicLit:
		if witness.Kind != token.STRING {
			return false
		}
		value, err := strconv.Unquote(witness.Value)
		return err == nil && value == ""
	default:
		return false
	}
}

func (w *workerRefuseScan) comparison(node *ast.BinaryExpr, mode workerGuardMode) {
	left, right := ledgerExprText(node.X), ledgerExprText(node.Y)
	switch {
	case left == w.measured && right == w.measured:
		w.fail("comparison measures the expression on both sides")
		return
	case left == w.measured:
	case right == w.measured:
		node = &ast.BinaryExpr{Op: ledgerFlipComparisonOp(node.Op), X: node.Y, Y: node.X}
	default:
		return
	}
	switch mode {
	case workerOther:
		w.fail(fmt.Sprintf("comparison on %s sits in a context whose truth does not gate admission", w.measured))
	case workerRefuse:
		switch node.Op {
		case token.GTR:
			w.uppers = append(w.uppers, workerUpper{operand: node.Y})
		case token.GEQ:
			w.uppers = append(w.uppers, workerUpper{operand: node.Y, strict: true})
		case token.NEQ:
			w.uppers = append(w.uppers, workerUpper{operand: node.Y})
		default: // <, <= lower-bound the admitted set; == pins nothing above
			w.lower++
		}
	case workerAdmit:
		switch node.Op {
		case token.LEQ:
			w.uppers = append(w.uppers, workerUpper{operand: node.Y})
		case token.LSS:
			w.uppers = append(w.uppers, workerUpper{operand: node.Y, strict: true})
		case token.EQL:
			w.uppers = append(w.uppers, workerUpper{operand: node.Y})
		default: // >, >= lower-bound the admitted set; != pins nothing above
			w.lower++
		}
	}
}

// audit counts every measured comparison in the region, including the
// positions the scan skipped, and fails when one escaped classification.
func (w *workerRefuseScan) audit(root ast.Node) {
	total := 0
	ast.Inspect(root, func(n ast.Node) bool {
		binary, ok := n.(*ast.BinaryExpr)
		if !ok || !workerIsComparison(binary.Op) {
			return true
		}
		if ledgerExprText(binary.X) == w.measured || ledgerExprText(binary.Y) == w.measured {
			total++
		}
		return true
	})
	if total > len(w.uppers)+w.lower {
		w.fail(fmt.Sprintf("%d comparison(s) on %s sit in a position this derivation does not support", total-len(w.uppers)-w.lower, w.measured))
	}
}

// workerInlineRefuseBound derives an optional string's refusing len guard.
// The selected body must stop admission, and the inferred byte measurement
// survives evaluation rather than relying on the row's declared unit.
func workerInlineRefuseBound(file, function, measurement string) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		fns := ledgerFuncDeclsNamed(goS.file, function)
		if len(fns) == 0 {
			return evaluatedBound{}, fmt.Errorf("worker guard: function %s is not declared in %s", function, file)
		}
		if len(fns) > 1 || fns[0].Body == nil {
			return evaluatedBound{}, fmt.Errorf("worker guard: function %s is ambiguous or bodyless in %s", function, file)
		}
		fn := fns[0]
		measured, err := parser.ParseExpr(measurement)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("worker guard measurement %q does not parse: %w", measurement, err)
		}
		call, ok := measured.(*ast.CallExpr)
		if !ok || !ledgerCalleeMatches(call, "len") || len(call.Args) != 1 {
			return evaluatedBound{}, fmt.Errorf("worker guard measurement %q is not one single-argument len() call", measurement)
		}
		inferred := ledgerScanMeasurement(measured)
		if err := reconcileGoUnit(boundBytes, inferred); err != nil {
			return evaluatedBound{}, fmt.Errorf("worker guard %s:%s: %w", file, function, err)
		}
		argText := ledgerExprText(call.Args[0])
		root := fn.Body
		scan := &workerRefuseScan{
			measured: ledgerExprText(measured),
			argText:  argText,
			bareText: strings.TrimPrefix(argText, "*"),
		}
		scan.stmt(root)
		scan.audit(root)
		if len(scan.unsupported) > 0 {
			return evaluatedBound{}, fmt.Errorf("worker guard %s:%s: %s", file, function, scan.unsupported[0])
		}
		switch {
		case len(scan.uppers) == 0:
			return evaluatedBound{}, fmt.Errorf("worker guard %s:%s: no comparison upper-bounds %s", file, function, measurement)
		case len(scan.uppers) > 1:
			return evaluatedBound{}, fmt.Errorf("worker guard %s:%s: %d comparisons upper-bound %s; the guard is ambiguous", file, function, len(scan.uppers), measurement)
		}
		if scan.uppers[0].body == nil {
			return evaluatedBound{}, fmt.Errorf("worker guard %s:%s: the upper comparison has no rejecting body", file, function)
		}
		value, err := rt.evalNumeric(goS, fn, scan.uppers[0].operand)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("worker guard %s:%s: %w", file, function, err)
		}
		if scan.uppers[0].strict {
			if value <= 0 {
				return evaluatedBound{}, fmt.Errorf("worker guard %s:%s: a strict comparison against %d admits no value", file, function, value)
			}
			value--
		}
		if err := ledgerGuardBodyEffect(scan.uppers[0].body, measured, value, func(expr ast.Expr) (int64, error) {
			return rt.evalNumeric(goS, fn, expr)
		}); err != nil {
			return evaluatedBound{}, fmt.Errorf("worker guard %s:%s: %w", file, function, err)
		}
		describe := fmt.Sprintf("worker inline refuse guard %s:%s bounds %s at %d bytes", file, function, measurement, value)
		return evaluatedBound{
			constraints: []boundConstraint{{unit: boundBytes, value: value, from: describe}},
			kind:        "derived",
			describe:    describe,
			inferred:    inferred,
		}, nil
	}}
}

// ---------------------------------------------------------------------------
// Packet work record task
// ---------------------------------------------------------------------------

// workerRecordedTaskBound cites both intent folds, which also guard generic
// event append and replay independently of the agent and bootstrap inputs.
func workerRecordedTaskBound() outputBoundSource {
	const file = "internal/store/lifecycle.go"
	return maxBound(
		workerInlineRefuseBound(file, "foldWorkCreated", "len(payload.Task)"),
		workerInlineRefuseBound(file, "foldWorkIntentRevised", "len(*payload.Task)"),
	)
}
