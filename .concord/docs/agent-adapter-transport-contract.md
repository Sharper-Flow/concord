# Concord Agent Adapter and Transport Contract (TS6)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** TS6; binding input to CD-0005 §6 and TS7–TS9.
> **Binding inputs:** CD-0003 short-lived Go CLI/no daemon; accepted TS1–TS5
> contracts; capability-placement native-authority rule; current official OpenCode
> custom-tool, plugin, permission, and MCP documentation reviewed 2026-08-06.
> **Does not decide:** TS7 result/error field layout, TS8 digest identity/change
> evidence, TS9 pre-go-live measurements, adapter package/repository name, UI styling,
> or C15.

## Context

Concord's Go core needs one primary client transport. The binding inputs are
CD-0003's short-lived Go CLI decision, the accepted TS1 through TS5
contracts, the capability-placement native-authority rule, and the official
OpenCode custom-tool, plugin, permission, and MCP documentation reviewed
2026-08-06. The thin TypeScript adapter provides the accepted custom tools
through a plugin entry module, with transport to the short-lived CLI and the
host approval bridge.

## Contract

Every recorded write is one current-row mutation; no event append exists
([CD-0242](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2).

The approval bridge applies only to core-minted challenges required by CD-0013 D11, not every consequential mutation. Routine and engineering routes must not create host consent prompts or fabricated approval records. On a decision-bearing challenge, transport the complete typed `work_decision` summary unchanged; the host displays its work, contract version, and decision domains alongside the existing bound facts. Missing required approval transport still fails closed.

The binding contract is sections 1 through 5: the adapter boundary and its
host responsibilities, the process and transport rules including client
bootstrap, the registration and schema rules including the hidden
TS5 envelope, the approval bridge with its `always: []` rule, and the adapter
boundary of allowed and forbidden behaviors. Sections 6 through 11 describe
registration, alternatives, evidence, implementation acceptance, and
falsifiers, and carry no obligation.

## 1. Decision

Concord's primary OpenCode integration is a **thin TypeScript adapter**. The
`concord.ts` module defines the generated current tools and adapts calls to
CD-0003's short-lived Go CLI. The `concord-plugin.ts` entry module registers
those definitions under their `concord_*` names through OpenCode's plugin API.

The adapter's domain-tool interface has three host-specific responsibilities:

1. register the accepted tool names, descriptions, and strict schemas once;
2. inject OpenCode's trusted session, agent, directory, and worktree context into
   TS5's hidden envelope; and
3. submit a host permission request through the current tool-context `ask` method
   when the core returns an exact approval challenge.

The plugin entry also registers only the host runtime hooks that accepted
decisions require. The continuity hook re-asserts core-derived state without
approval, gating, or payload-validation
authority, as [CD-0090 D1–D2](./decisions/CD-0090-per-turn-continuity-re-pin-hook.md#d1-runtime-hooks-and-authority-hooks-are-different-classes)
requires. Every domain decision, scope resolution, authorization check, approval
binding, expected-version check, idempotency decision, recorded write, recovery
state, and result classification stays in the Go core.

## 2. Process and transport

Routine call:

```text
LLM → OpenCode custom tool → `concord.ts`
    → spawn `concord invoke` with argv + JSON stdin
    → short-lived Go process validates/executes
    → one typed JSON result on stdout
    → adapter returns it to OpenCode
```

- Routine call: one short-lived CLI process and one logical core invocation.
- Transport: argv array plus JSON stdin/stdout; never shell interpolation, command
  strings, temporary argument files, or environment-variable payloads.
- Stdout: exactly one TS7 JSON envelope. Bounded diagnostics use stderr and cannot
  be mistaken for success.
- OpenCode abort terminates the child. The core still reports whether a committed or
  external effect occurred; cancellation never fabricates rollback.
- Caller budget passes unchanged. Unsupported budgets receive a typed core refusal.
- SQLite operations finish in-process. TS4 compaction may return one durable
  operation for later reconciliation.

No adapter-owned daemon, worker, database connection pool, HTTP server, IPC socket,
or background reconciliation loop exists.

### 2.1 Client registration

The adapter carries no bearer credential and runs no bootstrap handshake. Every
domain call is one short-lived `concord invoke`. The core authorizes each call
from the registered client policy and the repository the envelope names. It
refuses a client that is not active. Session and path fields carry no
authority of their own.

This protects the model/tool-call boundary and rejects unregistered clients. Concord's
local single-operator threat boundary still trusts the OS account, OpenCode process,
installed adapter; arbitrary code already controlling
that account/process is outside Concord's authority model.

## 3. Registration and schemas

The plugin entry registers custom-tool definitions from `concord.ts`, including:

```text
product_view      → concord_product_view
work_browse       → concord_work_browse
work_trace        → concord_work_trace
knowledge         → concord_knowledge
work_define       → concord_work_define
work_transition   → concord_work_transition
work_relate       → concord_work_relate
work_compact      → concord_work_compact
```

Descriptions and strict discriminated schemas are generated or verified from one
canonical contract shared with Go validators. TypeScript may perform structural
parsing required by OpenCode; it cannot maintain divergent semantic validation or
invent defaults absent from the core contract.

Contract-request authoring descriptions expose the accepted shape and a minimal illustrative example for each constrained field. The generator derives both from the canonical schema, including referenced and composed constraints, and validates each example against that field's effective schema. Examples are not defaults, real entity identities, approvals, or evidence, and do not promise admission by a pinned workflow or a live registry. No separately maintained field-specific format or example table owns this guidance.

Every model-visible Concord tool input exposes its enforced numeric, string-length, byte-length, collection-cardinality, and object-property-count minima and maxima in authoring descriptions before the first call. Guidance states each bound's value, count unit, and endpoint inclusion. It derives from the effective canonical contract, including references and composed constraints; branch-specific bounds remain labeled by branch. A host signature's omission of a schema keyword does not waive this obligation. The same schema-derived guidance mechanism serves contract requests and other tool inputs; no separately maintained field-specific bound table owns the descriptions. Publication and enforcing validators must agree on values and count units. Descriptions introduce no defaults, authority, evidence, or workflow admission.

The TS5 envelope is not model-visible. The adapter derives OpenCode `agent`,
`sessionID`, `messageID`, `directory`, and `worktree` from tool context and sends
them beside domain input. Core resolves stable Project/Product
identity and rejects untrusted/stale assertions.

The registered client key stays in the OS credential store and never enters
model-visible arguments, logs, artifacts, or tool output. Correctness never
depends on restoring adapter state.

## 4. Approval bridge

Consequential mutation flow:

1. A workflow read may expose a closed `operator_question`. The model calls the
   built-in question UI and supplies the selected semantic choice and its exact
   context digest to the public action. The Concord custom tool does not call the
   question service.
2. Adapter sends the canonical request with stable idempotency key. Core strictly
   validates the selection and digest before creating any approval challenge.
3. Core returns `approval_required` with challenge ID, canonical digest, pinned
   scope/versions, consequence summary, and approval policy. Workflow checkpoints
   also include work/action/contract version, selected choice, and bounded premise
   summary.
4. Only then does the adapter call current tool-context
   `ask({ permission, patterns, always, metadata })`, placing the exact checkpoint
   metadata in `metadata`.
5. Current `ask` returns `void` on approval and throws on rejection. It provides no
   typed user reply.
6. On rejection, adapter returns denied; no core mutation occurs.
7. On approval, adapter resubmits the identical request/challenge, asserting only
   that the host request resolved.
8. Core revalidates digest/scope/versions, derives approval-authority attribution
   from the trusted-client policy and exact consumed challenge/approval record,
   creates and consumes TS5's durable approval record, then executes or returns
   durable-operation state.

The adapter passes `always: []` for consequential calls; OpenCode cannot turn one
prompt into an unbounded Concord approval. The question is semantic input; `ask` is
authorization. `ask`, metadata, and the adapter are not approval authority. The
core is. A recorded operator means approval authority, not an identified human.

`ToolContext.ask` exists in current source but is not documented on OpenCode's
custom-tool/plugin pages. Release must pin OpenCode and verify its exact input shape,
void return, rejection behavior, and context field names. Missing/drifted behavior
fails consequential mutations closed—never approval prose or `approved: true`.

## 5. Adapter boundary

### Allowed

- register the generated current static tool set and strict schemas;
- capture documented OpenCode execution context;
- serialize one canonical request and parse one canonical response;
- call `ask` for one exact core challenge;
- propagate cancellation and caller budget;
- classify malformed/non-JSON CLI output as transport failure; and
- attach adapter/client identity for audit only.

### Forbidden

- open SQLite or git authority directly;
- derive Product scope or trust from path;
- authorize, approve, consume approval, or choose idempotency outcome;
- implement lifecycle, relation, membership, compaction, recovery, or native-system
  rules;
- cache Product/work state;
- rewrite typed core errors into guessed success/prose;
- retry mutation with a new idempotency key;
- execute GitHub/cloud/database/service-manager operations; or
- expose internal CLI commands as tools.

The adapter may retry one failed transport attempt only when no typed core response
was received, using the same request and idempotency key. Core decides whether the
retry returns a prior result, resumes a durable operation, or executes.

## 6. Plugin entry and transport module

`concord-plugin.ts` owns registration and the host hooks required by accepted
decisions. [CD-0090](./decisions/CD-0090-per-turn-continuity-re-pin-hook.md)
owns the continuity re-pin hook. `concord.ts` stays transport-only.

Native Task is the host's own surface. Concord adds no authorization,
packaging, completion readback or recovery bridge for it (CD-0242 D6).

## 7. Why not MCP

OpenCode supports local/remote MCP and automatically adds their tools. Its official
docs warn that MCP tools consume context and large sets can exceed context limits.

MCP is rejected for v1 because:

- no concrete second client exists;
- CD-0003 selects short-lived CLI and forbids a persistent MCP daemon/server;
- custom tools provide strict schemas and primary-client context;
- MCP adds process lifecycle, config, timeout, and context failure modes; and
- generic MCP auth does not replace TS5 scope, approval, or idempotency.

The JSON CLI contract is the stable internal client boundary. A future concrete
client may add another thin adapter. Persistent MCP requires TS8/TS9 comparison and
explicit CD-0003 D2 reconsideration. It must preserve the same eight semantics.

## 8. Alternatives

| Candidate | Decision |
|---|---|
| Agent calls CLI via `bash` | Rejected: loses schemas, hidden TS5 context, approval correlation, and selection reliability. |
| Thin plugin entry with shared `concord.ts` transport | **Selected:** generated tool registration and accepted host runtime hooks; domain authority stays in Go. |
| Eight tool files + shared library | Viable but rejected: more deployment/naming surface with no gain. |
| Local/remote MCP | Rejected for v1: no second client, extra context/lifecycle, no-daemon conflict. |
| TypeScript core/SQLite access | Rejected: second authority/validator. |
| FFI/WASM/native addon | Rejected: needless distribution/crash complexity. |

## 9. Evidence

- Custom tools: TS/JS definitions may invoke any language; Zod schemas; multiple
  exports; context includes agent/session/message/directory/worktree:
  <https://opencode.ai/docs/custom-tools/>.
- Plugins can register tools, receive project/directory/worktree, and expose host
  hooks: <https://opencode.ai/docs/plugins/>.
- Permissions use `allow|ask|deny`, input patterns, and per-agent overrides:
  <https://opencode.ai/docs/permissions/>.
- Current source wires the same `ask` into standalone/plugin tools. `ask` returns
  `void`; `metadata`, `abort`, and `ask` are undocumented on the user-facing pages:
  <https://github.com/anomalyco/opencode/blob/dev/packages/plugin/src/tool.ts> and
  <https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/tool/registry.ts>.
- MCP local/remote behavior and context caveat:
  <https://opencode.ai/docs/mcp-servers/>.

## 10. Implementation acceptance

Before release, prove:

- all eight names/schemas match canonical registry and Go validators;
- wrong-client, inactive-client, and wrong-worktree invocations fail closed;
  registered key rotation/revocation takes effect;
- directory/worktree/session/agent reaches core scope correctly;
- the registered client key never enters model args, logs, artifacts, or output;
- pinned `ask` shape, void/throw behavior, `messageID`, challenge approve/reject, and
  changed-input/version rejection;
- `always: []` cannot bypass TS5 policy;
- cancellation before/after commit reports authoritative outcome;
- malformed stdout/stderr, missing binary, timeout, and crash never report success;
- transport retry reuses idempotency key;
- adapter reload leaves no canonical state and the next call recovers; and
- measured spawn overhead stays within budget.

## 11. Falsifiers

Reopen TS6 when:

- pinned OpenCode cannot bind challenge safely through void-returning `ask`;
- custom-tool context cannot populate the TS5 envelope without model plumbing;
- adapter lifecycle/crash risk exceeds an alternative transport in measured runs;
- spawn overhead fires CD-0003 reversal;
- a concrete second client requires portable transport; or
- OpenCode adds a simpler external-binary tool adapter preserving schema, context,
  permission evidence, and cancellation.

Any replacement preserves one core authority, eight semantics, hidden verified
context, operation-bound approval, same-key retries, and native-system ownership.

## Acceptance criteria

- Given a registered client and an invocation envelope
  When the core authorizes the call
  Then the client and its key must both be active, and an unknown client,
  inactive key, or unresolvable worktree fails closed.

- Given a registered client key and its envelope
  When the model sends a tool call
  Then the client key and the TS5 envelope never enter model-visible
  arguments, logs, artifacts, or output.

- Given a CLI invocation that completes
  When stdout returns
  Then it carries exactly one TS7 envelope and malformed output classifies as
  a typed transport failure, never success.

- Given a failed transport attempt with no typed core response
  When the adapter retries
  Then it reuses the same request and idempotency key, and never retries a
  mutation with a new key.

- Given a core approval challenge
  When the host approves
  Then the adapter resubmits the identical request, the core revalidates
  digest, scope, and versions, and `always: []` keeps one host prompt from
  becoming an unbounded approval.

## Verification

The transport is exercised below scenario grain, so every criterion carries a
typed exemption in the record naming the test that proves the guarantee.

- Criterion 1 is proved by `TestCommandBoundaryRejectsInvalidTrailingJSONAcrossCommands`
  (`cmd/concord/main_test.go`) and `TestCLIEndToEndRegistersClientAndInvokesRead`
  (`cmd/concord/main_test.go`).
- Criterion 2 is proved by `TestInvokeRejectsUnknownFieldWithoutEcho`
  (`cmd/concord/main_test.go`) and `TestAuthorityRefusalsCarryTheUnauthorizedKind`
  (`internal/agent/authority_refusal_kind_test.go`).
- Criterion 3 is proved by `TestDecodeInvokeRequestRejectsInvalidTrailingJSON`
  (`internal/agent/runtime_test.go`) and the adapter contract tests
  (`adapter/opencode/generated-contract-tests.ts`).
- Criterion 4 is proved by `TestDispatchIdempotentReplaySurvivesAmbientScopeDrift`
  (`internal/agent/mutation_dispatch_test.go`).
- Criterion 5 is proved by `TestDispatchApprovalChallengeRoundTripIsDurableAndSingleUse`
  and `TestDispatchFailedDomainEffectRollsBackGrantAndApproval`
  (`internal/agent/mutation_dispatch_test.go`). Section 11 records the
  falsifiers for each guarantee.
