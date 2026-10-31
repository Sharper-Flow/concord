# Concord OpenCode adapter

The release installer owns adapter placement and version registration. Follow
the [installation guide](../../.concord/docs/installation.md) for release artifacts,
prerequisites, upgrade, and uninstall behavior.

The release installer places the adapter under `~/.config/opencode/tools/` and
registers the plugin entry module `concord-plugin.ts` in the host `plugin`
array so OpenCode loads the typed tools. Keep the generated contract files
beside the entry module. OpenCode invokes every function-valued export of a
plugin entry module as a factory, so `concord-plugin.ts` exports exactly one
default factory and re-exports the tool definitions from `concord.ts`; the tool
modules are never used as entry points directly.

The adapter presents the registered client reference selected by
`CONCORD_CLIENT_REF`, defaulting to `opencode`. It reads no private signing key.
An incompatible core, malformed stdout, or a failed
transport returns a typed failure and does not guess an effect.

## Planning and recorded issue identity

Agents use the Linear MCP server for all planning reads and writes. Concord
owns managed execution, not an issue or Project mirror. Its binary makes no
Linear API call and holds no Linear credential.

After the agent creates or finds an issue through Linear MCP, it records the
issue key, UUID, and URL with `concord_work_define.issue_link_record`.
Concord stores that reported identity without remote confirmation. One issue
identity belongs to one work unit; a second claim refuses. Resume reports the
recorded key and URL. Read current issue state through Linear MCP.
See [CD-0213](../../.concord/law/decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md).

`concord upgrade` takes a verified snapshot before any breaking migration applies.
The [upgrade note](../../README.md#planning-retirement-upgrade) describes the
intentional removal of unsent mirror writes and incomplete issue links.

## Operator bootstrap CLI

`concord --help` is the bounded operator usage surface. Every listed command
reads one strict JSON object from stdin and writes one bounded JSON result. The
hyphenated and two-word forms below are the complete command vocabulary; no
other aliases are accepted:

| Hyphenated form | Two-word form |
|---|---|
| `client-register` | `client register` |
| `client-show` | `client show` |
| `client-policy-update` | `client policy-update` |
| `client-policy-expand` | `client policy-expand` |
| `client-key-rotate` | `client key-rotate` |
| `client-revoke` | `client revoke` |
| `product-create` | `product create` |
| `project-create` | `project create` |
| `product-project-add` | `product project-add` |
| `project-locator-add` | `project locator-add` |
| `project-locator-update` | `project locator-update` |
| `project-locator-remove` | `project locator-remove` |
| `worktree-locate` | — |

`invoke` uses only its single-word form. A first installation
normally registers the client, runs `product-create` (which atomically creates
the Product, its first Project, and their membership), then runs
`project-locator-add`. Each setup result includes `changed_refs` with the new
Product/Project version, so the next command can use the returned version
without maintaining a separate version counter. The adapter resolves the
current Project context, then performs `invoke`.

Run `concord --help` for the complete required field lists and accepted enum
values. Operator setup commands use these closed values:

- `stage_maturity`: `prototype`, `alpha`, `beta`, `production`, `deprecated`.
- `stage_audience_commitment`: `operator_only`, `limited`, `public`.
- Membership `role`: `primary` or `secondary`.
- Locator `kind`: `canonical_path` or `git_remote`.
- Client capabilities: `product_read`, `work_define`,
  `work_transition`, `work_relate`, `work_compact`, or `cross_scope`.

### Authorize the primary agent names before activation

Before installing or activating a release with `Concord` and `Concord-RTE`,
the operator must authorize the exact agent names on every trusted client
that will run them. Agent scope matches are case-sensitive. Installing an
agent definition does not grant its name authority.

Use the same database selection and data home as the affected adapter. Inspect
the current policy, add the needed names with the operator CLI, then inspect
the result. This example uses the default client `opencode` and authorizes
`Concord` and `Concord-RTE`; replace the client reference when the host
uses another client. A client that will run only Concord needs only `Concord`.
A client that will run the optional `Concord-Planner` adds that exact name the
same way.

```sh
concord client-show <<'JSON'
{"client_ref":"opencode"}
JSON
concord client-policy-expand <<'JSON'
{"client_ref":"opencode","agent_scope":["Concord","Concord-RTE"]}
JSON
concord client-show <<'JSON'
{"client_ref":"opencode"}
JSON
```

Do not activate the release or select a profile until the final read
reports an active client whose `agent_scope` includes each intended exact name.
Verify that the principal, capabilities, Product scope, Project scope, and all
previous agent names survive unchanged. Retain the before and after policy
revisions with the installation evidence. On a fresh installation, register
the intended names in the client's `agent_scope` before starting managed work.

The additive command preserves existing grants under
[CD-0097 D6](../../.concord/law/decisions/CD-0097-bounded-parallel-predecessor-migration.md#d6-trusted-client-policy-grows-by-an-additive-operator-command).
Removing retired names is a separate operator decision through the
full-statement `client-policy-update` command; it is not part of this expansion.
The installer does not modify trusted-client policy. This is an operator
installation prerequisite, not an automatic installer check or an authority
transfer to the RTE. If it is skipped, managed calls remain refused.

## Database and host resolution

The authority database is outside a Project repository:

- `CONCORD_DB_PATH` selects an explicit database path, but Concord refuses an
  override inside a Git repository or worktree.
- Without the override, Concord uses
  `$XDG_DATA_HOME/concord/concord.db` when `XDG_DATA_HOME` is set; otherwise it
  uses `~/.local/share/concord/concord.db`.
- The database parent and file are created with restricted permissions.

Project host resolution is intentionally strict. The `directory` and `worktree`
must identify a real Git repository, and that repository must match a registered
Project locator. Register a `canonical_path` locator (or a matching `git_remote`)
before the adapter invokes a tool.

## Verbatim first installation

The following synthetic example runs from a non-Git parent directory. The
public key is from the RFC Ed25519 test pair.

```sh
export CONCORD_DB_PATH="$HOME/.local/share/concord/concord.db"
mkdir -p workspace/concord-demo
git -C workspace/concord-demo init --quiet

printf '%s\n' '{"client_ref":"demo-client","key_id":"demo-key-1","principal_ref":"demo-operator","public_key":"11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=","capabilities":["product_read"],"product_scope":["demo-product"],"project_scope":["demo-project"],"agent_scope":["demo-agent"]}' | concord client-register
printf '%s\n' '{"product_id":"demo-product","display_name":"Demo Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only","project_id":"demo-project","project_display_name":"Demo Repository","role":"primary"}' | concord product-create
printf '%s\n' '{"project_id":"demo-project","locator_id":"demo-path","kind":"canonical_path","value":"workspace/concord-demo","expected_version":1}' | concord project-locator-add
```

The three commands print one JSON result each. Use the `changed_refs` versions
from the Product result when composing later setup mutations; the example's
new Project version is `1`, so its locator mutation uses `expected_version: 1`.
`agent_scope` names the agents this client may present. It is fail-closed: an
agent absent from the scope is refused, and a client registered with an empty
scope authorizes none. Name each agent the adapter presents, which is the
OpenCode agent name, not its definition file name.

After installing the adapter, run an adapter tool from that repository. It
resolves the matching locator and scope, then invokes the tool after core
authorization.

## Native Tasks and coordinator outcomes

OpenCode owns Task execution, child sessions, progress, cancellation, permissions,
and model selection. The adapter does not intercept Task arguments, authorize
subagents, package prompts, or read their results into Concord evidence.

The coordinator obtains independent review through native Task, then records
`record_review` through `concord_work_transition.workflow_action` with
`fields: { review: <typed Review report> }`. The workflow checks the current
candidate, dependencies, findings, and disposition. Concord does not attest
which subagent produced the review; the coordinator owns that independence claim.
The [payload schema](../../contracts/agent-tool-surface-payloads.schema.json)
owns the typed report.

Native `worktree_verify` preparation retains its control bundle, subject, and
environment. Execution selects only `phase: execute` and the exact
`preparation_run_ref`. Captured producer receipts remain separate from the
coordinator's `record_verdict`; neither a Task summary nor a claimed verdict
replaces required verification evidence or CI.

### Session scope

Host tools create worktrees and start sessions; Concord claims, moves, and
vacates nothing (CD-0088 D1). A work item that spans two repositories gets one
host-started session per repository.

The resolved Project owns the session's Product scope. An inherited
`CONCORD_SELECTED_PRODUCT_ID` selection applies only when it names one of that
Project's Products. A Project that spans Products refuses until the operator
names one.

### The calling-host registry probe

Preparing a session for managed work runs one bounded probe of the calling
host: the bare `opencode debug config` command in the session's
directory, under the environment the calling process already has. The probe
resolves the registry the identity check reads, and Concord appends no
wrapper, option, or placeholder to it. An interrupted probe is a typed
transient failure, and the same request passes once the host answers.

An operator who wraps the host binary does that in host configuration, outside
this repository.

`OPENCODE_BIN` stays a test seam. The move-session version diagnostic stays on
the bare host.

### The text-part channel

When a mutation completes, cancels, or supersedes a work item, the reporter
delegates the closure receipt to the core: it shells `concord receipt` with
the terminal pin's work ID through the same runner as every core call, and
queues whatever bytes the verb prints. The receipt's format is product-owned
law (CD-0169), rendered by the core's internal/receipt package as an
unfenced markdown table — a plane header line and one row per verified
contract predicate — so the adapter keeps zero formatting logic and the
alignment belongs to the host markdown renderer. The verb prints nothing for
a work item outside the completed lifecycle, so a cancellation or a
supersession queues no notice, and an ambiguous contract projection
degrades to the header alone.

A failed receipt call appends a warning line to the tool result output after
the envelope, because the agent is the only reader that can respond to it.

The receipt rides the assistant's own message. The work-state reporter queues
one block per session and suppresses a second emission of the same session,
work, and terminal lifecycle triple, and the plugin's
`experimental.text.complete` hook drains the queue into the text part before
the host persists it. The agent spends no tokens forming the receipt and
cannot omit it. A host that never calls the hook produces no receipt and no
error, and the hook swallows every throw so a display can never damage an
assistant message.

### Session goal title

The work-state reporter writes the host session title to `Goal: <work title>`
through `PATCH /session/{id}` with a `{ title }` body from the post-state pin,
so a revised intent reaches it. OpenCode replaces a session
title automatically only while it is still the generated default, so the
Concord title persists.

The plugin registers `experimental.session.compacting` and reads the session
title through the control plane. When the title starts with `Goal: `, the hook
pushes that one line onto the compaction context, and the host joins it into
the compaction prompt so the anchored summary restates the goal. The hook is
required because the title never reaches the model, and the continuity block
injects only when the session's directory resolves an active claim.

Every title write stays best effort: an absent route, an empty title, or a
failed call warns and never changes a recorded outcome.

### Recommended host permission and fallback configuration

The generated `Concord` primary agent works end to end without an RTE session.
`Concord-RTE` is optional coordination for busy Product or Project work. Both
profiles allow native Task delegation. `Concord-Planner` is optional research and backlog
refinement: its profile edits only under `.planning/`, asks before any shell
command it does not name, denies commit, push, pull-request, and release
commands, delegates only to the read-only utilities, and the adapter refuses
it every Concord tool that writes work state. Shell rules match command text,
so a redirect can still write outside `.planning/`; the ask default is the
human gate. Numbered host examples keep their narrower Task policy;
hosts own those files and may configure their permissions independently.

The native `concord-review` helper loads the review method, repairs in-scope
findings and leaves changes uncommitted for its caller. It keeps Concord reads,
denies workflow writes and publication, and delegates only to exploration and
external lookup. Its shell rules preserve inherited host safety denies. These
command-text rules are not a sandbox; its role also prohibits direct-store and
wrapper bypasses.

The native `concord-verify` helper runs caller-named local checks under repository
test admission and an explicit caller budget, or diagnoses completed CI runs.
It reports commands, exit codes and failure evidence, linking known-flake records
only when they match. It denies editing, workflow writes and CI watch; the caller
owns waits, evidence binding and delivery. It shares the reviewer's host safety
and no-bypass boundaries. Test output alone is not captured verification proof.

The native `concord-implement` helper accepts assigned paths or a subsystem,
an outcome and a named check. It returns needs-decision for missing inputs or
changes outside that assignment. For a Fix it proves the regression fails before
the fix and passes after. It leaves changes uncommitted and reports exact paths,
commands, exits and gaps. It shares the reviewer's read access, explore/lookup-only
delegation, host safety and no-bypass boundaries; the caller owns workflow state
and delivery.

Hosts that enable helper-to-utility delegation must set OpenCode's
[`subagent_depth`](https://github.com/anomalyco/opencode/blob/v1.18.29/packages/web/src/content/docs/config.mdx)
to `2`. Its default `1` admits the helper but stops nested Tasks; the profile's
Task allowlist does not override that host ceiling. Concord does not change it.

The read-only utility definitions deny nested Task delegation. If a host wants
per-agent model fallback behavior, configure the OMR plugin's ordered fallback
targets under its plugin tuple, for example:

```jsonc
{
  "plugin": [["opencode-model-routing", {
    "agents": {
      "concord-review": { "fallback_models": ["provider/preferred", "provider/fallback"] }
    }
  }]]
}
```

This configuration is host-owned. The native Task runtime owns model selection;
Concord ships no model pin and neither reads nor asserts the host's routing.
