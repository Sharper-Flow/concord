# Instruction obligation inventory

This research appendix maps available instruction text, not Product law or an
adopted rewrite. Source rows and line ranges refer to commit
`3286386259ff489da83f0b1e486c63b106bfb0b3`; the
[main report](instruction-effectiveness.md) owns its per-file source sizes and
their limits. Host subgroups, provider hint, task prefix and individual
obligations are unmeasured.

The inventory has 76 rows: 59 for substantive obligations or coherent groups in
the role, eight repository shared instructions and repository guidance, and 17
for supplied host groups. Host entries summarize supplied context, not a pinned
public snapshot, and carry no private citation. The installed content and exact
serialized snapshot are not pinned.
Static reading establishes what text asks, not compliance, current law status or
a failure rate. No behavioral evaluation ran. Every disposition is a candidate,
and its stated effect is a **HYPOTHESIS**, not a measured benefit. Changes need
an obligation comparison, applicable authority and evidence before adoption.

## Reading the rows

The actor is the agent receiving the named surface unless a row names another.
Source links identify files; line ranges in rows identify the specific text.
`U` means enforcement **unknown**: no complete behavioral enforcement has been
established. `P` means **partial** enforcement of the specified boundary only;
`T` means **phrase-only** checks. No row claims full enforcement. All rows need
positive and negative behavioral evidence; the stated risk is a static evidence
need, not an observed incident, except where expressly identified as reported.

Case families are planned synthetic fixtures: REF refusal and effect states;
ASK required and unnecessary approval; AUTH operator, coordinator and retrigger;
REC record length, information and privacy; SKILL positive and negative trigger;
TASK ordinary completion and verification; HOLD resume and compaction; REPO
generated files, transactions and worktree. Family names identify proposed
coverage, not completed tests or a one-to-one mapping to the main report's 24
smoke cases. A future expansion must add distinct positive and negative cases
for each group and each branch, freeze fixtures and budget sessions separately.

### Enforcement references

- `E1`: [profile generator](../../../scripts/generate-agent-profiles.py#L16-L28)
  emits stash command-pattern denials and describes helper refusal boundaries;
  [projection](../../../scripts/generate-agent-profiles.py#L290-L304) appends
  denials. Pattern restrictions are not a sandbox.
- `E2`: [profile manifest](../../../contracts/agent-primary-profiles.v1.json#L244-L303)
  denies named workflow-writing tools and command patterns to the implement
  helper. That is not proof that all mutation routes are impossible.
- `E3`: [repository guidance checker](../../../scripts/check-agents-md.py#L130-L165)
  checks a line budget, certain backticked paths and exact CI command copies.
  The [checker description](../../../scripts/check-agents-md.py#L20-L46) reports
  four stale CI-command copies as its historical failure class; it does not
  establish instruction-following behavior.
- `E4`: [profile generator](../../../scripts/generate-agent-profiles.py#L337-L353)
  checks generated projection equality; [skill checks](../../../scripts/check-agent-contracts.py#L623-L719)
  inspect admitted inventory, frontmatter and references. Neither proves an
  agent selects a skill or refrains from hand-editing.
- `E5`: [primary prompt check](../../../scripts/check-primary-prompts.py#L152-L168)
  and [its marker checks](../../../scripts/check-primary-prompts.py#L379-L389)
  check selected phrases. Phrase presence is not behavioral enforcement.
- `E6`: [public content check](../../../scripts/check-public-content.py#L13-L30)
  matches selected private-content and credential patterns; it cannot recognize
  all private information. [Pre-push configuration](../../../lefthook.yml#L1-L23)
  describes conditional gates, not a mechanism that makes bypass impossible.
- `E7`: [transaction-scope check](../../../internal/store/txscope_test.go#L40-L83)
  scans selected production Go files for specified AST patterns; its
  [discovery scope](../../../internal/store/txscope_test.go#L185-L200) and
  [function scan](../../../internal/store/txscope_test.go#L283-L320) limit the
  claim. It is not a proof of every possible transaction call path.
- `E8`: [visual-approval contract tests](../../../scripts/test-agent-contracts.py#L737-L761)
  assert the asking clauses and two skill pointers by phrase. They do not show
  that an agent embeds screenshots or links a preview.

## Primary role

Source: [role](../../roles/concord.md). Lines below refer to that file.

| ID | Lines; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| R01 | 3–15: Primary owns delivery and durable state; optional RTE is unnecessary and sequences rather than decides. | U; ownership under delegation needs observation. | Keep; may preserve accountability. AUTH, TASK |
| R02 | 19–25: Only primary records transitions; subagent results do not record them. Use typed operations; unknown operations and refused writes get no alternate route. | P E2 for helper tool denials; no proof of all routes. | Keep; may prevent unauthorized state. AUTH, REF |
| R03 | 27–41: Use Linear MCP for planning; mark start In Progress, stopped work Todo with resume comment, PR wait In Review; Planner shapes, RTE corrects status without scope changes; record issue identity; no fallback authority. | U; exercise each status and missing access. | Keep; may preserve planning ownership. AUTH, HOLD |
| R04 | 45–60: Delegate through named helper, independent review exact subject; slice implementation, use design helper and skill for visual work, parallelize disjoint files only, serialize shared-worktree checks, final clean review. | T E5 delegation anchors; actual Task and review unproven. | Rewrite for precise routing; may reduce ambiguity. TASK, SKILL |
| R05 | 62–70: Inline fix is one file and at most five added or removed lines total; no splitting; after three fixes delegate; name helper or exception; insert verify helper for test-only work and CI triage; primary keeps plan, commits and records. | U; boundary counts need fixtures. | Keep; clarify host task-prefix applicability without relaxing limit. TASK, AUTH |
| R06 | 72–78: Record review with actual Task result; a tuple does not run review; intermediate repair may proceed, final review must be clean; bind ordinary evidence once without producer fields, use producer fields only for matching recorded evidence; CI external. | T E5 selected phrases; need producer and candidate checks. | Keep; may protect evidence integrity. TASK, AUTH |
| R07 | 82–87: Apply asking boundary to law consent; report excessive core consent requirement, follow recovery, never bypass or invent consent; unapproved enacted revision is derived, not reason to ask. | U; separate mismatch and approval cases. | Keep; may protect operator authority. ASK, REF |
| R08 | 91–106: Load shaping before execution; reuse readiness or resolve gaps; direct requests or RTE assignment start managed work unless explicitly outside, factual questions do not; host worktree is not authority; report state then shape; configuration-only capture needs route check; load self-repair for blocking own-work defects. | U; test positive and negative triggers. | Keep; may avoid accidental workflow capture. SKILL, AUTH |
| R09 | 108–119: Retrieve known targets directly; bounded discovery goes to explore, independent external investigation to lookup, with scope and cited deliverable; keep interactive exploration inline; surface evidence conflicts. | U; test known target versus discovery. | Rewrite; may improve routing without losing disagreement. TASK, SKILL |
| R10 | 121–130: State contract lock-in and commitments, record admitted action; seek only required consent, do not reopen settled matters; stop for contradictory evidence; load triggered shipped skill without granting authority. | U; conflict and non-trigger controls needed. | Keep; may prevent unapproved drift. AUTH, SKILL |
| R11 | 132–144: Capture out-of-contract requests as new work, return to current contract; keep session per repository; load multi-repository method when needed; resume with item-scoped full browse and trace, resolve ticket by key not title; host placement does not replace verification. | U; resume and multi-repository cases needed. | Keep; may preserve scope and identity. HOLD, SKILL |
| R12 | 148–160: RTE may assign and sequence, not approve, accept or publish; send start, publication/merge and actual blocker notices by reference; operator decides reserved matters; primary records delivery and resumes gate. | U; simulate unsupported approval and actual overlap. | Keep; may preserve decision authority. AUTH, HOLD |

## Shared instructions

### Voice

Source: [voice](../../instructions/voice.md), lines 3–24.

| ID | Lines; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| V01 | 3–8: Write plain short technical English, specific consistent names, same-sentence pronoun antecedents, active verbs and actors. | U; assess readability and exceptions in real tasks. | Merge with host voice per receiving context; may reduce duplication without depriving other Products. REC, TASK |
| V02 | 10–24: Lead with outcome, match length and artifact, avoid narration and filler, report uncomfortable risks, cost and errors plainly. | U; assess required detail and honesty, not just brevity. | Merge with host voice per context; may improve consistent answers. REC, TASK |

### Asking

Source: [asking](../../instructions/asking.md).

| ID | Lines; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| A01 | 3–14: Ask one question and await answer. Ticket-triggered work or an operator instruction to use the ticket frontend uses linked issue and ticket method; directly driven work uses pane; explicit instruction or qualifying RTE direction switches route; retrigger is not consent. | U; separate route, switch, unanswered retrigger. | Rewrite host applicability, retain each branch; may prevent wrong-channel asks. ASK, AUTH, HOLD |
| A02 | 16–28: Ticket question keeps decision, recommendation, options and consequences visible; every question stands alone, uses plain language and balanced context. | U; inspect visible and collapsed text. | Merge record formatting pointers; may improve clarity. REC, ASK |
| A03 | 30–43: Ask only consequential unresolved questions after investigation; do not ask for known facts or agreed work; apply and record settled rules, retaining gates; consult vision before direction decisions and cite it when decisive. | U; compare genuine fork with lookup and settled cases. | Keep; may reduce unnecessary questions. ASK, TASK |
| A04 | 45–54: Operator directly approves product direction, UI/UX, production application data, security, increased spending and legal obligations; ordinary coordination, existing paid service, routine preferences, minor UI corrections and restoring security alone do not independently require approval; other classes still apply; exact approval-boundary changes need approval; apply Concord deletion exemption. | U; test each class, exception and overlapping class. | Keep inline; may preserve consent without gratuitous asks. ASK, AUTH |
| A05 | 56–58, 66–72: Preview approval shows inspectable version and records answer; approval questions name class or operator-only reservation and concrete consequence; law status, seriousness, size, Domain count and wording alone do not require approval; seek applicable law-change approval before implementation, not at end. | U; separate reservation, timing and non-approval controls. | Keep; may prevent late or invented approval. ASK, AUTH |
| A06 | 74–85: Give recommendation and material risks; explain permanent loss and missing recovery; never risk irreversible customer-data loss without informed approval; otherwise decide in scope, record reason, use advisor and external review, coordinate without treating RTE or Planner as authority; obey blockers and recovery. | U; exercise loss, no-class decision and refusal. | Keep; may preserve informed consent and safe autonomy. ASK, REF, AUTH |
| A07 | 60–64: For every Linear UI/UX approval comment, embed screenshots of the exact material and version; link the running preview when the operator can reach it. Screenshots only in issue Resources or a link alone do not suffice; use synthetic or redacted material without credentials or private details. | T E8 selected phrases and pointers; behavior unproven. | Keep; may preserve inspectable, safe approval evidence. ASK, REC |

Planned A07 extension, not included in the 24-case pilot: in a synthetic UI
approval fixture with a reachable preview, the positive case embeds screenshots
of the exact version in the approval comment and links that preview. Negative
cases give only a preview link, screenshots only in Resources, or a mismatched
version; the grader rejects each. An unreachable-preview control still requires
the embedded screenshots, and all fixture material is synthetic or redacted.

### Evidence

Source: [evidence](../../instructions/evidence.md).

| ID | Lines; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| E01 | 3–13: Establish actual behavior before assertions or changes; distinguish declaration from effect and trace to implementation; interrogate primary sources rather than memory. | T E5 selected markers; behavior unproven. | Keep; may reduce unsupported claims. TASK |
| E02 | 15–23: Resolve law context before relying on law or delegating law-dependent work; resolve delegate gaps; compare documentation with reachable consumer code and surface disagreement. | T E5 one law-context marker; actual resolution unproven. | Keep; may avoid treating branch prose as accepted law. AUTH, TASK |
| E03 | 25–36: Report unknowns and blocked lookups; brevity never lowers evidence bar; choose source appropriate to technical uncertainty; correct errors in place. | T E5 selected markers; verify uncertain and incorrect answers. | Keep; may prevent false confidence. TASK, REC |

### Records

Source: [records](../../instructions/records.md).

| ID | Lines; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| D01 | 3–17: Put facts with their owner, link elsewhere, retain decisions, evidence, risk and scope in work record, not progress transcripts; preserve required workflow and ticket events; exclude private data and inaccessible citations from public content; external planning owns backlogs. | P E6 selected patterns only; privacy and ownership require review. | Keep privacy; merge progress guidance; may reduce duplication. REC |
| D02 | 19–30: New or rewritten issue/work item states observed problem, affected party and checkable end state before plan; defects describe experienced failure; issue holds current scope and decisions, comment one event; PR and completion list law change, meaning and reason; delegation prompt carries complete task brief. | U; check defect, law-change and delegation records. | Merge brief references; may reduce drift. REC, AUTH |
| D03 | 32–42: For new Linear records count all visible prose including after collapses; aim at about 600 characters, lead with outcome, one event heading, short bullets, tables only for comparisons, selective bold and copyable code, omit redundant material. | U; no hard 600-character refusal established. | Replace with tool-boundary warning only after equivalence check of count, links and information; may aid authors without arbitrary rejection. REC |
| D04 | 44–56: Preserve exact approval and attribution; settled decisions not transcripts; quote operator answers in Concord as law requires, not ordinary answers verbatim in Linear; use scannable Agent detail collapse and links; do not rewrite old comments merely for style; one event per record, keep required ticket events. | U; inspect exact quotes and collapse boundaries. | Merge repeated quote and progress guidance; may preserve meaning with fewer copies. REC, AUTH |
| D05 | 58–64: Keep unresolved questions, limitations, merged/deployed/verified distinction, exact approvals, timers, notice owners and safe resume accessible; show question decision and consequence outside collapse; include reproduction and diagnostic link when useful. | U; test omitted-risk and visible-question cases. | Keep; may preserve actionable context. REC, HOLD |
| D06 | 68–81: Assigning agent puts complete brief in spawn, not Linear, and maintains managed durable assignment; at launch/retrigger/restart/reset/RTE succession read captured task, full item browse, resume and ticket events; tab and work record identify owner without assignment comments; evidence belongs in linked delivery records; report absent pre-capture carrier. | U; distinguish brief locations and every resume trigger. | Merge repeated brief/resume references, preserve branches; may reduce drift. HOLD, REC |
| D07 | 83–92: Link blockers and unblock conditions in Linear; put drain acknowledgements and routine coordination in coordination records; research lives in knowledge home, Linear Document copy only on request; concise linked readiness/completion; ordinary progress comments rare, ticket events still required. | U; evaluate research placement and required-event exceptions. | Merge progress instructions; may prevent duplicate records. REC |
| D08 | 94–103: Name openable artifacts, state observations before interpretations, aim below forty words per sentence, record cost/risk/errors, preserve quoted text exactly. | U; exact quotation and useful detail need review. | Move occasional writing detail to triggered method with pointer; may reduce common context. REC, SKILL |
| D09 | 105–116: Load ticket writing for issue/comment/work item, project updates for project updates, PR writing for PR/completion, research writing for research, handoff writing for briefs. | P E4 validates admitted skill inventory, not invocation. | Keep trigger list; may preserve method recall. SKILL, REC |
| D10 | 118–136: Record actionable recurring costs once with surface, evidence, impact, workaround and scope; separate observation from cause; no invented savings or log for resolved incidental friction; surface blockers and material risks, carry unresolved findings in handoff; file out-of-scope defect follow-up without widening work or granting authority. | U; compare incidental friction, actionable defect and unsafe scope expansion. | Move detailed reporting recipe to triggered method, retain boundaries; may reduce irrelevant context. REC, TASK |

### Change

Source: [change](../../instructions/change.md).

| ID | Lines; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| C01 | 3–11: Fix the faulty mechanism, not a wrapper or extra case; before extending an inherited mechanism establish purpose and consequence of absence, otherwise report the finding. | U; compare cause repair with workaround. | Keep; may reduce structural debt. TASK |
| C02 | 13–25: Retire what approved work replaces, including governing law through its route, tests and coverage with retiring clause; retain current proof or replacement; no compatibility hiding or copied implementation. Concord-only D3 permits qualifying retirement without per-deletion approval, with reason, review, CI, exclusions and unchanged outcome. | U; distinguish Concord from other Products, exclusions and retained gates. | Keep; may prevent unauthorized or incomplete retirement. ASK, TASK |
| C03 | 27–32: Choose dependencies by total cost and maintenance; apply project restrictions and security, legal and spending boundaries. | U; compare suitable dependency with prohibited change. | Keep; may prevent false economy. ASK, REPO |
| C04 | 34–42: Delete obsolete historical markers but retain material still governing a decision; never delete protections or failing tests merely for size; cite retirement clause and review coverage for every test deletion. | U; distinguish retired behavior from current protection. | Keep; may preserve evidence. TASK |
| C05 | 44–45: Never stash; permitted committers save work on item branch. | P E1 command patterns; alternate routes remain possible. | Keep; may protect shared worktrees. REPO |
| C06 | 47–60: Committed comments and documents carry durable intent and constraints, not provenance, diff labels or conversation history; provenance belongs in commits, PRs and work records. Remove change-created bloat, file out-of-scope follow-up rather than expand; prefer clarity and necessary structure without speculative building. | U; review artifacts and scope. | Rewrite for concise boundaries; may reduce incidental expansion. REC, TASK |

### Completion

Source: [completion](../../instructions/completion.md).

| ID | Lines; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| F01 | 3–16: Completion requires verified end state, acceptance and explained failures; choose narrow checks for risks, widen only when needed; reuse evidence only while inputs and required producer/subject remain valid. | U; test stale evidence and uncovered risk. | Keep; may reduce false completion and redundant checks. TASK |
| F02 | 18–27: Before push run declared checks through host admission; installed pre-push hook is owner, documentation substitutes only without hook; failure stops push, report missing declared checks; stop after acceptance and adequate risk evidence. | P E6 conditional hook configuration, not unbypassability. | Keep; may preserve delivery gate. REPO, TASK |
| F03 | 29–36: Regression test exercises fix and fails before it; removal proof is deletion, build, reachability and review; test only retained old-boundary behavior, not mere absence; non-executable changes use relevant validation and review. | U; distinguish fix, removal and prose change. | Keep; may match proof to behavior. TASK |
| F04 | 38–53: Investigate failures before compensation; classify many broken tests against accepted retiring clauses before repair or delegation; report unfinished work and gaps; never weaken a check. | U; exercise unexpected failure and retired/current batches. | Keep; may prevent hidden regressions. TASK |

### Continuation

Source: [continuation](../../instructions/continuation.md).

| ID | Lines; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| N01 | 3–14: Continue agreed permitted work; progress is not a blocker or reason to end turn; stop at decision, refusal or end state; ticket question produces durable stop and requires ticket method. | U; separate progress, real blocker and ticket hold. | Keep; may reduce premature stops. HOLD, SKILL |
| N02 | 16–27: Context size alone is not a stop; preserve work, reread durable task/resume/ticket events on launch, retrigger, restart or reset; report missing carrier, never treat summary as authority; delegate external wait and act on result, no new decision merely for passed check. | U; simulate compaction and pending check. | Merge resume pointers without losing triggers; may improve recovery. HOLD |
| N03 | 29–37: No second policy, mutation, approval or evidence route; reuse approval only for unchanged task/scope/consequence, new consequence or scope needs decision; check declared routes before claiming unavailability. | U; distinguish same-scope bookkeeping failure and changed scope. | Keep; may avoid duplicate approvals and invented routes. AUTH, REF |
| N04 | 39–49: Refusal stops that route; follow admitted recovery. For no effect, correct and resend only within unchanged authority and scope, without replacing approval. For possible or unknown effect reconcile before resend; later no-effect refusal does not clear uncertainty. Never repeat identical refusal, bypass, invent approval, split or change arguments to evade denial, or report no effect before reconciliation. | P E2 restricts helper tool routes only; uncertainty handling unproven. | Keep inline; may prevent duplication and unauthorized action. REF, HOLD |
| N05 | 51–60: Without admitted recovery report failed action, effect and exact operator action; continue other admitted work; state authority limit or maintainer follow-up if operator cannot help; recommend restart only for evidenced reload; retain refusal, scope and approval gates, no simulated restart. | U; test no recovery, unrelated work and no-help branch. | Keep; may make safe recovery actionable. REF, HOLD |

### Repository mode

Source: [repository mode](../../instructions/repository-mode.md), lines 3–16.

| ID | Lines; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| M01 | 3–8: Before stage, commit or push resolve the Project mode; on mode treats Concord and agent files as ordinary Product files. | U; check before each publication action. | Keep; may avoid incorrect exclusions. REPO |
| M02 | 10–16: Off or unresolved mode forbids staging, committing or pushing Concord/agent files and undoing local exclusion; off mode has no Product law unless operator designates separate knowledge home; otherwise follow shared and repository instructions. | U; test off, unresolved and designated-home branches. | Keep; may protect repository boundary. REPO, AUTH |

## Repository guidance

Source: [AGENTS.md](../../../AGENTS.md).

| ID | Lines; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| G01 | 11–17, 62–66: Linear MCP owns planning, PR/checks own review evidence, accepted law owns Product obligations, ordinary prose does not; surface decision conflicts and never narrow contracts; law requires default-branch law folder. | P E3 structural guidance checks only; resolved law status still needed. | Merge duplicated authority paragraphs without losing default-branch condition; may reduce competing formulations. AUTH |
| G02 | 18–28: One host-created branch/worktree per change, never implement on main; use Concord workflow for new development, respect underway/repair routes; Build fallback only for named defective profile/prompt/plugin cases, no workflow writes for repair and name D2 in PR. | U; test each fallback and unauthorized default branch. | Keep; may protect isolation and workflow authority. REPO, AUTH |
| G03 | 32–37: Regenerate generated files from owner rather than hand-edit, including frontmatter-only agent definitions; releases automated, no hand-tag or manual release. | P E4 checks projection drift; does not prevent edits or manual release. | Keep; may preserve provenance. REPO |
| G04 | 38–48: Do not add unscoped Go dependencies, framework abstractions or runtime behavior; dependency changes need approval, temporary pinned host typecheck install is not repository dependency change and must run without skip; do not populate reserved boundaries; normal push runs hook, fix failures, install missing hook. | P E6 hook applies conditional gates; other behavior unknown. | Keep; may preserve scope and checks. ASK, REPO, TASK |
| G05 | 49–52: Never put personal paths, machines, private networks, service addresses, environment paths, logs or private history in repository, even temporarily; use synthetic/public sources. | P E6 detects selected shapes, not all private information. | Keep; may prevent disclosure. REC, REPO |
| G06 | 54–60: Inside a transaction do not call pooled Store methods; use transaction-scoped core to avoid single-connection deadlock. | P E7 AST scope and patterns only. | Keep until equivalent coverage proven; may prevent deadlock. REPO |
| G07 | 68–73: Use pinned Go, package-local ownership, structural invariants, typed failures, transactions and deterministic tests; commit titles affect release version. | U; style and title behavior require separate checks. | Move occasional style detail to appropriate method with pointer; may reduce routine context. TASK, REPO |
| G08 | 75–91: Use referenced sources for CLI, CI, law, tests, tooling, adapter, generation, releases, coverage, documentation and architecture. | P E3 checks certain backticked paths and budget; does not validate every claim. | Replace repeated deterministic lookup recipes with existing pointers only after equivalent retrieval verified; may reduce drift. TASK |

## Host and injected groups

These summaries are descriptive of the supplied host instruction context, not a
publicly pinned snapshot. Each subgroup is unmeasured. `U` reflects the absence
of inspected host enforcement, not proof that none exists.

| ID | Host surface; trigger and intended behavior | Enforcement; static evidence need | Disposition; HYPOTHESIS; cases |
|---|---|---|---|
| H01 | Global AGENTS: never commit secrets; after tool/shell config changes back up non-secret current state and update manifest for new surfaces; exclude secrets and generated dependencies. | U; exercise backup and secret exclusions. | Keep safety, move rare backup recipe to triggered method if available; may reduce irrelevant context. REC, REPO |
| H02 | Global AGENTS: validate Vision config before reload; MCP onboarding distinguishes connection types and uses file-based header secret injection, not environment interpolation. | U; validate sequencing and secret handling. | Move rare onboarding recipe to method, keep inline secret safety; may reduce context without weakening protections. SKILL, REPO |
| H03 | Global AGENTS: edit deployed scripts only in development source then use matching deployment; never patch or rebuild OpenCode core absent permitted exceptional route and ADR. | U; distinguish source from installed artifact and exceptional core case. | Keep; may protect installed state. REPO |
| H04 | Global AGENTS: present machine-specific approved preview endpoint, configure allowed host and restart when host check blocks it. | U; verify environment-specific recipe without exposing address publicly. | Move preview recipe to triggered method; may reduce irrelevant context. SKILL, TASK |
| H05 | Global AGENTS: respect ADR do-not-revive restrictions on public-repository search replacements, generic TUI exit cleanup and per-project database workarounds, including stated exceptions. | U; compare restricted paths and exceptions. | Keep safety boundary; may prevent recurrence. REPO, TASK |
| H06 | lgrep: use specific local-discovery tool first, retrieve snippets and symbols through declared interfaces, index on stated error; fallback only for specified regex, timeout or unavailable-tool cases; no shell code search. | U; test known target, timeout retry and fallback. | Rewrite as concise routing plus method pointer; may improve tool selection. TASK |
| H07 | oc-test-gate: route tests through repository admission, otherwise cap Vitest workers; for eligible long commands use managed run with real issue key/UUID and explicit budget, notification not polling, no handmade detachment. | U; separate synchronous, eligible long-run and missing identity. | Keep admission boundary, move recipe to method; may preserve resource safety. TASK, REPO |
| H08 | Code Mode: MCP tools via interpreter discovery and exact returned signatures; built-ins and plugin tools remain top-level; use direct top-level MCP route only when Code Mode is off. | U; test discovery, missing namespace and mode distinction. | Rewrite; may prevent invalid tool calls. TASK |
| H09 | Trunk/worktree: keep trunk on default, host creates isolated worktree, task belongs to worktree; restart only for reload, deployment from merged default; publication/merge follows role scope, hook and CI; remove only after merge, respect foreign default trunks. | U; check independent creation, publication, merge, deployment and deletion boundaries. | Keep boundaries, move detailed procedures to method; may improve retrieval. REPO, HOLD |
| H10 | Freshness: sync before deploy, first review request or unreviewed push, never reviewed head; no automatic conflict resolution, autostash or force push. | U; compare reviewed and unreviewed head. | Keep; may prevent history damage. REPO |
| H11 | Link references: cite address on first mention for addressable evidence, exact paths/lines, run/job URLs and commit identity; obtain addresses from owner, do not fabricate; public artifacts exclude private addresses. | U; inspect link targets and public/private split. | Merge with records link guidance per context; may improve traceability. REC |
| H12 | Operator question format: rank non-approval choices and act; blocking question uses question tool when applicable, otherwise chat fallback; subagent returns blocker to parent; prescribe one question, marker, options and closing status. | U; ticket route conflict and subagent branch need fixtures. | Rewrite applicability to respect ticket route, keep operator gates; may avoid conflicting channels. ASK, AUTH |
| H13 | Host coordination: RTE loads role/session methods; primary loads session method; Task is not Herdr participant and returns only to parent; assignment briefs stay out of issue; notices use prescribed one-line protocol, link evidence, reconcile uncertain sends. | U; positive RTE and negative Task cases needed. | Move mechanics to triggered session method, retain role boundary; may reduce irrelevant context. AUTH, SKILL |
| H14 | Host coordination: Item owners notify the RTE about actual start, merge/publication, blockers and friction when assigned; facts do not grant authority; verify claimed operator approval through linked operator record, do not treat restart as approval; reviews use prescribed review role and caps. | U; simulate absent operator evidence, friction and review limits. | Keep authority inline, move notice recipe to method; may preserve decisions. AUTH, HOLD |
| H15 | Injected global voice: concise, outcome-first, specific active prose, one fact once, links at claims, no tables to hide prose, closing divider; preserve evidence, safety and artifact formats. | U; compare with shipped voice and non-Concord contexts. | Merge per receiving context, never remove only voice copy another Product receives; may reduce duplication. REC, TASK |
| H16 | Conditional provider hint: generic tool-routing reminders apply only when the host injects the hint; actual configured and serving models and text are not pinned here. | U; need serialized positive and absent-hint snapshots. | Cut generic restatements only after snapshot and equivalence review; may reduce duplicate context. TASK |
| H17 | Task prefix: host generally delegates complex implementation/review/investigation while allowing small changes and known lookups inline; assigned helper role, scoped brief and host permissions govern. | U; compare generic few-lines wording with role's exact one-file/five-line/three-fix bound. | Rewrite applicability and role override, preserve narrower role bound; may reduce ambiguity. TASK, AUTH |

## Evidence and adoption limits

The row inventory covers the available semantic surfaces, not a claim that the
supplied host and runtime text forms a complete or reproducible prompt snapshot.
A07 shows that runtime and repository content can drift; installed content
remains unpinned.
No per-rule bytes, agent adherence, prompt effectiveness, savings, or replacement
enforcement were measured. `U` is not evidence that enforcement is absent.
Before changing an instruction, inspect its actual consumer and applicable law,
compare every branch, establish permission and review, then test the candidate
against independently scored positive and negative cases. The main report's
24-case smoke design cannot independently cover every obligation above; a wider
case expansion is planned, not executed or included in that pilot.
