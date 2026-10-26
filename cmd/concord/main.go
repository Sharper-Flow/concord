package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/predecessor"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run handles the deliberately small bootstrap CLI surface.
func run(args []string, out, errOut io.Writer) int {
	return runWithInput(args, os.Stdin, out, errOut)
}

func runWithInput(args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		_, _ = fmt.Fprintln(out, version.Value)
		return 0
	}
	// The JSON descriptor is the same bootstrap surface as plain --version:
	// it answers before any stdin read, store open, or lease write, so the
	// installer can learn a staged core's capability without side effects
	// (CON-807). Plain --version output stays exactly the version string.
	if len(args) == 2 && args[0] == "--version" && args[1] == "--json" {
		return writeCoreDescriptor(out, errOut)
	}
	if len(args) == 1 && isHelpFlag(args[0]) {
		writeUsage(out)
		return 0
	}
	// Command and group help routes before any stdin read or store open:
	// help output must never consume the JSON bytes a command would read.
	if code, handled := runCommandHelpRoute(args, out, errOut); handled {
		return code
	}
	if len(args) == 0 {
		writeUsage(errOut)
		return 2
	}
	// Continuity block is a read-only transport for the adapter's per-turn
	// hook. It must run before project and JSON routing so it does not
	// consume stdin.
	if len(args) > 0 && args[0] == "continuity-block" {
		return runContinuityBlockCommand(args[1:], out, errOut)
	}
	// The release verbs route around the store open: host-lease and
	// host-leases touch only the lease directory, and upgrade must be the one
	// command that applies a pending breaking migration an open refuses
	// (CD-0111 D3).
	if len(args) > 0 && args[0] == "host-lease" {
		return runHostLeaseCommand(args[1:], in, out, errOut)
	}
	if len(args) > 0 && args[0] == "host-leases" {
		return runHostLeasesCommand(args[1:], in, out, errOut)
	}
	if len(args) > 0 && args[0] == "upgrade" {
		return runUpgradeCommand(args[1:], in, out, errOut)
	}
	// Repair runs from an ordinary shell during an outage (#912): it routes
	// around the store open so a damaged deployment still reaches the
	// verified installer, and it opens the store only for the pre-repair
	// database snapshot.
	if len(args) > 0 && args[0] == "repair" {
		return runRepairCommand(args[1:], in, out, errOut)
	}
	// ci-wait is store-free (CD-0160): it queries GitHub through gh and a
	// state file, never Concord authority, so it routes around the database
	// open like the release verbs and consumes its JSON body directly.
	if len(args) > 0 && args[0] == "ci-wait" {
		return runStoreFreeJSONCommand("ci-wait", args[1:], in, out, errOut, runCiWait)
	}
	command, commandArgs, ok := routeCommand(args)
	if ok {
		return runJSONCommand(command, commandArgs, in, out, errOut)
	}

	writeDiagnostic(errOut, fmt.Sprintf("concord: unsupported arguments: %s", strings.Join(args, " ")))
	writeUsage(errOut)
	return 2
}

type commandSpec struct {
	Canonical      string
	TwoWord        string
	RequiredFields []commandField
	Optional       string
	Enums          string
}

type commandField struct {
	Name   string
	Nested []string
}

func field(name string) commandField { return commandField{Name: name} }
func nestedField(name string, nested ...string) commandField {
	return commandField{Name: name, Nested: nested}
}
func requiredFields(fields ...commandField) []commandField { return fields }
func formatRequiredFields(fields []commandField) string {
	formatted := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field.Nested) == 0 {
			formatted = append(formatted, field.Name)
			continue
		}
		formatted = append(formatted, field.Name+"{"+strings.Join(field.Nested, ", ")+"}")
	}
	return strings.Join(formatted, ", ")
}

func invokeOperationEnums() string {
	operations := make([]string, 0, len(agent.ContractOperations))
	for _, operation := range agent.ContractOperations {
		operations = append(operations, operation.ID)
	}
	return "tool.operation: " + strings.Join(operations, " | ")
}

// commandSpecs is the single source of truth for operator tokenization and
// help. The hyphenated and two-word forms are deliberate, exact forms; no
// other aliases are accepted.
// Operator setup commands predate a standalone JSON schema, so their field
// lists are kept beside the command boundary and exercised by the README
// bootstrap test. Agent invoke fields remain owned by their generated
// transport contracts.
var commandSpecs = []commandSpec{
	{Canonical: "invoke", RequiredFields: requiredFields(nestedField("call_envelope", "schema_version", "request_id", "client_ref", "principal_ref", "session_ref", "agent_ref", "directory", "worktree", "ambient_project_id", "scope_version", "manifest_digest"), field("tool"), field("operation"), field("input")), Optional: "call_envelope.selected_product_id, call_envelope.host_assertion_digest, call_envelope.host_approval_assertion; pre-dispatch decode refusals exit 64; worktree_verify input uses command OR oracle (phase prepare | execute), never both; worktree_inspect mode oracle_output uses run_ref, stream stdout | stderr, offset, length (1-16384 raw bytes), never path or idempotency_key; exact fields and exclusions follow the generated agent contracts", Enums: invokeOperationEnums()},
	{Canonical: "worker-dispatch", RequiredFields: requiredFields(field("event_id"), field("work_id"), field("attempt_id"), field("lane_id"), field("lane_digest"), field("packet_schema_version"), field("report_schema_version"), field("packet_digest")), Optional: "readback_model (host-reported executing model); terminal ('failed') with terminal_failure_kind and terminal_detail for an attempt born failed, such as a lost or ambiguous readback; host_provenance.digest (sha256), host_provenance.sources[] (kind: agent_definition | agents_md | instruction_file | unenumerated; path; sha256) — required for v3 evidence (CD-0034); worker_job (job_id, revision, digest) — required on a job-bound attempt (CD-0205); failure_attribution (typed producer observation: observer, phase, cause, basis, sources[], diagnosis) — born-failed terminal records only, and the assertion must be version 2 signing its canonical observation digest", Enums: "none"},
	{Canonical: "worker-complete", RequiredFields: requiredFields(field("event_id"), field("work_id"), field("attempt_id"), field("readback_model"), field("report_schema_version"), field("evidence_origin")), Optional: "worker_directory — required on a new completion, base_comparison, review, worker_job — required on a job-bound attempt, direct_caller_repairs[] (at most 8), evidence[] — required when evidence_origin is reported, context_findings[] (0-16 typed claims, 16 KiB)", Enums: "evidence_origin: reported | legacy_unavailable; evidence[].obligation: bounded_findings | commands | contract_findings | exit_codes | failure_classification | files_touched | severity | source_citations | uncertainties | unresolved_issues | verification_commands | visual_artifacts; reported evidence must discharge every declared obligation; base_comparison.checks[]: pass | fail | not_run; review.disposition: continue | correction_required | revision_required | ready_for_final_proof, findings severity P0-P3, confidence low | medium | high; blocking dispositions remain valid reports; a lane requiring the typed review block refuses a completion without it and a free-text severity entry beside it; the block discharges severity; context_findings[].kind: observation | inference | hypothesis | rejected_approach | open_question | contradiction | direction"},
	{Canonical: "worker-fail", RequiredFields: requiredFields(field("event_id"), field("work_id"), field("attempt_id"), field("readback_model"), field("failure_kind"), field("detail")), Optional: "context_findings[] (as worker-complete; worker_error only); failure_attribution (typed producer observation: observer, phase, cause, basis, sources[], diagnosis) whose canonical observation digest the assertion must sign at version 2", Enums: "failure_kind: fallback_blocked | worker_error | invalid_report | abandoned"},
	{Canonical: "worker-abandon", RequiredFields: requiredFields(field("event_id"), field("work_id"), field("attempt_id"), field("detail")), Optional: "failure_attribution (typed producer observation: observer, phase, cause, basis, sources[], diagnosis) whose canonical observation digest the assertion must sign at version 2", Enums: "the host signs a worker-fail assertion with failure_kind abandoned; readback_model is derived from the dispatched attempt"},
	{Canonical: "worker-read", RequiredFields: requiredFields(field("work_id"), field("attempt_id"), field("worktree_identity"), field("packet"), field("ordinal"), field("offset"), field("length")), Optional: "none", Enums: "read-only host capability; original authorized packet only; ordinal and offset nonnegative; length 1-4096 raw bytes; data is base64; no path, ref, command, or workflow mutation"},
	{Canonical: "worker-diff", RequiredFields: requiredFields(field("work_id"), field("attempt_id"), field("worktree_identity"), field("packet"), field("offset"), field("length")), Optional: "none", Enums: "read-only inspect_diff capability; original live review packet and pinned job scope only; first editing subject under the active contract to held review subject; offset nonnegative; length 1-4096 raw bytes; no caller-selected path or ref"},
	{Canonical: "worker-control", RequiredFields: requiredFields(field("work_id"), field("project_id"), field("attempt_id"), field("worktree_identity"), field("packet"), field("control_id"), field("assertion")), Optional: "none", Enums: "private host ingress; signed actual child execution policy and original packet; recorded native control only; no caller-authored argv, cwd, runner, actor, or workflow mutation"},
	{Canonical: "client-register", TwoWord: "client register", RequiredFields: requiredFields(field("client_ref"), field("key_id"), field("principal_ref"), field("public_key"), field("capabilities"), field("product_scope"), field("project_scope"), field("agent_scope")), Optional: "none", Enums: "capabilities: product_read | work_define | work_transition | work_relate | work_compact | cross_scope | research | worker_evidence | worker_dispatch; public_key: base64 Ed25519; agent_scope: the agent references this client may present"},
	{Canonical: "client-policy-update", TwoWord: "client policy-update", RequiredFields: requiredFields(field("client_ref"), field("principal_ref"), field("capabilities"), field("product_scope"), field("project_scope"), field("agent_scope")), Optional: "none", Enums: "capabilities: product_read | work_define | work_transition | work_relate | work_compact | cross_scope | research | worker_evidence | worker_dispatch; agent_scope: the agent references this client may present"},
	{Canonical: "client-policy-expand", TwoWord: "client policy-expand", RequiredFields: requiredFields(field("client_ref")), Optional: "capabilities, product_scope, project_scope, agent_scope — additive union; every existing grant and the stored principal are preserved (CD-0097 D6)", Enums: "capabilities: product_read | work_define | work_transition | work_relate | work_compact | cross_scope | research | worker_evidence | worker_dispatch"},
	{Canonical: "client-key-rotate", TwoWord: "client key-rotate", RequiredFields: requiredFields(field("client_ref"), field("key_id"), field("public_key")), Optional: "none", Enums: "public_key: base64 Ed25519"},
	{Canonical: "client-revoke", TwoWord: "client revoke", RequiredFields: requiredFields(field("client_ref")), Optional: "none", Enums: "none"},
	{Canonical: "client-show", TwoWord: "client show", RequiredFields: requiredFields(field("client_ref")), Optional: "none", Enums: "read-only; returns registered policy, not session authorization; no key material"},
	{Canonical: "product-create", TwoWord: "product create", RequiredFields: requiredFields(field("product_id"), field("display_name"), field("stage_maturity"), field("stage_audience_commitment"), field("project_id"), field("project_display_name"), field("role")), Optional: "reason", Enums: "stage_maturity: prototype | alpha | beta | production | deprecated; stage_audience_commitment: operator_only | limited | public; role: primary | secondary"},
	{Canonical: "resource-create", TwoWord: "resource create", RequiredFields: requiredFields(field("event_id"), field("resource_id"), field("product_id"), field("display_name"), field("class"), field("kind"), field("purpose"), field("stage_maturity"), field("stage_audience_commitment"), field("environments"), field("expected_product_version")), Optional: "locator_absence_reason, metadata_schema_version, metadata, owner_purpose, owner_environments", Enums: "stage_maturity: prototype | alpha | beta | production | deprecated; stage_audience_commitment: operator_only | limited | public"},
	{Canonical: "resource-share", TwoWord: "resource share", RequiredFields: requiredFields(field("event_id"), field("resource_id"), field("product_id"), field("expected_resource_version")), Optional: "purpose, environments", Enums: "none"},
	{Canonical: "domain-project-attachments-replace", TwoWord: "domain project-attachments-replace", RequiredFields: requiredFields(field("event_id"), field("product_id"), field("domain_id"), field("expected_version"), field("attachments")), Optional: "attachments (replaces the full edge set)", Enums: "attachments[].role: primary | secondary"},
	{Canonical: "domain-resource-attachments-replace", TwoWord: "domain resource-attachments-replace", RequiredFields: requiredFields(field("event_id"), field("product_id"), field("domain_id"), field("expected_version"), field("attachments")), Optional: "attachments (replaces the full edge set)", Enums: "none"},
	{Canonical: "project-create", TwoWord: "project create", RequiredFields: requiredFields(field("project_id"), field("display_name"), field("product_id"), field("role"), field("expected_product_version")), Optional: "reason", Enums: "role: primary | secondary"},
	{Canonical: "product-project-add", TwoWord: "product project-add", RequiredFields: requiredFields(field("product_id"), field("project_id"), field("role"), field("expected_version")), Optional: "reason", Enums: "role: primary | secondary"},
	{Canonical: "product-stage-update", TwoWord: "product stage-update", RequiredFields: requiredFields(field("product_id"), field("stage_maturity"), field("stage_audience_commitment"), field("expected_version")), Optional: "reason (cite the CD-0091 rung manifest when raising maturity)", Enums: "stage_maturity: prototype | alpha | beta | production | deprecated; stage_audience_commitment: operator_only | limited | public"},
	{Canonical: "product-knowledge-home-designate", TwoWord: "product knowledge-home-designate", RequiredFields: requiredFields(field("product_id"), field("project_id"), field("locator_id"), field("expected_version")), Optional: "reason", Enums: "locator_id: a canonical_path locator of the member Project"},
	{Canonical: "product-knowledge-home-clear", TwoWord: "product knowledge-home-clear", RequiredFields: requiredFields(field("product_id"), field("expected_version")), Optional: "reason", Enums: "none"},
	{Canonical: "domain-shape-check", TwoWord: "domain shape-check", RequiredFields: requiredFields(field("product_id")), Optional: "checkout (clean exact pinned subject worktree of the designated home; defaults to its registered path)", Enums: "explicit operator command; strict ratchet; missing checks remain unverified; JSON report <=32768 bytes; diagnostics <=4096 bytes; failures exit nonzero"},
	{Canonical: "product-knowledge-source-register", TwoWord: "product knowledge-source-register", RequiredFields: requiredFields(field("product_id"), field("project_id"), field("locator_id"), field("expected_version")), Optional: "reason", Enums: "locator_id: a canonical_path locator of the member Project, not the designated home"},
	{Canonical: "product-knowledge-source-remove", TwoWord: "product knowledge-source-remove", RequiredFields: requiredFields(field("product_id"), field("project_id"), field("locator_id"), field("expected_version")), Optional: "reason", Enums: "locator_id: a registered knowledge source of the Product"},
	{Canonical: "project-locator-add", TwoWord: "project locator-add", RequiredFields: requiredFields(field("project_id"), field("locator_id"), field("kind"), field("value"), field("expected_version")), Optional: "none", Enums: "kind: canonical_path | git_remote"},
	{Canonical: "project-locator-update", TwoWord: "project locator-update", RequiredFields: requiredFields(field("project_id"), field("locator_id"), field("kind"), field("value"), field("expected_version")), Optional: "none", Enums: "kind: canonical_path | git_remote"},
	{Canonical: "project-locator-remove", TwoWord: "project locator-remove", RequiredFields: requiredFields(field("project_id"), field("locator_id"), field("expected_version")), Optional: "none", Enums: "none"},
	{Canonical: "cd-reservations", TwoWord: "cd reservations", RequiredFields: requiredFields(field("directory")), Optional: "none", Enums: "prints the law-addition reservations (law_id, owner_work_id) of the Product that owns the calling checkout, with checkout_work_id naming the work that owns the checkout; read-only"},
	{Canonical: "backup", RequiredFields: requiredFields(field("destination")), Optional: "none", Enums: "destination: absolute clean path that does not yet exist; a manifest is written beside it"},
	{Canonical: "worktree-locate", RequiredFields: requiredFields(field("project_id"), field("work_id")), Optional: "ref (a rev-syntax ref; defaults to HEAD, the default branch under the trunk-stays-on-default rule)", Enums: "none"},
	{Canonical: "claim-landing", RequiredFields: requiredFields(field("work_id"), field("session_ref"), field("landed_directory")), Optional: "none", Enums: "none"},
	{Canonical: "vacate-landing", RequiredFields: requiredFields(field("work_id"), field("session_ref"), field("landed_directory")), Optional: "none", Enums: "none"},
	{Canonical: "work-bootstrap", RequiredFields: requiredFields(field("product_id"), field("project_id"), field("title"), field("value_statement"), field("kind"), field("task"), field("idempotency_key")), Optional: "priority, urgency, tags, workflow_type_ref, external_ref, governing_requirements, ref (default branch resolved after identity), defect_intake (required for bug), size_inputs (paths and/or behavior, security, user_visible, blast_radius, estimated_production_lines; the model decides the size), host_pid (required with session_ref); handler refusals exit 2; required-field refusals exit 64", Enums: "kind: task | bug | decision | research | other; urgency: standard | expedite"},
	{Canonical: "work-resume", RequiredFields: requiredFields(field("product_id"), field("project_id")), Optional: "exactly one of work_id or issue_key (unique locally recorded Linear key); session_ref; handler refusals exit 2; required-field refusals exit 64", Enums: "none"},
	{Canonical: "receipt", RequiredFields: requiredFields(field("work_id")), Optional: "none", Enums: "prints the product-owned closure receipt markdown (CD-0169) for a completed work item; empty output when the item is not completed"},
	{Canonical: "work-shelve", RequiredFields: requiredFields(field("operation_id"), field("idempotency_key"), field("work_id"), field("expected_version"), field("handoff")), Optional: "product_id, linear, actor, safety evidence", Enums: "reason is fixed to shelved; no sixth lifecycle state"},
	{Canonical: "work-cancel", RequiredFields: requiredFields(field("operation_id"), field("idempotency_key"), field("work_id"), field("expected_version"), field("handoff")), Optional: "product_id, linear, actor, safety evidence", Enums: "reason is fixed to cancelled; removal is not archival"},
	{Canonical: "ci-wait", RequiredFields: requiredFields(field("selector"), field("repo")), Optional: "mode (pr checks|merge), head_sha (pr), time_seconds_max, state_file", Enums: "selector.kind: pr|sha|run; mode: checks|merge"},
	{Canonical: "session-prepare", RequiredFields: requiredFields(field("product_id"), field("work_id"), field("agent")), Optional: "task (max 8192 bytes; none on resume); agent is the active agent; handler refusals exit 2; required-field refusals exit 64", Enums: "none"},
	{Canonical: "project-resolve", TwoWord: "project resolve", RequiredFields: requiredFields(field("directory")), Optional: "worktree (defaults to directory)", Enums: "none"},
	{Canonical: "restore", RequiredFields: requiredFields(field("source"), field("destination")), Optional: "none", Enums: "source: existing verified backup snapshot path; destination: absolute clean path that does not yet exist and is not the live database"},
	{Canonical: "predecessor-inventory", TwoWord: "predecessor inventory", RequiredFields: requiredFields(field("snapshot_path")), Optional: "none", Enums: "snapshot_path: absolute path to a predecessor snapshot file (CD-0097)"},
	{Canonical: "predecessor-import", TwoWord: "predecessor import", RequiredFields: requiredFields(field("snapshot_path"), nestedField("product", "product_id", "display_name", "stage_maturity", "stage_audience_commitment"), field("projects"), field("select_change_ids")), Optional: "dry_run, surfaces", Enums: "stage_maturity: prototype | alpha | beta | production | deprecated; stage_audience_commitment: operator_only | limited | public; projects[].role: primary | secondary; select_change_ids: change ids the snapshot enumerates as active and that belong to a declared snapshot_project_id, or already-imported ids that turned terminal or left the active set since the previous harvest; surfaces: specifications | active_work | terminal_history | wisdom | reflections; only active_work imports, a surface outside this set refuses before import (CD-0097)"},
	{Canonical: "host-lease", RequiredFields: requiredFields(field("pid")), Optional: "directory, worktree: session location, named by a breaking-migration refusal; advisory, never interpreted", Enums: "pid: the host process that holds this release; the core writes the lease under the data root"},
	{Canonical: "host-leases", RequiredFields: requiredFields(), Optional: "none", Enums: "prints the live host leases and prunes stale ones (CD-0111 D2)"},
	{Canonical: "upgrade", RequiredFields: requiredFields(), Optional: "plan: read-only readiness report; confirm_sessions_stopped: no session runs on an unfenceable release tree; cancel_cutover_pinned_work: cancel every nonterminal work item selected by the authoring-cutover gate after a verified snapshot inside the maintenance boundary (CD-0218 D5)", Enums: "applies pending migrations; a breaking step runs only inside the maintenance boundary, which excludes session admission until activation and refuses under a live older session (CD-0111 D3)"},
}

// matchCommandSpec resolves the leading tokens against commandSpecs' canonical
// and two-word forms. It returns the matched spec and the arguments left after
// the command form.
func matchCommandSpec(args []string) (*commandSpec, []string, bool) {
	if len(args) == 0 {
		return nil, nil, false
	}
	for i := range commandSpecs {
		spec := &commandSpecs[i]
		if args[0] == spec.Canonical {
			return spec, args[1:], true
		}
		if len(args) >= 2 && args[0]+" "+args[1] == spec.TwoWord {
			return spec, args[2:], true
		}
	}
	return nil, nil, false
}

func routeCommand(args []string) (string, []string, bool) {
	spec, rest, ok := matchCommandSpec(args)
	if !ok {
		return "", nil, false
	}
	return spec.Canonical, rest, true
}

// verbUsages lists the verbs that route before the JSON-stdin command table,
// in top-level usage order, with their usage lines. A verb that commandSpecs
// also declares answers --help with its command section; the others answer
// with these lines.
var verbUsages = []struct {
	Verb  string
	Lines []string
}{
	{"continuity-block", []string{"continuity-block <directory>   # read-only continuity packet for the session directory"}},
	{"host-lease", []string{"host-lease < JSON stdin      # record this host session's release lease (adapter-invoked)"}},
	{"host-leases", []string{"host-leases                  # print live release leases; prunes stale ones"}},
	{"upgrade", []string{"upgrade < JSON stdin      # apply pending migrations; {\"plan\":true} reports readiness"}},
	{"repair", []string{"repair < JSON stdin          # verify assets, back up the database, repair the installed release (#912)"}},
	{"ci-wait", []string{"ci-wait < JSON stdin         # one bounded slice of a GitHub CI wait (CD-0160)"}},
}

// isHelpFlag reports whether an argument asks for help; -h is an alias of
// --help on every help route.
func isHelpFlag(arg string) bool { return arg == "--help" || arg == "-h" }

// writeVerbUsage writes one verb's usage lines and reports whether the verb
// has any.
func writeVerbUsage(out io.Writer, verb string) bool {
	for _, usage := range verbUsages {
		if usage.Verb == verb {
			for _, line := range usage.Lines {
				_, _ = fmt.Fprintln(out, "  concord "+line)
			}
			return true
		}
	}
	return false
}

func writeUsage(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Usage:")
	_, _ = fmt.Fprintln(out, "  concord --help")
	_, _ = fmt.Fprintln(out, "  concord --version")
	for _, usage := range verbUsages {
		writeVerbUsage(out, usage.Verb)
	}
	_, _ = fmt.Fprintln(out, "")
	_, _ = fmt.Fprintln(out, "Required-field refusals exit 64 before dispatch.")
	_, _ = fmt.Fprintln(out, "Commands read one strict JSON object from stdin:")
	for _, spec := range commandSpecs {
		writeCommandSection(out, spec)
	}
}

// writeCommandSection writes one command's usage lines: both accepted forms,
// then the required, optional, and accepted-value lines commandSpecs declares.
func writeCommandSection(out io.Writer, spec commandSpec) {
	_, _ = fmt.Fprintf(out, "  concord %s < JSON stdin\n", spec.Canonical)
	if spec.TwoWord != "" {
		_, _ = fmt.Fprintf(out, "  concord %s < JSON stdin\n", spec.TwoWord)
	}
	_, _ = fmt.Fprintf(out, "    required: %s\n", formatRequiredFields(spec.RequiredFields))
	if spec.Optional != "" && spec.Optional != "none" {
		_, _ = fmt.Fprintf(out, "    optional: %s\n", spec.Optional)
	}
	if spec.Enums != "" && spec.Enums != "none" {
		_, _ = fmt.Fprintf(out, "    accepted values: %s\n", spec.Enums)
	}
}

// writeCommandUsageSection writes the usage section of one canonical command
// name. A name commandSpecs does not declare has no section and writes
// nothing, so a verb outside commandSpecs keeps its own diagnostic alone.
func writeCommandUsageSection(out io.Writer, canonical string) {
	for i := range commandSpecs {
		if commandSpecs[i].Canonical == canonical {
			writeCommandSection(out, commandSpecs[i])
			return
		}
	}
}

// commandGroups returns the bare words that name a command family rather than
// one command: the first word of every two-word form in commandSpecs, in
// first-appearance order.
func commandGroups() []string {
	var groups []string
	for _, spec := range commandSpecs {
		group, _, found := strings.Cut(spec.TwoWord, " ")
		if found && !slices.Contains(groups, group) {
			groups = append(groups, group)
		}
	}
	return groups
}

func groupWord(word string) (string, bool) {
	if slices.Contains(commandGroups(), word) {
		return word, true
	}
	return "", false
}

// writeGroupSections writes the usage sections of every command whose
// two-word form starts with the group word, in commandSpecs order.
func writeGroupSections(out io.Writer, group string) {
	prefix := group + " "
	for _, spec := range commandSpecs {
		if strings.HasPrefix(spec.TwoWord, prefix) {
			writeCommandSection(out, spec)
		}
	}
}

// runCommandHelpRoute resolves '<command> --help', '<verb> --help', and the
// group words before any stdin read or store open; -h is the same flag.
// '<command> --help' prints one section and '<verb> --help' its usage lines,
// both to stdout with exit 0. A bare group word, or a group word followed by
// a word that names none of its commands, is a usage error that names the
// group and lists its commands on stderr with exit 2; '<group> --help' prints
// the same sections to stdout. It reports whether it handled the arguments,
// with the exit code to return.
func runCommandHelpRoute(args []string, out, errOut io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	if spec, rest, matched := matchCommandSpec(args); matched {
		if len(rest) == 1 && isHelpFlag(rest[0]) {
			writeCommandSection(out, *spec)
			return 0, true
		}
		return 0, false
	}
	if len(args) == 2 && isHelpFlag(args[1]) && writeVerbUsage(out, args[0]) {
		return 0, true
	}
	group, ok := groupWord(args[0])
	if !ok {
		return 0, false
	}
	switch {
	case len(args) == 1:
		writeDiagnostic(errOut, fmt.Sprintf("concord: %s names a command group, not a command; use 'concord %s --help' to list its commands", group, group))
		writeGroupSections(errOut, group)
		return 2, true
	case len(args) == 2 && isHelpFlag(args[1]):
		writeGroupSections(out, group)
		return 0, true
	case !isHelpFlag(args[1]):
		writeDiagnostic(errOut, fmt.Sprintf("concord: %s has no command %s; use 'concord %s --help' to list its commands", group, args[1], group))
		writeGroupSections(errOut, group)
		return 2, true
	}
	return 0, false
}

const dbOverrideEnv = "CONCORD_DB_PATH"

// inputRefusalExit is the CLI transport signal for validation that precedes
// dispatch. Store, handler, and output failures must not use this exit code.
const inputRefusalExit = 64

// workerPacketDigestPattern bounds the dispatch evidence's packet_digest to
// the sha256:hex shape the core's canonicalJSON pipeline produces. The CLI
// enforces it at the worker-dispatch boundary; the store gate enforces the
// same value against the digest the dispatch_worker completion recorded.
var workerPacketDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// runStoreFreeJSONCommand reads and validates a JSON command body for a verb
// that never opens the store, then hands the raw bytes to its handler. It
// mirrors runJSONCommand's input bounds and required-field gate without the
// store open.
func runStoreFreeJSONCommand(name string, args []string, in io.Reader, out, errOut io.Writer, handler func([]byte, io.Writer, io.Writer) int) int {
	if len(args) != 0 {
		writeDiagnostic(errOut, fmt.Sprintf("concord: unsupported arguments: %s", strings.Join(append([]string{name}, args...), " ")))
		writeCommandUsageSection(errOut, name)
		return 2
	}
	inputLimit := int64(agent.MaxRequestBytes)
	raw, err := io.ReadAll(io.LimitReader(in, inputLimit+1))
	if err != nil || int64(len(raw)) > inputLimit {
		writeDiagnostic(errOut, fmt.Sprintf("input exceeds %d bytes", inputLimit))
		return 1
	}
	if err := validateRequiredCommandFields(name, raw); err != nil {
		writeOperatorDiagnostic(errOut, name, diagnosticText(err))
		return inputRefusalExit
	}
	return handler(raw, out, errOut)
}

func runJSONCommand(command string, args []string, in io.Reader, out, errOut io.Writer) (exitCode int) {
	if len(args) != 0 {
		writeDiagnostic(errOut, fmt.Sprintf("concord: unsupported arguments: %s", strings.Join(append([]string{command}, args...), " ")))
		writeCommandUsageSection(errOut, command)
		return 2
	}
	inputLimit := int64(agent.MaxRequestBytes)
	raw, err := io.ReadAll(io.LimitReader(in, inputLimit+1))
	if err != nil || int64(len(raw)) > inputLimit {
		writeDiagnostic(errOut, fmt.Sprintf("input exceeds %d bytes", inputLimit))
		return 1
	}
	if err := validateRequiredCommandFields(command, raw); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return inputRefusalExit
	}
	// Predecessor inventory reads only the operator-supplied snapshot file and
	// writes nothing to the Concord store, so it routes around the database
	// open before any authority is touched.
	if command == "predecessor-inventory" {
		return runPredecessorInventory(raw, out, errOut)
	}
	path, err := databasePath()
	if err != nil {
		writeDiagnostic(errOut, diagnosticText(err))
		return 1
	}
	// Restore must remain available when the live store refuses to open.
	if command == "restore" {
		var request struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		if request.Destination == path {
			writeOperatorDiagnostic(errOut, command, "restore destination equals the live database path; restore to a new path and swap")
			return 1
		}
		release, err := lockManagedSnapshotRestore(request.Source)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		defer release()
		report, err := store.RestoreBackup(context.Background(), request.Source, request.Destination)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeJSON(out, report, errOut)
	}
	s, err := openStoreForCommand(context.Background(), path)
	if err != nil {
		writeDiagnostic(errOut, diagnosticText(err))
		return 1
	}
	defer func() {
		if err := s.Close(); err != nil && exitCode == 0 {
			writeDiagnostic(errOut, "concord: cannot close store: "+diagnosticText(err))
			exitCode = 1
		}
	}()
	clock := func() time.Time { return time.Now().UTC() }
	s.Clock = clock
	service := agent.NewService(s)
	service.Now = clock
	service.ProjectHostProber = func(ctx context.Context, directory, worktree string) (store.ResolvedProjectHost, error) {
		return store.ResolveProjectHost(ctx, directory, worktree, store.ExecGitRunner{})
	}
	service.ProjectHostMatcher = func(ctx context.Context, tx *store.Transaction, host store.ResolvedProjectHost) (store.ProjectResolution, error) {
		if tx == nil {
			return s.MatchResolvedProjectHost(ctx, host)
		}
		return store.MatchResolvedProjectHostTx(ctx, tx, host)
	}
	switch command {
	case "invoke":
		return runInvoke(raw, s, service, out, errOut)
	case "worker-dispatch", "worker-complete", "worker-fail", "worker-abandon":
		return runWorkerCommand(command, raw, s, service, clock, out, errOut)
	case "worker-read":
		return runWorkerRead(raw, s.ReadWorkerReading, out, errOut)
	case "worker-diff":
		return runWorkerDiff(raw, s.ReadWorkerDiff, out, errOut)
	case "worker-control":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runWorkerControl(ctx, raw, service.ExecuteWorkerControl, out, errOut)
	case "work-bootstrap":
		return runWorkBootstrap(raw, s, out, errOut)
	case "work-resume":
		return runWorkResume(raw, s, out, errOut)
	case "claim-landing":
		return runWorktreeClaimLanding(raw, s, out, errOut)
	case "vacate-landing":
		return runSessionVacateLanding(raw, s, out, errOut)
	case "work-shelve", "work-cancel":
		var request store.WorkRemovalRequest
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		if command == "work-shelve" {
			request.Reason = "shelved"
		} else {
			request.Reason = "cancelled"
		}
		receipt, err := s.RemoveWork(context.Background(), request)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeJSON(out, receipt, errOut)
	case "session-prepare":
		return runSessionPrepare(raw, s, out, errOut, hostLaneAgentIdentity, hostSessionPrepareRegistry, hostOrchestratorIdentity, DeriveSessionBoot)
	case "receipt":
		return runReceipt(raw, s, out, errOut)
	default:
		return runInternal(command, raw, service, s, clock, out, errOut)
	}
}

type workerDispatchRequest struct {
	AuthorizedPacket    json.RawMessage `json:"authorized_packet,omitempty"`
	EventID             string          `json:"event_id"`
	WorkID              string          `json:"work_id"`
	AttemptID           string          `json:"attempt_id"`
	LaneID              string          `json:"lane_id"`
	LaneDigest          string          `json:"lane_digest"`
	PacketSchemaVersion string          `json:"packet_schema_version"`
	ReportSchemaVersion string          `json:"report_schema_version"`
	// PacketDigest is the canonical lane-packet digest the dispatch_worker
	// authorization recorded on its completion. CD-0067 D6 makes the
	// adapter quote this value on its signed assertion; the store gate
	// then refuses evidence whose digest does not match the window. The
	// field is required so a missing digest cannot silently weaken the
	// boundary.
	PacketDigest string `json:"packet_digest"`
	// ReadbackModel records the model the host reports as having executed
	// the attempt (CD-0058 D2). Concord asserts nothing about which model
	// should have run; this is the sole model evidence the store retains.
	ReadbackModel string `json:"readback_model"`
	// HostProvenance is the declared record of host prompt-injection
	// surfaces (CD-0034 / issue #103); required for v3 evidence.
	HostProvenance *store.WorkerHostProvenance `json:"host_provenance"`
	// Terminal, TerminalFailureKind, and TerminalDetail record an attempt
	// born failed in the same event that creates it (CD-0017 D5): a lost
	// or ambiguous model readback is one terminal row, never a dispatched
	// attempt that a later failure event must close. Terminal is empty or
	// "failed"; the kind is closed by the store and signed on the assertion
	// as failure_kind.
	Terminal            string `json:"terminal,omitempty"`
	TerminalFailureKind string `json:"terminal_failure_kind,omitempty"`
	TerminalDetail      string `json:"terminal_detail,omitempty"`
	// WorkerJob is the worker-job revision the packet's inputs.worker_job
	// bound (CD-0205). The packet digest the assertion signs covers that
	// content, and the fold refuses evidence whose binding differs from the
	// one the dispatch_worker authorization recorded.
	WorkerJob *store.WorkerJobBinding `json:"worker_job,omitempty"`
	// FailureAttribution is the optional typed producer observation a
	// born-failed terminal dispatch records . It carries the
	// observation only, never an origin or identity: the CLI assembles the
	// recorded block under the recorded origin and the store derives the
	// core identity from the dispatch window at ingest. Its canonical digest
	// is what the v2 assertion signs.
	FailureAttribution *store.WorkerFailureObservation `json:"failure_attribution,omitempty"`
	// Assertion authenticates the caller and binds this exact attempt
	// identity (CD-0044 / issue #185).
	Assertion agent.WorkerEvidenceAssertion `json:"assertion"`
}

type workerCompleteRequest struct {
	EventID             string                        `json:"event_id"`
	WorkID              string                        `json:"work_id"`
	AttemptID           string                        `json:"attempt_id"`
	ReadbackModel       string                        `json:"readback_model"`
	ReportSchemaVersion string                        `json:"report_schema_version"`
	WorkerDirectory     string                        `json:"worker_directory,omitempty"`
	Assertion           agent.WorkerEvidenceAssertion `json:"assertion"`
	// Evidence is the parsed agent-lane-report.v1 evidence the adapter read
	// from the worker; EvidenceOrigin says whether it was reported at all
	// (CD-0056 D1/D6).
	Evidence       []store.WorkerReportEvidence `json:"evidence"`
	EvidenceOrigin string                       `json:"evidence_origin"`
	// BaseComparison is the optional informational comparison the worker
	// reported between branch and base results of its verification commands.
	// It joins no obligation vocabulary and no workflow route reads it
	// (CD-0043 D1).
	BaseComparison *store.WorkerBaseComparison `json:"base_comparison,omitempty"`
	// Review is the optional typed review block the worker reported
	// (CD-0197). Whether the dispatching lane requires it is decided in the
	// fold against the stored attempt, live only.
	Review *store.WorkerReviewBlock `json:"review,omitempty"`
	// WorkerJob is the worker-job revision the report claims to complete
	// (CD-0205). The fold refuses a report whose claim differs from the
	// revision the attempt was dispatched under.
	WorkerJob *store.WorkerJobBinding `json:"worker_job,omitempty"`
	// ContextFindings is the optional typed context content the report
	// carried (CON-887). Worker-claimed content only: findings join no
	// obligation vocabulary, and the CLI carries them into the terminal
	// payload beside the evidence the same authenticated request supplied.
	ContextFindings []store.WorkerContextFinding `json:"context_findings,omitempty"`
	// DirectCallerRepairs are the evidenced direct-caller repairs the
	// worker made under the dispatched job's allowance (CD-0227 D2).
	DirectCallerRepairs []store.WorkerDirectCallerRepair `json:"direct_caller_repairs,omitempty"`
}

type workerFailRequest struct {
	EventID       string `json:"event_id"`
	WorkID        string `json:"work_id"`
	AttemptID     string `json:"attempt_id"`
	ReadbackModel string `json:"readback_model"`
	FailureKind   string `json:"failure_kind"`
	Detail        string `json:"detail"`
	// ContextFindings is the optional typed context content the report
	// carried (CON-887). Only the worker-reported failure kind worker_error
	// may retain findings; the store refuses them on every diagnostic kind.
	ContextFindings []store.WorkerContextFinding `json:"context_findings,omitempty"`
	// FailureAttribution is the optional typed producer observation this
	// terminal failure records . It carries the observation only,
	// never an origin or identity: the CLI assembles the recorded block under
	// the recorded origin and the store derives the core identity from the
	// dispatch window at ingest. Its canonical digest is what the v2
	// assertion signs.
	FailureAttribution *store.WorkerFailureObservation `json:"failure_attribution,omitempty"`
	Assertion          agent.WorkerEvidenceAssertion   `json:"assertion"`
}

// workerAbandonRequest is the host-only close route for a dispatched attempt
// whose lane never returned a report. The core derives readback_model from the
// attempt projection and reads liveness from the durable worktree_occupancy
// projection, so the request carries no host session observation.
type workerAbandonRequest struct {
	EventID   string `json:"event_id"`
	WorkID    string `json:"work_id"`
	AttemptID string `json:"attempt_id"`
	Detail    string `json:"detail"`
	// FailureAttribution is the optional typed producer observation the host
	// records for the event that settled the attempt . It carries
	// the observation only, never an origin or identity; its canonical digest
	// is what the v2 assertion signs.
	FailureAttribution *store.WorkerFailureObservation `json:"failure_attribution,omitempty"`
	Assertion          agent.WorkerEvidenceAssertion   `json:"assertion"`
}

// workerFailureAttributionBinding computes the assertion-version binding
// members a request's optional failure_attribution observation requires. A
// present observation is structurally validated and digested here, so the
// assertion must be v2 and authenticate exactly this digest; an absent
// observation keeps the released v1 binding byte-exact.
func workerFailureAttributionBinding(observation *store.WorkerFailureObservation) (int, string, error) {
	if observation == nil {
		return 0, "", nil
	}
	digest, err := store.WorkerFailureObservationDigest(observation)
	if err != nil {
		return 0, "", err
	}
	return agent.WorkerEvidenceAssertionVersion2, digest, nil
}

// workerFailureAttribution assembles the recorded block the payload carries:
// the producer observation under the recorded origin. The core-derived
// identity is never assembled here — the store derives it at ingest from the
// dispatch window and the recorded revisions , so a caller echo can
// neither supply nor select it.
func workerFailureAttribution(observation *store.WorkerFailureObservation) *store.WorkerFailureAttribution {
	if observation == nil {
		return nil
	}
	return &store.WorkerFailureAttribution{Origin: store.WorkerFailureAttributionRecorded, Observation: observation}
}

// runWorkerCommand records worker attempt evidence. Every verb authenticates
// its caller with a signed assertion bound to the exact attempt identity, and
// consumes the assertion nonce in the same transaction as the appended event,
// so authentication and evidence share one commit (CD-0044 / issue #185).
//
// The verified client identity becomes the event actor. Recording evidence
// grants no workflow authority: CD-0017 D4 still forbids a worker run from
// transitioning a step, recording a verdict, or completing work.
func runWorkerCommand(command string, raw []byte, s *store.Store, service *agent.Service, clock func() time.Time, out, errOut io.Writer) int {
	ctx := context.Background()
	// These parent-observed stamps record when the parent CLI recorded evidence, not when the child worked.
	switch command {
	case "worker-dispatch":
		return runWorkerDispatch(ctx, command, raw, s, service, clock, out, errOut)
	case "worker-complete", "worker-fail", "worker-abandon":
		return runWorkerTerminalCommand(ctx, command, raw, s, service, clock, out, errOut)
	}
	writeOperatorDiagnostic(errOut, command, "unsupported command")
	return 2
}

func runWorkerDispatch(ctx context.Context, command string, raw []byte, s *store.Store, service *agent.Service, clock func() time.Time, out, errOut io.Writer) int {
	var request workerDispatchRequest
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	lane, err := store.LookupLane(request.LaneID, request.LaneDigest)
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	if err := store.ValidateWorkerHostProvenance(request.HostProvenance); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	if request.HostProvenance == nil {
		writeOperatorDiagnostic(errOut, command, "worker-dispatch v3 evidence requires host_provenance (CD-0034: host injection is permitted only when recorded)")
		return 1
	}
	// CD-0067 D6: the dispatch assertion quotes the digest the core
	// recorded on the dispatch_worker authorization. The adapter
	// never computes the digest itself, so the CLI is the seam that
	// guarantees the value the assertion claims is the value the
	// core recorded. A missing or malformed digest is a refused
	// request — the gate then checks the recorded digest equals
	// what the adapter signed.
	if !workerPacketDigestPattern.MatchString(request.PacketDigest) {
		writeOperatorDiagnostic(errOut, command, "worker-dispatch requires packet_digest (sha256:64 hex) — the canonical lane-packet digest the dispatch authorization recorded (CD-0067)")
		return 1
	}
	binding := agent.WorkerEvidenceBinding{
		Verb:                 agent.WorkerEvidenceVerbDispatch,
		WorkID:               request.WorkID,
		AttemptID:            request.AttemptID,
		LaneID:               request.LaneID,
		LaneDigest:           request.LaneDigest,
		ReadbackModel:        request.ReadbackModel,
		FailureKind:          request.TerminalFailureKind,
		HostProvenanceDigest: request.HostProvenance.Digest,
		PacketDigest:         request.PacketDigest,
	}
	if request.Terminal != "" && request.Terminal != "failed" {
		writeOperatorDiagnostic(errOut, command, "worker-dispatch terminal must be empty or 'failed'")
		return 1
	}
	if (request.Terminal == "failed") != (request.TerminalFailureKind != "") {
		writeOperatorDiagnostic(errOut, command, "worker-dispatch terminal='failed' and terminal_failure_kind are declared together")
		return 1
	}
	// only a born-failed terminal record carries attribution; an
	// ordinary dispatch opens no terminal outcome to attribute.
	if request.FailureAttribution != nil && request.Terminal != "failed" {
		writeOperatorDiagnostic(errOut, command, "worker-dispatch carries failure_attribution only on a born-failed terminal record")
		return 1
	}
	attributionVersion, attributionDigest, err := workerFailureAttributionBinding(request.FailureAttribution)
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	binding.Version = attributionVersion
	binding.ObservationDigest = attributionDigest
	payload := store.WorkerDispatchedPayload{AttemptID: request.AttemptID, LaneID: request.LaneID, LaneDigest: request.LaneDigest, CapabilityClass: lane.CapabilityClass, PacketSchemaVersion: request.PacketSchemaVersion, ReportSchemaVersion: request.ReportSchemaVersion, HostProvenance: request.HostProvenance, ReadbackModel: request.ReadbackModel, PacketDigest: request.PacketDigest, Terminal: request.Terminal, TerminalFailureKind: request.TerminalFailureKind, TerminalDetail: request.TerminalDetail, WorkerJob: request.WorkerJob, FailureAttribution: workerFailureAttribution(request.FailureAttribution)}
	if len(request.AuthorizedPacket) != 0 {
		if err := store.ValidateRecoveryPacket(request.AuthorizedPacket, request.PacketDigest, request.WorkID, request.AttemptID); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			writeWorkerEvidenceFailure(out, errOut, err, nil)
			return 1
		}
	}
	return applyWorkerEvidence(ctx, command, s, service, request.Assertion, binding, store.Event{EventID: request.EventID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, OccurredAt: clock().UTC(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerDispatched), Payload: mustMarshalWorkerPayload(payload)}, out, errOut)
}

func runWorkerTerminalCommand(ctx context.Context, command string, raw []byte, s *store.Store, service *agent.Service, clock func() time.Time, out, errOut io.Writer) int {
	switch command {
	case "worker-complete":
		var request workerCompleteRequest
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		if request.EvidenceOrigin == "" {
			writeOperatorDiagnostic(errOut, command, "worker-complete v2 evidence requires evidence_origin (CD-0056: a completion records whether its evidence was reported)")
			return 1
		}
		binding := agent.WorkerEvidenceBinding{
			Verb:          agent.WorkerEvidenceVerbComplete,
			WorkID:        request.WorkID,
			AttemptID:     request.AttemptID,
			ReadbackModel: request.ReadbackModel,
		}
		payload := store.WorkerCompletedPayload{AttemptID: request.AttemptID, ReadbackModel: request.ReadbackModel, ReportSchemaVersion: request.ReportSchemaVersion, WorkerDirectory: request.WorkerDirectory, Evidence: request.Evidence, EvidenceOrigin: request.EvidenceOrigin, BaseComparison: request.BaseComparison, Review: request.Review, WorkerJob: request.WorkerJob, ContextFindings: request.ContextFindings, DirectCallerRepairs: request.DirectCallerRepairs}
		event := store.Event{EventID: request.EventID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, OccurredAt: clock().UTC(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerCompleted), Payload: mustMarshalWorkerPayload(payload)}
		return applyWorkerEvidence(ctx, command, s, service, request.Assertion, binding, event, out, errOut)
	case "worker-fail":
		var request workerFailRequest
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		attributionVersion, attributionDigest, err := workerFailureAttributionBinding(request.FailureAttribution)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		binding := agent.WorkerEvidenceBinding{
			Verb:              agent.WorkerEvidenceVerbFail,
			WorkID:            request.WorkID,
			AttemptID:         request.AttemptID,
			ReadbackModel:     request.ReadbackModel,
			FailureKind:       request.FailureKind,
			Version:           attributionVersion,
			ObservationDigest: attributionDigest,
		}
		payload := store.WorkerFailedPayload{AttemptID: request.AttemptID, ReadbackModel: request.ReadbackModel, FailureKind: request.FailureKind, Detail: request.Detail, ContextFindings: request.ContextFindings, FailureAttribution: workerFailureAttribution(request.FailureAttribution)}
		return applyWorkerEvidence(ctx, command, s, service, request.Assertion, binding, store.Event{EventID: request.EventID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, OccurredAt: clock().UTC(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerFailed), Payload: mustMarshalWorkerPayload(payload)}, out, errOut)
	case "worker-abandon":
		var request workerAbandonRequest
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		attributionVersion, attributionDigest, err := workerFailureAttributionBinding(request.FailureAttribution)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		binding := agent.WorkerEvidenceBinding{
			Verb:              agent.WorkerEvidenceVerbFail,
			WorkID:            request.WorkID,
			AttemptID:         request.AttemptID,
			FailureKind:       store.WorkerFailureAbandoned,
			Version:           attributionVersion,
			ObservationDigest: attributionDigest,
		}
		payload := store.WorkerFailedPayload{AttemptID: request.AttemptID, FailureKind: store.WorkerFailureAbandoned, Detail: request.Detail, FailureAttribution: workerFailureAttribution(request.FailureAttribution)}
		event := store.Event{EventID: request.EventID, Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: request.WorkID, OccurredAt: clock().UTC(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerFailed), Payload: mustMarshalWorkerPayload(payload)}
		return applyWorkerEvidence(ctx, command, s, service, request.Assertion, binding, event, out, errOut)
	}
	writeOperatorDiagnostic(errOut, command, "unsupported command")
	return 2
}

// applyWorkerEvidence authenticates and consumes a fresh assertion in the
// same durable transaction as new evidence or an exact existing-event match.
// A match appends nothing and consumes no second dispatch authorization.
func applyWorkerEvidence(ctx context.Context, command string, s *store.Store, service *agent.Service, assertion agent.WorkerEvidenceAssertion, binding agent.WorkerEvidenceBinding, event store.Event, out, errOut io.Writer) int {
	if err := s.RequireActionableWork(ctx, binding.WorkID, command); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		writeWorkerEvidenceFailure(out, errOut, err, nil)
		return 1
	}
	// CD-0179 D3: an abandoned close releases a legacy occupancy row only on
	// the lease-set proof, so the verb reads the live host lease set before
	// the transaction opens and carries the observation to the fold gate on
	// the context. A missing or unreadable set releases nothing.
	if binding.Verb == agent.WorkerEvidenceVerbFail && binding.FailureKind == store.WorkerFailureAbandoned {
		ctx = store.WithHostLeaseSet(ctx, s.ReadHostLeases())
	}
	// The terminal delivery observation is sampled before this
	// transaction opens — no Git subprocess runs under a SQL transaction
	// (CD-0195 D2). The preparation reads the window, attempt, and claim
	// identities, closes them, probes Git under its fixed deadline only for
	// a new completed implementation attempt, and refuses typed with zero
	// effects on failure. The sealed proof rides the context into the
	// signed ingest seam, which revalidates every sampled identity with SQL
	// only.
	if binding.Verb == agent.WorkerEvidenceVerbComplete {
		preparation, preparationErr := store.PrepareWorkerTerminalDelivery(ctx, s, store.PrepareWorkerTerminalDeliveryRequest{WorkID: binding.WorkID, AttemptID: binding.AttemptID, EventID: event.EventID})
		if preparationErr != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(preparationErr))
			writeWorkerEvidenceFailure(out, errOut, preparationErr, nil)
			return 1
		}
		ctx = store.WithWorkerTerminalPreparation(ctx, preparation)
	}
	var eventIDs []string
	err := s.TransactDurable(ctx, func(tx *store.Transaction) error {
		var ingestErr error
		eventIDs, ingestErr = ingestWorkerEvidenceTx(ctx, tx, service, assertion, binding, event)
		return ingestErr
	})
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		// An uncertain durable commit can leave the very event a retry will
		// reconcile. The typed result carries the event identities then, so
		// the adapter reports a possible effect instead of a bare refusal.
		var failure *store.Failure
		if !errors.As(err, &failure) || !failure.EffectPossible {
			eventIDs = nil
		}
		writeWorkerEvidenceFailure(out, errOut, err, eventIDs)
		return 1
	}
	return writeOperatorResult(command, s, eventIDs, nil, out, errOut)
}

func ingestWorkerEvidenceTx(ctx context.Context, tx *store.Transaction, service *agent.Service, assertion agent.WorkerEvidenceAssertion, binding agent.WorkerEvidenceBinding, event store.Event) ([]string, error) {
	existing, found, lookupErr := store.EventByIDTx(ctx, tx, event.EventID)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if binding.Verb != agent.WorkerEvidenceVerbDispatch {
		attempt, err := store.WorkerAttemptByIDTx(ctx, tx, binding.AttemptID)
		if err != nil {
			return nil, err
		}
		if attempt.WorkID != binding.WorkID {
			return nil, errors.New("worker attempt belongs to a different work item")
		}
		if store.WorkerAttemptIsTerminal(attempt) && !found {
			return nil, errors.New("worker attempt already reached a terminal outcome")
		}
		binding.LaneID = attempt.LaneID
		binding.LaneDigest = attempt.LaneDigest
	}
	if binding.Verb == agent.WorkerEvidenceVerbDispatch && !found {
		// The dispatch window integrity check runs after the attempt
		// lookup (a no-op for dispatch) and before the assertion
		// validation: a worker that fails this gate cannot consume a
		// signed assertion nonce. CD-0067 D6 makes packet digest part
		// of the gate so evidence that quotes a different packet is
		// refused before the signature is verified.
		if err := store.ValidateWorkerDispatchWindow(ctx, tx, binding.WorkID, "", binding.AttemptID, binding.PacketDigest); err != nil {
			return nil, err
		}
	}
	principal, err := service.ValidateWorkerEvidenceAssertionTx(ctx, tx, assertion, binding)
	if err != nil {
		return nil, err
	}
	event.Actor = "client:" + assertion.ClientRef + ":" + principal
	if found {
		if err := store.ValidateWorkerEvidenceReplayWindow(ctx, tx, binding.WorkID, binding.AttemptID, binding.PacketDigest); err != nil {
			return nil, err
		}
		// CD-0208 D1: the acknowledgment compares against the stored
		// event's original payload version and complete payload. The
		// transport carries no user version, so the comparison event is
		// reconstructed at the stored row's own version, with the
		// lane-actor enrichment derived again from the authenticated
		// identity when the original dispatch shape carried one.
		acknowledgment, err := reconstructWorkerEvidenceAck(ctx, tx, existing, event, "principal:"+principal, "client:"+assertion.ClientRef)
		if err != nil {
			return nil, err
		}
		if !sameWorkerEvidence(existing, acknowledgment) {
			return nil, errors.New("worker evidence event identity conflicts with an existing event")
		}
		return []string{existing.EventID}, nil
	}
	if binding.Verb == agent.WorkerEvidenceVerbDispatch {
		// CD-0017 D4: the dispatched lane is the step's
		// executing actor. The actor is recorded and pinned in the same
		// transaction as the attempt, from the host identity that
		// authenticated the dispatch.
		prepared, prepareErr := store.PrepareLaneActorDispatch(ctx, tx, event, "principal:"+principal, "client:"+assertion.ClientRef)
		if prepareErr != nil {
			return nil, prepareErr
		}
		result, applyErr := store.AppendLaneActorDispatchTx(ctx, tx, prepared)
		if applyErr != nil {
			return nil, applyErr
		}
		return result.EventIDs, nil
	}
	result, err := store.ApplyWorkerEvidenceOperationTx(ctx, tx, event)
	if err != nil {
		return nil, err
	}
	return result.EventIDs, nil
}

func mustMarshalWorkerPayload(value any) []byte {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}

func validateRequiredCommandFields(command string, raw []byte) error {
	var spec *commandSpec
	for i := range commandSpecs {
		if commandSpecs[i].Canonical == command {
			spec = &commandSpecs[i]
			break
		}
	}
	if spec == nil || len(spec.RequiredFields) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	for _, field := range spec.RequiredFields {
		raw, ok := object[field.Name]
		if !ok {
			return fmt.Errorf("missing required field %s", field.Name)
		}
		if len(field.Nested) == 0 {
			continue
		}
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(raw, &nested); err != nil || nested == nil {
			return fmt.Errorf("required field %s must be an object", field.Name)
		}
		for _, name := range field.Nested {
			if _, ok := nested[name]; !ok {
				return fmt.Errorf("missing required field %s.%s", field.Name, name)
			}
		}
	}
	return nil
}

// databasePath resolves the store every command route uses. It admits the
// path before any route reads or writes the data root's leases, locks,
// fences, or store (CD-0240 D1).
func databasePath() (string, error) {
	path, err := resolveDatabasePath()
	if err != nil {
		return "", err
	}
	if err := store.AdmitStorePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func resolveDatabasePath() (string, error) {
	if override := os.Getenv(dbOverrideEnv); override != "" {
		absolute, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("invalid database override")
		}
		probe := absolute
		if info, statErr := os.Stat(probe); statErr == nil && !info.IsDir() {
			probe = filepath.Dir(probe)
		}
		for {
			if _, statErr := os.Stat(probe); statErr == nil {
				break
			}
			parent := filepath.Dir(probe)
			if parent == probe {
				break
			}
			probe = parent
		}
		if _, err := exec.Command("git", "-C", probe, "rev-parse", "--show-toplevel").Output(); err == nil { //nolint:gosec // git is fixed, probe is a separate absolute argv value, and no shell is invoked.
			return "", fmt.Errorf("database override refused inside a git repository or worktree")
		}
		return absolute, nil
	}
	return store.DefaultPath()
}

func decodeObject(data []byte, value any) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func runInvoke(raw []byte, s *store.Store, service *agent.Service, out, errOut io.Writer) int {
	response, err := agent.Invoke(context.Background(), s, service, raw)
	if err != nil {
		writeDiagnostic(errOut, diagnosticText(err))
		return inputRefusalExit
	}
	return writeJSON(out, response, errOut)
}

// runProductKnowledgeConfiguration executes the four operator verbs that
// configure a Product's knowledge source set (PM6 §2, CD-0200): designating
// or clearing the shared-law home, and registering or removing a member
// Project knowledge source. One request shape, one Product-scoped result.
func runProductKnowledgeConfiguration(command string, raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		ProductID       string `json:"product_id"`
		ProjectID       string `json:"project_id"`
		LocatorID       string `json:"locator_id"`
		Reason          string `json:"reason"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	var result store.ApplyOperationResult
	var err error
	switch command {
	case "product-knowledge-home-designate":
		result, err = designateOperatorDomainShape(context.Background(), s, store.ProductKnowledgeHomeDesignation{
			ProductID: request.ProductID, ProjectID: request.ProjectID, LocatorID: request.LocatorID,
			Reason: request.Reason, ExpectedVersion: request.ExpectedVersion,
		})
	case "product-knowledge-home-clear":
		result, err = s.ClearProductKnowledgeHome(context.Background(), store.ProductKnowledgeHomeDesignation{
			ProductID: request.ProductID, Reason: request.Reason, ExpectedVersion: request.ExpectedVersion,
		})
	case "product-knowledge-source-register":
		result, err = s.RegisterProductKnowledgeSource(context.Background(), store.ProductKnowledgeSourceRegistration{
			ProductID: request.ProductID, ProjectID: request.ProjectID, LocatorID: request.LocatorID,
			Reason: request.Reason, ExpectedVersion: request.ExpectedVersion,
		})
	case "product-knowledge-source-remove":
		result, err = s.RemoveProductKnowledgeSource(context.Background(), store.ProductKnowledgeSourceRegistration{
			ProductID: request.ProductID, ProjectID: request.ProjectID, LocatorID: request.LocatorID,
			Reason: request.Reason, ExpectedVersion: request.ExpectedVersion,
		})
	}
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
}

func runInternal(command string, raw []byte, service *agent.Service, s *store.Store, clock func() time.Time, out, errOut io.Writer) int {
	switch command {
	case "domain-shape-check":
		return runDomainShapeCheck(raw, s, out, errOut)
	case "client-show":
		return runClientShow(raw, s, out, errOut)
	case "client-register", "client-policy-update", "client-policy-expand":
		return runClientPolicyCommand(command, raw, service, s, out, errOut)
	case "client-key-rotate", "client-revoke":
		return runClientKeyCommand(command, raw, service, s, out, errOut)
	case "project-locator-add", "project-locator-update", "project-locator-remove":
		return runProjectLocatorCommand(command, raw, s, out, errOut)
	case "product-create":
		return runProductCreate(command, raw, s, out, errOut)
	case "resource-create", "resource-share", "domain-project-attachments-replace", "domain-resource-attachments-replace":
		return runResourceDomainCommand(command, raw, s, clock, out, errOut)
	case "project-create", "product-project-add", "product-stage-update":
		return runProductProjectCommand(command, raw, s, out, errOut)
	case "product-knowledge-home-designate", "product-knowledge-home-clear", "product-knowledge-source-register", "product-knowledge-source-remove":
		return runProductKnowledgeConfiguration(command, raw, s, out, errOut)
	case "backup":
		return runBackup(command, raw, s, out, errOut)
	case "worktree-locate":
		return runWorktreeLocate(raw, s, out, errOut)
	case "project-resolve":
		return runProjectResolve(raw, s, out, errOut)
	case "cd-reservations":
		return runCDReservations(raw, s, out, errOut)
	case "predecessor-import":
		return runPredecessorImport(raw, s, out, errOut)
	default:
		writeOperatorDiagnostic(errOut, command, "unsupported command")
		return 2
	}
}

func runClientPolicyCommand(command string, raw []byte, service *agent.Service, s *store.Store, out, errOut io.Writer) int {
	ctx := context.Background()
	switch command {
	case "client-register":
		var request struct {
			ClientRef    string   `json:"client_ref"`
			KeyID        string   `json:"key_id"`
			PrincipalRef string   `json:"principal_ref"`
			PublicKey    string   `json:"public_key"`
			Capabilities []string `json:"capabilities"`
			ProductScope []string `json:"product_scope"`
			ProjectScope []string `json:"project_scope"`
			AgentScope   []string `json:"agent_scope"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		key, err := base64.StdEncoding.DecodeString(request.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			writeOperatorDiagnostic(errOut, command, "public_key must be base64 Ed25519")
			return 1
		}
		caps := make([]agent.Capability, len(request.Capabilities))
		for i, v := range request.Capabilities {
			caps[i] = agent.Capability(v)
		}
		err = service.RegisterTrustedClient(ctx, agent.ClientRegistration{ClientRef: request.ClientRef, KeyID: request.KeyID, PublicKey: ed25519.PublicKey(key), Policy: agent.TrustedClientPolicy{PrincipalRef: request.PrincipalRef, Capabilities: caps, ProductScope: request.ProductScope, ProjectScope: request.ProjectScope, AgentScope: request.AgentScope}})
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, nil, nil, out, errOut)
	case "client-policy-update":
		var request struct {
			ClientRef    string   `json:"client_ref"`
			PrincipalRef string   `json:"principal_ref"`
			Capabilities []string `json:"capabilities"`
			ProductScope []string `json:"product_scope"`
			ProjectScope []string `json:"project_scope"`
			AgentScope   []string `json:"agent_scope"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		caps := make([]agent.Capability, len(request.Capabilities))
		for i, v := range request.Capabilities {
			caps[i] = agent.Capability(v)
		}
		if err := service.UpdateTrustedClientPolicy(ctx, request.ClientRef, agent.TrustedClientPolicy{PrincipalRef: request.PrincipalRef, Capabilities: caps, ProductScope: request.ProductScope, ProjectScope: request.ProjectScope, AgentScope: request.AgentScope}); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, nil, nil, out, errOut)
	case "client-policy-expand":
		var request struct {
			ClientRef    string   `json:"client_ref"`
			Capabilities []string `json:"capabilities"`
			ProductScope []string `json:"product_scope"`
			ProjectScope []string `json:"project_scope"`
			AgentScope   []string `json:"agent_scope"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		caps := make([]agent.Capability, len(request.Capabilities))
		for i, v := range request.Capabilities {
			caps[i] = agent.Capability(v)
		}
		if err := service.ExpandTrustedClientPolicy(ctx, request.ClientRef, agent.TrustedClientPolicy{Capabilities: caps, ProductScope: request.ProductScope, ProjectScope: request.ProjectScope, AgentScope: request.AgentScope}); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, nil, nil, out, errOut)
	default:
		writeOperatorDiagnostic(errOut, command, "unsupported command")
		return 2
	}
}

func runClientKeyCommand(command string, raw []byte, service *agent.Service, s *store.Store, out, errOut io.Writer) int {
	ctx := context.Background()
	switch command {
	case "client-key-rotate":
		var request struct {
			ClientRef string `json:"client_ref"`
			KeyID     string `json:"key_id"`
			PublicKey string `json:"public_key"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		key, err := base64.StdEncoding.DecodeString(request.PublicKey)
		if err != nil || len(key) != ed25519.PublicKeySize {
			writeOperatorDiagnostic(errOut, command, "public_key must be base64 Ed25519")
			return 1
		}
		if err := service.RotateClientKey(ctx, agent.ClientRegistration{ClientRef: request.ClientRef, KeyID: request.KeyID, PublicKey: ed25519.PublicKey(key)}); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, nil, nil, out, errOut)
	case "client-revoke":
		var request struct {
			ClientRef string `json:"client_ref"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		if err := service.RevokeClient(ctx, request.ClientRef); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, nil, nil, out, errOut)
	default:
		writeOperatorDiagnostic(errOut, command, "unsupported command")
		return 2
	}
}

func runProjectLocatorCommand(command string, raw []byte, s *store.Store, out, errOut io.Writer) int {
	ctx := context.Background()
	switch command {
	case "project-locator-add", "project-locator-update":
		var request struct {
			ProjectID       string            `json:"project_id"`
			LocatorID       string            `json:"locator_id"`
			Kind            store.LocatorKind `json:"kind"`
			Value           string            `json:"value"`
			ExpectedVersion int64             `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		method := s.AddProjectLocator
		if command == "project-locator-update" {
			method = s.UpdateProjectLocator
		}
		if err := method(ctx, request.ProjectID, store.ProjectLocator{ID: request.LocatorID, Kind: request.Kind, Value: request.Value}, request.ExpectedVersion); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, nil, []operatorRef{{EntityKind: store.SubjectProject, ID: request.ProjectID}}, out, errOut)
	case "project-locator-remove":
		var request struct {
			ProjectID       string `json:"project_id"`
			LocatorID       string `json:"locator_id"`
			ExpectedVersion int64  `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		if err := s.RemoveProjectLocator(ctx, request.ProjectID, request.LocatorID, request.ExpectedVersion); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, nil, []operatorRef{{EntityKind: store.SubjectProject, ID: request.ProjectID}}, out, errOut)
	default:
		writeOperatorDiagnostic(errOut, command, "unsupported command")
		return 2
	}
}

func runProductCreate(command string, raw []byte, s *store.Store, out, errOut io.Writer) int {
	ctx := context.Background()
	var request struct {
		ProductID          string `json:"product_id"`
		DisplayName        string `json:"display_name"`
		StageMaturity      string `json:"stage_maturity"`
		StageAudience      string `json:"stage_audience_commitment"`
		ProjectID          string `json:"project_id"`
		ProjectDisplayName string `json:"project_display_name"`
		Role               string `json:"role"`
		MembershipReason   string `json:"reason"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	result, err := s.CreateProductWithProject(ctx, store.ProductCreation{
		ProductID: request.ProductID, DisplayName: request.DisplayName, StageMaturity: request.StageMaturity,
		StageAudienceCommitment: request.StageAudience, ProjectID: request.ProjectID,
		ProjectDisplayName: request.ProjectDisplayName, Role: request.Role, Reason: request.MembershipReason,
	})
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}, {EntityKind: store.SubjectProject, ID: request.ProjectID}}, out, errOut)
}

func runResourceDomainCommand(command string, raw []byte, s *store.Store, clock func() time.Time, out, errOut io.Writer) int {
	ctx := context.Background()
	switch command {
	case "resource-create":
		var request struct {
			EventID                 string          `json:"event_id"`
			ResourceID              string          `json:"resource_id"`
			ProductID               string          `json:"product_id"`
			DisplayName             string          `json:"display_name"`
			Class                   string          `json:"class"`
			Kind                    string          `json:"kind"`
			Purpose                 string          `json:"purpose"`
			StageMaturity           string          `json:"stage_maturity"`
			StageAudienceCommitment string          `json:"stage_audience_commitment"`
			Environments            []string        `json:"environments"`
			LocatorAbsenceReason    string          `json:"locator_absence_reason"`
			MetadataSchemaVersion   string          `json:"metadata_schema_version"`
			Metadata                json.RawMessage `json:"metadata"`
			OwnerPurpose            string          `json:"owner_purpose"`
			OwnerEnvironments       []string        `json:"owner_environments"`
			ExpectedProductVersion  int64           `json:"expected_product_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		if _, err := store.CreateManagedResource(ctx, s, store.ManagedResourceCreateRequest{
			EventID: request.EventID, ResourceID: request.ResourceID, ProductID: request.ProductID,
			DisplayName: request.DisplayName, Class: request.Class, Kind: request.Kind, Purpose: request.Purpose,
			StageMaturity: request.StageMaturity, StageAudienceCommitment: request.StageAudienceCommitment,
			Environments: request.Environments, LocatorAbsenceReason: request.LocatorAbsenceReason,
			MetadataSchemaVersion: request.MetadataSchemaVersion, Metadata: request.Metadata,
			OwnerPurpose: request.OwnerPurpose, OwnerEnvironments: request.OwnerEnvironments,
			ExpectedProductVersion: request.ExpectedProductVersion, Actor: "operator", OccurredAt: clock().UTC(),
		}); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, []string{request.EventID}, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "resource-share":
		var request struct {
			EventID                 string   `json:"event_id"`
			ResourceID              string   `json:"resource_id"`
			ProductID               string   `json:"product_id"`
			Purpose                 string   `json:"purpose"`
			Environments            []string `json:"environments"`
			ExpectedResourceVersion int64    `json:"expected_resource_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		if err := store.AddManagedResourceConsumer(ctx, s, store.AddManagedResourceConsumerRequest{
			EventID: request.EventID, ResourceID: request.ResourceID, ProductID: request.ProductID,
			Purpose: request.Purpose, Environments: request.Environments, ExpectedResourceVersion: request.ExpectedResourceVersion,
			Actor: "operator", OccurredAt: clock().UTC(),
		}); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, []string{request.EventID}, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "domain-project-attachments-replace":
		var request struct {
			EventID         string                          `json:"event_id"`
			ProductID       string                          `json:"product_id"`
			DomainID        string                          `json:"domain_id"`
			ExpectedVersion int64                           `json:"expected_version"`
			Attachments     []store.DomainProjectAttachment `json:"attachments"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		if err := store.ReplaceDomainProjectAttachments(ctx, s, store.DomainProjectAttachmentsRequest{
			EventID: request.EventID, ProductID: request.ProductID, DomainID: request.DomainID,
			ExpectedVersion: request.ExpectedVersion, Attachments: request.Attachments,
			Actor: "operator", OccurredAt: clock().UTC(),
		}); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, []string{request.EventID}, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "domain-resource-attachments-replace":
		var request struct {
			EventID         string                           `json:"event_id"`
			ProductID       string                           `json:"product_id"`
			DomainID        string                           `json:"domain_id"`
			ExpectedVersion int64                            `json:"expected_version"`
			Attachments     []store.DomainResourceAttachment `json:"attachments"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		if err := store.ReplaceDomainResourceAttachments(ctx, s, store.DomainResourceAttachmentsRequest{
			EventID: request.EventID, ProductID: request.ProductID, DomainID: request.DomainID,
			ExpectedVersion: request.ExpectedVersion, Attachments: request.Attachments,
			Actor: "operator", OccurredAt: clock().UTC(),
		}); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, []string{request.EventID}, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	default:
		writeOperatorDiagnostic(errOut, command, "unsupported command")
		return 2
	}
}

func runProductProjectCommand(command string, raw []byte, s *store.Store, out, errOut io.Writer) int {
	ctx := context.Background()
	switch command {
	case "project-create":
		var request struct {
			ProjectID          string `json:"project_id"`
			DisplayName        string `json:"display_name"`
			ProductID          string `json:"product_id"`
			Role               string `json:"role"`
			Reason             string `json:"reason"`
			ExpectedProductVer int64  `json:"expected_product_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		result, err := s.CreateProjectForProduct(ctx, store.ProjectCreation{
			ProjectID: request.ProjectID, DisplayName: request.DisplayName, ProductID: request.ProductID,
			Role: request.Role, Reason: request.Reason, ExpectedProductVersion: request.ExpectedProductVer,
		})
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProject, ID: request.ProjectID}, {EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "product-project-add":
		var request struct {
			ProductID       string `json:"product_id"`
			ProjectID       string `json:"project_id"`
			Role            string `json:"role"`
			Reason          string `json:"reason"`
			ExpectedVersion int64  `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		result, err := s.AddProductProjectMembership(ctx, store.ProductMembershipAddition{
			ProductID: request.ProductID, ProjectID: request.ProjectID, Role: request.Role,
			Reason: request.Reason, ExpectedVersion: request.ExpectedVersion,
		})
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	case "product-stage-update":
		var request struct {
			ProductID               string `json:"product_id"`
			StageMaturity           string `json:"stage_maturity"`
			StageAudienceCommitment string `json:"stage_audience_commitment"`
			Reason                  string `json:"reason"`
			ExpectedVersion         int64  `json:"expected_version"`
		}
		if err := decodeObject(raw, &request); err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		result, err := s.ChangeProductStage(ctx, store.ProductStageChange{
			ProductID: request.ProductID, StageMaturity: request.StageMaturity, StageAudienceCommitment: request.StageAudienceCommitment,
			Reason: request.Reason, ExpectedVersion: request.ExpectedVersion,
		})
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		return writeOperatorResult(command, s, result.EventIDs, []operatorRef{{EntityKind: store.SubjectProduct, ID: request.ProductID}}, out, errOut)
	default:
		writeOperatorDiagnostic(errOut, command, "unsupported command")
		return 2
	}
}

func runBackup(command string, raw []byte, s *store.Store, out, errOut io.Writer) int {
	ctx := context.Background()
	var request struct {
		Destination string `json:"destination"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	if request.Destination == s.Path() {
		writeOperatorDiagnostic(errOut, command, "backup destination equals the live database path; choose a separate snapshot path")
		return 1
	}
	manifest, err := store.Backup(ctx, s, request.Destination)
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	if _, err := store.VerifyBackup(ctx, request.Destination); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	return writeJSON(out, manifest, errOut)
}

// runPredecessorInventory validates a harvest snapshot and emits the bounded
// enumeration report. It runs before any store open because the inventory
// never touches Concord authority — it is read-only against the snapshot file.
func runPredecessorInventory(raw []byte, out, errOut io.Writer) int {
	var request struct {
		SnapshotPath string `json:"snapshot_path"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "predecessor-inventory", diagnosticText(err))
		return 1
	}
	if request.SnapshotPath == "" {
		writeOperatorDiagnostic(errOut, "predecessor-inventory", "snapshot_path must be a non-empty path")
		return 1
	}
	info, statErr := os.Stat(request.SnapshotPath)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			writeOperatorDiagnostic(errOut, "predecessor-inventory", fmt.Sprintf("snapshot file does not exist: %s", request.SnapshotPath))
			return 1
		}
		writeOperatorDiagnostic(errOut, "predecessor-inventory", "snapshot path is unavailable: "+diagnosticText(statErr))
		return 1
	}
	if info.IsDir() {
		writeOperatorDiagnostic(errOut, "predecessor-inventory", fmt.Sprintf("snapshot path is a directory: %s", request.SnapshotPath))
		return 1
	}
	snapshot, err := predecessor.Load(request.SnapshotPath)
	if err != nil {
		writeOperatorDiagnostic(errOut, "predecessor-inventory", diagnosticText(err))
		return 1
	}
	report := predecessor.Inventory(snapshot)
	return writeJSON(out, report, errOut)
}

type operatorRef struct {
	EntityKind store.SubjectType
	ID         string
}

type operatorResponse struct {
	OK          bool                 `json:"ok"`
	ProductID   string               `json:"product_id,omitempty"`
	ProjectID   string               `json:"project_id,omitempty"`
	EventIDs    []string             `json:"event_ids,omitempty"`
	ChangedRefs []operatorChangedRef `json:"changed_refs"`
}

type operatorChangedRef struct {
	EntityKind string `json:"entity_kind"`
	ID         string `json:"id"`
	Version    string `json:"version"`
}

func writeOperatorResult(command string, s *store.Store, eventIDs []string, refs []operatorRef, out, errOut io.Writer) int {
	changed := make([]operatorChangedRef, 0, len(refs))
	response := operatorResponse{OK: true, EventIDs: eventIDs, ChangedRefs: changed}
	for _, ref := range refs {
		version, err := s.EntityVersion(context.Background(), ref.EntityKind, ref.ID)
		if err != nil {
			writeOperatorDiagnostic(errOut, command, diagnosticText(err))
			return 1
		}
		changed = append(changed, operatorChangedRef{EntityKind: string(ref.EntityKind), ID: ref.ID, Version: strconv.FormatInt(version, 10)})
		switch ref.EntityKind {
		case store.SubjectProduct:
			response.ProductID = ref.ID
		case store.SubjectProject:
			response.ProjectID = ref.ID
		}
	}
	response.ChangedRefs = changed
	return writeJSON(out, response, errOut)
}

func writeOperatorDiagnostic(out io.Writer, command, message string) {
	writeDiagnostic(out, fmt.Sprintf("concord %s: %s", command, message))
}

func writeJSON(out io.Writer, value any, errOut io.Writer) int {
	data, err := json.Marshal(value)
	if err != nil {
		writeDiagnostic(errOut, diagnosticText(err))
		return 1
	}
	if len(data) > agent.MaxEnvelopeBytes {
		writeDiagnostic(errOut, "output exceeds 65536 bytes")
		return 1
	}
	_, err = fmt.Fprintln(out, string(data))
	if err != nil {
		writeDiagnostic(errOut, diagnosticText(err))
		return 1
	}
	return 0
}

// writeDiagnostic prints the whole message. Stderr carries no size contract,
// and a refusal that names every blocking session is only actionable in full.
func writeDiagnostic(out io.Writer, message string) {
	_, _ = fmt.Fprintln(out, message)
}

// diagnosticText renders an error for stderr. Failure.Error omits the recovery
// the store decided, so a store refusal's recovery is added here as the last
// line, once, however the refusal was wrapped. A route that has no refusal to
// carry its recovery formats the line with recoveryLine.
func diagnosticText(err error) string {
	var failure *store.Failure
	if errors.As(err, &failure) && failure.RecoveryAction != "" {
		action := failure.RecoveryAction
		if len(failure.RecoveryRefs) > 0 {
			action += ": " + strings.Join(failure.RecoveryRefs, ", ")
		}
		return err.Error() + "\n" + recoveryLine(action)
	}
	return err.Error()
}

func recoveryLine(action string) string {
	return "recovery: " + action
}
