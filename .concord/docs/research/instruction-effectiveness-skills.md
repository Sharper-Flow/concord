# Skill description inventory

This appendix asks which skill descriptions the primary prompt exposed, when an
agent should select each one, and what a change to exposure might improve. The
inventory covers the 48 local descriptions and the built-in description exposed
in an observed primary session on 2026-11-10: 22 Concord, 26 host and one
built-in. It does not inventory skills absent from that prompt. The
[companion research](instruction-effectiveness.md) owns the wider analysis.

## Evidence and limits

The primary source for every row is the available-skills metadata in that
session. Concord's public skill sources live under `skills/<name>/SKILL.md`;
the links below identify their owning sources, not proof that installed bytes
match repository bytes. For example, the public [adoption](../../../skills/concord-adoption/SKILL.md)
and [research-writing](../../../skills/concord-research-writing/SKILL.md)
frontmatter (lines 1–4) carries a name and description. Host descriptions are identified
only by name and purpose; the host source snapshots are not publicly
reproducible. The built-in source is not available here. No host file paths,
configuration or logs are reproduced.

**Size:** the table below counts Unicode code points in the description strings
exposed in that session's available-skills metadata, including a trailing
newline where the exposed string carries one. The 49 descriptions total
**12,741 code points**. These are catalog-metadata counts, not serialized prompt
measurements, byte counts or tokens. The baseline prompt cost remains unknown:
host catalogs and serialization can differ. A future evaluation must capture
the exact sanitized serialized catalog, version, model, tokenizer and exposure
context before estimating prompt cost or comparing variants.

**Enforcement:** the session catalog exposes each name and description, and the
skill tool can load a named method. Public frontmatter establishes metadata
shape for the two inspected examples. Neither catalog presence nor frontmatter
validity proves that an agent selects or loads a skill, that the full method is
followed, or that a particular exclusion is enforced. Per-skill behavioral
enforcement is **unverified** throughout this inventory. Several exclusions
below are proposed routing boundaries inferred from the trigger, not claims
that current text explicitly enforces them. An inferred exclusion never
overrides a mandatory load that a role or instruction states. The built-in
descriptor explicitly excludes application code and projects not configuring
OpenCode.

**Reading the rows:** IDs are stable within this appendix. Each row names the
trigger and a distinct exclusion, then one positive and one negative synthetic
routing case. “Keep”, “trim”, “move exposure” and “clarify” are hypotheses,
not approved edits. Each proposed effect needs observed selection and task
outcome evidence; no evaluation has been run. Approval-bearing and safety
triggers remain intact unless an evaluation proves an alternative equivalent.
Similarity of names does not establish interchangeability. In particular,
instruction-writing owns the instruction method, writing-for-agents concerns
agent-facing documents, prose-style concerns human-facing prose, and
antislop-code concerns comments only; test their overlap before consolidation.

## Concord skills

Sources in this section are public repository skill files; the current exposed
descriptions are session metadata. The size table and enforcement status apply
to every row.

| ID and exact name / owner | Trigger; exclusion | Positive / negative routing case | Proposal and hypothesis to evaluate |
|---|---|---|---|
| K01 [concord-adoption](../../../skills/concord-adoption/SKILL.md) | Adopt an existing repository's knowledge; not routine work in an already adopted project. | Adopt a repository with decisions / fix an existing endpoint. | Keep: explicit adoption scope may prevent treating ordinary edits as adoption. Measure correct selection. |
| K02 [concord-architecture-writing](../../../skills/concord-architecture-writing/SKILL.md) | Shape architecture references, glossary or architectural plan outputs; not a routine code-only patch. | Draft a Domain reference / rename a local variable. | Keep: distinct knowledge placement cues may improve routing. Measure architecture and code-only cases. |
| K03 [concord-codebase-design](../../../skills/concord-codebase-design/SKILL.md) | Design module interfaces or repository checks; not an unchanged internal implementation. | Add a check / change a literal without changing a seam. | Keep: check-design cue may prevent shallow validation. Measure both check and non-seam selection. |
| K04 [concord-diagnosis](../../../skills/concord-diagnosis/SKILL.md) | Diagnose an unproven defect or regression; not a known, verified cause. | Investigate an unexplained timeout / apply a verified spelling correction. | Keep: causal uncertainty cue may reduce premature fixes. Measure selection before diagnosis. |
| K05 [concord-diagram-writing](../../../skills/concord-diagram-writing/SKILL.md) | Decide on or write a diagram; not prose without a visual question. | Review a Mermaid flow / correct a typo in prose. | Keep: diagram-specific cue may improve rendering review. Measure both cases. |
| K06 [concord-handoff-writing](../../../skills/concord-handoff-writing/SKILL.md) | Write an assignment or handoff; not a research finding. | Draft a delegation brief / record an experiment result. | Keep: recipient and scope cues may improve handoffs. Measure routing separately from research. |
| K07 [concord-improve-codebase-architecture](../../../skills/concord-improve-codebase-architecture/SKILL.md) | Scan and rank architectural improvement candidates; not implement a scoped fix. | Rank shallow-module candidates / fix a named bug. | Keep: scan-versus-implementation distinction may limit scope drift. Measure false positives. |
| K08 [concord-instruction-writing](../../../skills/concord-instruction-writing/SKILL.md) | Edit agent instructions, profiles, skills or their source; not ordinary human-facing prose. | Revise a role profile / edit a customer essay. | Clarify overlap with writing-for-agents and prose-style, without deletion: method ownership may become clearer. Measure joint selection and omissions. |
| K09 [concord-law-writing](../../../skills/concord-law-writing/SKILL.md) | Place Product knowledge or draft and prune law; not generic code-only work. | Place a Product research note / refactor a private helper. | Keep: preserve authority and approval cues. Measure knowledge-placement selection and approval preservation. |
| K10 [concord-multi-repository](../../../skills/concord-multi-repository/SKILL.md) | Work must edit a second Project in another repository; not multiple directories in one repository. | Coordinate an edit in another Project / edit two packages in one repo. | Keep: boundary cue may prevent wrong-worktree edits. Measure both routes. |
| K11 [concord-pr-writing](../../../skills/concord-pr-writing/SKILL.md) | Write a PR body or completion report; not a task's implementation. | Draft a completion report / change an API function. | Keep: delivery-record cue may improve evidence reporting. Measure report selection. |
| K12 [concord-project-updates](../../../skills/concord-project-updates/SKILL.md) | Write a Linear project description or status update; not a single issue comment. | Update project status / comment on one issue. | Keep: project-versus-issue distinction may avoid wrong record form. Measure both. |
| K13 [concord-readme-writing](../../../skills/concord-readme-writing/SKILL.md) | Create or improve a README; not any Markdown file. | Rewrite a README / amend a research note. | Keep: audience-specific cue may improve README decisions. Measure false positives on Markdown. |
| K14 [concord-research-writing](../../../skills/concord-research-writing/SKILL.md) | Write research findings, notes or briefings; not a delivery-only status. | Write a cited findings note / post a completion status. | Keep: citation cue may improve traceability. Measure selection and source support. |
| K15 [concord-retrospective](../../../skills/concord-retrospective/SKILL.md) | Analyze recurring session friction or publish a Product lesson; not a one-off routine progress update. | Find recurring coordination costs / report one completed task. | Keep: recurrence and lesson cues may limit noise. Measure selection and actionable findings. |
| K16 [concord-review](../../../skills/concord-review/SKILL.md) | Review implementation or delivery against standards and outcomes; not implement the change itself. | Independently review a branch / implement an assigned patch. | Keep: review-stage cue may preserve independent assessment. Measure review selection. |
| K17 [concord-self-repair](../../../skills/concord-self-repair/SKILL.md) | Concord's own defect blocks safe managed work; not an unrelated product defect. | Repair a workflow blocker / fix a user-facing application typo. | Keep: retain authorization boundary. Measure selection and required repair route. |
| K18 [concord-test-design](../../../skills/concord-test-design/SKILL.md) | Choose, add or assess tests and their seam; not answer a question with no test decision. | Design a regression test / locate a known file. | Keep: seam and evidence cues may improve test fit. Measure selection and assertions. |
| K19 [concord-ticket-frontend](../../../skills/concord-ticket-frontend/SKILL.md) | Ask or resume work driven by the ticket frontend; not a directly driven session absent a route switch. | Resume after a ticket answer / answer a direct chat lookup. | Keep: retain durable-stop and answer-authority cues. Measure route and resume compliance. |
| K20 [concord-ticket-writing](../../../skills/concord-ticket-writing/SKILL.md) | Write a Linear issue, comment or captured work item; not a project status update. | Draft a defect issue / update a project description. | Keep: record-type cue may improve defect evidence. Measure issue-versus-project routing. |
| K21 [concord-ui-design](../../../skills/concord-ui-design/SKILL.md) | Make or review a visual UI or UX change or preview; not a nonvisual backend-only change. | Prepare a new screen preview / optimize a database query. | Keep: preserve accessibility and visual approval cues. Measure approval and preview behavior. |
| K22 [concord-work-shaping](../../../skills/concord-work-shaping/SKILL.md) | Triage and shape work before refinement, allocation or implementation, reusing recorded readiness when scope is unchanged. The [primary role](../../roles/concord.md) (lines 91–92) requires loading it before execution, including for an already scoped item; the inferred exclusion is an ordinary factual question that requests no work (role lines 102–103). | Start implementing an already shaped item and reuse its readiness / answer a factual question about a known file. | Keep: lifecycle cue may avoid both skipped shaping and repeated shaping. Measure that scoped execution loads the method and reuses readiness rather than reshaping. |

## Host skills

The owner in every row is the host skill catalog, except where a row proposes
project-scoped exposure. Descriptions and routing examples are sanitized; their
host snapshots are not public. The size table and enforcement status apply to
every row. H11 uses a sanitized identifier in place of its host catalog name;
its size counts the original exposed description, not the public summary here.

| ID and exact name / owner | Trigger; exclusion | Positive / negative routing case | Proposal and hypothesis to evaluate |
|---|---|---|---|
| H01 antislop-code — host | Remove generic code comments without editing code; not general prose editing. | Prune redundant comments / rewrite a README paragraph. | Clarify relationship to instruction-writing and prose-style: comment-only scope may prevent accidental code edits. Measure both. |
| H02 bash-defensive-patterns — host | Write robust Bash or shell CI; not a Go-only edit. | Add a fault-tolerant shell script / rename a Go field. | Keep: shell-specific failure cues may improve scripts. Measure applicable selection. |
| H03 duckdb-query — host | Query an attached database or file via DuckDB; not edit application source. | Aggregate a CSV with SQL / refactor a service. | Keep: database cue may reduce wrong tool selection. Measure SQL and code-edit routing. |
| H04 duckdb-read-file — host | Inspect supported data files or remote data; explicitly not source code. | Preview a Parquet dataset / inspect a Python module. | Keep: explicit source-code exclusion may avoid misrouting. Measure both cases. |
| H05 firecrawl-cli — host | Install, authenticate or troubleshoot the Firecrawl CLI; not ordinary web research. | Diagnose CLI authentication / read a public documentation page. | Keep: operational cue may prevent unnecessary setup. Measure selection. |
| H06 herdr-sessions — host | Manage host session mechanics for a primary or assigned RTE; not a Task subagent's ordinary report. | Transfer an owning session / return a subagent result. | Keep: role boundary may prevent inappropriate coordination. Measure role-sensitive routing. |
| H07 linear-mcp — host | Look up or update Linear planning through MCP; not local code search. | Find a Linear issue / find a local function. | Keep: service-specific cue may select the right interface. Measure both. |
| H08 oc-runbook-ci — host | Need exact CI-wait or test-throttle recipes; not a check-free prose lookup. | Determine test admission syntax / read a known document. | Keep: command-syntax cue may reduce unsafe test execution. Measure selection. |
| H09 oc-runbook-git — host | Need worktree or freshness procedure; not ordinary code navigation. | Determine worktree setup / inspect a known function. | Keep: host-boundary cue may prevent wrong-worktree actions. Measure selection. |
| H10 playwright-mcp — host | Browser-driven UI verification through Playwright MCP; not web research. | Verify a rendered form / research a public standard. | Keep: browser-versus-research boundary may avoid wrong tooling. Measure both. |
| H11 project-specific development authentication (host catalog identifier withheld) — host, project-specific | Verify authenticated routes or APIs in the owning development project; not another project's authentication. | Verify that project's signed-in route / test another product's login. | Move exposure to the owning project while preserving access there: may reduce irrelevant catalog competition. Measure both projects' routing and access before rollout. |
| H12 prose-style — host | Polish prose for human readers; not code behavior. | Edit an essay / change a parser. | Clarify overlap with instruction-writing, writing-for-agents and antislop-code: audience cues may avoid conflicting methods. Measure simultaneous selection. |
| H13 rte-role — host | Start assigned RTE duty or reload its instructions; not an ordinary primary or subagent. | Begin assigned coordination duty / execute a bounded subtask. | Keep: role cue may prevent authority confusion. Measure role-sensitive selection. |
| H14 shellcheck-configuration — host | Configure or use ShellCheck for shell analysis; not a general Python lint configuration. | Configure ShellCheck / configure a Python formatter. | Keep: tool-specific cue may improve correct configuration. Measure both. |
| H15 tob-differential-review — host | Security-focused differential review of a change; not implement an unrelated feature. | Review a security-sensitive diff / implement a feature without review. | Trim only after preserving security-review triggers: a shorter cue might reduce cost without losing recall. Measure security-review selection and findings. |
| H16 tob-gh-cli — host | Work with GitHub URLs, API access, issues or PRs; not a local-only Git question. | Inspect a GitHub PR / inspect a local unpushed diff. | Keep: remote-service cue may select authenticated tooling. Measure both. |
| H17 tob-modern-python — host | Configure modern Python tooling or migration; not ordinary Python application logic. | Set up Python tooling / fix a Python algorithm. | Keep: tooling distinction may avoid unnecessary migration advice. Measure selection. |
| H18 tob-property-based-testing — host | Design or review property-based tests for input domains; not coverage-guided fuzzing or mutation campaigns. | Test a parser round trip / run a coverage-guided fuzz campaign. | Trim only after preserving domain examples and exclusions: may reduce description cost while retaining recall. Measure positive and negative routing, including existing property tests. |
| H19 tob-sarif-parsing — host | Parse or aggregate SARIF results; not run a Semgrep scan. | Deduplicate SARIF findings / execute a new scanner. | Trim only if parser-versus-scanner distinction survives: may reduce cost without false scanner selection. Measure both. |
| H20 tob-semgrep — host | Run a Semgrep scan with its stated approval gate; not parse existing SARIF. | Plan a vulnerability scan / aggregate an existing SARIF file. | Trim only after preserving scan modes and approval trigger: may reduce cost without weakening approval. Measure routing and approval retention. |
| H21 tob-sharp-edges — host | Identify error-prone APIs or dangerous configurations; not general style review. | Review security defaults / proofread a README. | Keep: security-specific cue may improve misuse analysis. Measure both. |
| H22 tob-supply-chain-audit — host | Audit dependency supply-chain risk; not add a dependency without an audit request. | Review dependency advisories / update a routine code comment. | Keep: scope cue may prevent unexpected broad audits. Measure both. |
| H23 tob-variant-analysis — host | Find other instances of an established bug; not initial discovery without a known bug. | Search for variants of a proven flaw / investigate an unexplained first failure. | Trim only after retaining the known-bug prerequisite and exclusion: may reduce cost without premature variant hunts. Measure both. |
| H24 vitest-testing — host | Write or debug Vitest tests and mocks; not Go tests. | Add a Vitest mock / add a Go table test. | Keep: framework-specific cues may improve test routing. Measure both. |
| H25 web-performance — host | Optimize or audit web performance; not a generic visual redesign. | Investigate slow page load / change button color. | Keep: performance cue may avoid scope expansion. Measure both. |
| H26 writing-for-agents — host | Create or edit skills or agent-facing instruction files; not ordinary human-facing marketing. | Revise an agent instruction / write a customer announcement. | Clarify applicability alongside instruction-writing and prose-style; do not merge by name alone. Measure selection and complementary coverage. |

## Built-in skill

| ID and exact name / owner | Trigger; exclusion | Positive / negative routing case | Proposal and hypothesis to evaluate |
|---|---|---|---|
| B01 customize-opencode — built-in | Configure OpenCode itself, including agents, skills, plugins, servers or permissions; explicitly not application code or a project not configuring OpenCode. | Change an OpenCode agent definition / change an application's endpoint. | Keep: explicit exclusion may prevent configuration guidance leaking into application work. Behavioral enforcement unverified. Measure both cases. |

## Description sizes

Each ID joins one skill row above. Sizes use the metadata-count method described
in Evidence and limits.

| ID | Description code points |
|---|---:|
| K01 | 199 |
| K02 | 402 |
| K03 | 296 |
| K04 | 187 |
| K05 | 183 |
| K06 | 96 |
| K07 | 119 |
| K08 | 229 |
| K09 | 286 |
| K10 | 274 |
| K11 | 103 |
| K12 | 115 |
| K13 | 134 |
| K14 | 96 |
| K15 | 319 |
| K16 | 241 |
| K17 | 291 |
| K18 | 167 |
| K19 | 382 |
| K20 | 216 |
| K21 | 443 |
| K22 | 245 |
| H01 | 121 |
| H02 | 188 |
| H03 | 158 |
| H04 | 223 |
| H05 | 141 |
| H06 | 153 |
| H07 | 165 |
| H08 | 215 |
| H09 | 226 |
| H10 | 215 |
| H11 | 420 |
| H12 | 169 |
| H13 | 142 |
| H14 | 179 |
| H15 | 477 |
| H16 | 173 |
| H17 | 159 |
| H18 | 845 |
| H19 | 373 |
| H20 | 707 |
| H21 | 376 |
| H22 | 367 |
| H23 | 566 |
| H24 | 252 |
| H25 | 219 |
| H26 | 103 |
| B01 | 386 |
| **Total** | **12,741** |

## Evaluation design and remaining gaps

Parameterize the shared routing family **for each of the 49 descriptions**:
run that row's positive case expecting the named skill to be selected and loaded,
and its negative case expecting that skill not to be selected solely for that
reason. A negative case may legitimately select a different skill. These are 98
distinct case instantiations, not four aggregate cases that stand in for the
catalog. Include overlapping triggers and safety approval behavior in additional
cases where relevant. Record model, catalog snapshot, prompt and ordering,
selected skill calls, missed selections, unnecessary selections, task outcomes,
and repetitions; compare original and candidate descriptions under matched
conditions. Route disagreements for review rather than treating selection alone
as proof of quality.

No routing experiment, effectiveness measurement or serialized-size snapshot was
performed here. The proposals do not establish that shorter or on-demand exposure
is always better; results from any other benchmark or tested model do not prove
this catalog's behavior. Resolve selection quality, approval preservation,
installed-source parity, public reproducibility and exact prompt cost before
changing exposure. This inventory changes no instruction or Product law.
