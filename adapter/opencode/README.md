# Concord OpenCode adapter

The release installer owns adapter placement and version registration. Follow
the [installation guide](../../docs/installation.md) for release artifacts,
Secret Service prerequisites, upgrade, and uninstall behavior.

The release installer places the adapter under `~/.config/opencode/tools/` and
registers the plugin entry module `concord-plugin.ts` in the host `plugin`
array so OpenCode loads the typed tools. Keep the generated contract files
beside the entry module. OpenCode invokes every function-valued export of a
plugin entry module as a factory, so `concord-plugin.ts` exports exactly one
default factory and re-exports the tool definitions from `concord.ts`; the tool
modules are never used as entry points directly.

Worker evidence requires the OS Secret Service and `secret-tool`, with the
registered Ed25519 private key stored under the Concord credential identity.
The adapter does not store keys in a workspace, arguments, logs, or tool output.
Missing credentials, an incompatible core, malformed stdout, or a failed
transport returns a typed failure and does not guess an effect.

## Operator bootstrap CLI

`concord --help` is the bounded operator usage surface. Every listed command
reads one strict JSON object from stdin and writes one bounded JSON result. The
hyphenated and two-word forms below are the complete command vocabulary; no
other aliases are accepted:

| Hyphenated form | Two-word form |
|---|---|
| `client-register` | `client register` |
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
| `worker-dispatch` | — |
| `worker-complete` | — |
| `worker-fail` | — |
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
public key and seed are the RFC Ed25519 test pair; store the seed under the
same client reference before using the adapter.

```sh
export CONCORD_DB_PATH="$HOME/.local/share/concord/concord.db"
mkdir -p workspace/concord-demo
git -C workspace/concord-demo init --quiet
printf '%s\n' 'base64:nWGxne/9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2A=' | secret-tool store --label='Concord demo client' service concord account demo-client

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

The `worker-*` verbs are internal orchestrator verbs. They use the same strict
JSON-stdin boundary but are not capability-gated agent tools and do not expand TS8.

## Typed worker lane dispatch

The public `concord_work_transition.workflow_action` route accepts
`action_id: dispatch_worker` with `fields.lane_id`. The adapter builds the
closed lane packet from recorded state, obtains core authorization, and opens
one window for the next native Task call. The plugin replaces that call's
agent and prompt with the authorized packet. OpenCode owns the native worker
card, child session, progress, and cancellation. See
[CD-0102](../../docs/decisions/CD-0102-lane-dispatch-runs-as-a-native-task.md).

The adapter selects the registered `concord-<lane>` agent, not a model.
OpenCode resolves the model from host configuration. The worker ends with its
closed `agent-lane-report.v1` report as its final text part. The dispatch
window pins the worker directory, so the report carries none of it. The host's
Task result supplies the child session identity. The completion hook first
exports
that session unsanitized and refuses the attempt unless it opens with the exact
authorized packet; the sanitizer redacts text parts, so only the unsanitized
export can carry that check. It then exports with `opencode export <session>
--sanitize`, verifies the executing agent, and records host-derived model
readback. Worker-supplied identity is not a substitute for that readback.

The adapter derives `attempt_id`, `lane_id`, `lane_version`, and `lane_digest`
from the authorized dispatch packet. A model report cannot supply them: a report
that carries any of those fields is refused as `invalid_report`, whatever value
it names. An admitted completed report becomes `worker-complete` evidence. A
missing, invalid, or failed report follows the typed worker-failure route.
Workers return reports; they do not own workflow transitions, verdicts, operator
approvals, or completion.

### Managed Task scope

`concord_work_start` enrolls its calling session before capture or resume. A
valid public dispatch also enrolls before core authorization. The host persists
participation in session metadata, and the adapter verifies readback before
work proceeds. Enrollment changes no host permission rules and stores no
copy of a session directory or workflow state.

Managed sessions require an authorized window for every Task. Children inherit
participation from their host parent. An agent switch or host restart does not
remove participation, and an unmanaged caller cannot resume a managed worker.
An ordinary unmanaged Task retains its arguments and native permissions and
produces no Concord evidence. Direct calls to registered Concord lanes still
require authorization.

Unmarked sessions remain unmanaged until they enter through work start or
public dispatch. Use `concord_work_start` with the existing `work_id` when a
session must resume managed work. Read-only Concord calls do not enroll it.
If a later bootstrap step fails, recorded participation remains. If the host
cannot persist or read scope, the affected operation refuses instead of
starting unmanaged work. The host must support session metadata through its
documented session API.

### Operator work-state tab

The adapter renames the zellij tab and pane frame for every successful mutation
result that carries a `WorkPin`. The names use the returned post-state pin, not
request fields or a second database read:

```text
tab:  CON-42
pane: Concord | CON-42 | execution | Shorten the zellij tab name
```

The tab carries one stub: the `linear_issue_key` when the Product is
Linear-enabled, for example `CON-304`, and the project stub otherwise, for
example `toolbox`. The pane frame carries the full work state: project display
name, identifier, step, and work title joined with ` | ` and cut at 64 code
points. Absent fields drop from the pane name.

The tab mapping reads `ZELLIJ_PANE_ID` from `zellij action list-panes -a -j`,
then calls `zellij action rename-tab-by-id` with the matching `tab_id` and
`zellij action rename-pane` with the pane id. The adapter caches the mapping
briefly per session and strips control bytes and pipe characters from both
names.

`work_start` names the pane frame with the work title alone, because the
session-prepare contract carries no step or project display name and the
adapter does not add a database read for a cosmetic name. The first mutation
replaces it with the full work state. Both renames stay best effort: no
`ZELLIJ_PANE_ID`, an empty name, or a failed zellij call warns and never
changes a recorded outcome.

A failed best-effort side effect — a zellij rename, or a session title write
the host refused — appends a warning line to the tool result output after the
envelope, because the agent is the only reader that can respond to it.

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
The worktree-removal notice rides the same channel.

The receipt rides the assistant's own message. The work-state reporter queues
one block per session and suppresses a second emission of the same session,
work, and terminal lifecycle triple, and the plugin's
`experimental.text.complete` hook drains the queue into the text part before
the host persists it. The agent spends no tokens forming the receipt and
cannot omit it. A host that never calls the hook produces no receipt and no
error, and the hook swallows every throw so a display can never damage an
assistant message.

### Session goal title

A successful `work_start` writes the host session title to `Goal: <work title>`
through `PATCH /session/{id}` with a `{ title }` body after the confirmed
landing, and the work-state reporter refreshes the same title from the
post-state pin so a revised intent reaches it. OpenCode replaces a session
title automatically only while it is still the generated default, so the
Concord title persists.

The plugin registers `experimental.session.compacting` and reads the session
title through the control plane. When the title starts with `Goal: `, the hook
pushes that one line onto the compaction context, and the host joins it into
the compaction prompt so the anchored summary restates the goal. The hook is
required because the title alone never reaches the model and the continuity
block injects only when the launcher exported the selected Product and work.

Every title write stays best effort: an absent route, an empty title, or a
failed call warns and never changes a recorded outcome.

### Recommended host permission and fallback configuration

Apply Concord-only Task permissions to each coordinator's agent definition,
not to the global host Task permission. A coordinator definition can use:

```yaml
permission:
  task:
    "*": deny
    "general": deny
    "explore": deny
    "concord-*": allow
```

The lane agent definitions also deny nested task dispatch. If a host wants
per-lane model fallback behavior, configure the OMR plugin's ordered fallback
targets under its plugin tuple, for example:

```jsonc
{
  "plugin": [["opencode-model-routing", {
    "agents": {
      "concord-research": { "fallback_models": ["provider/preferred", "provider/fallback"] },
      "concord-implement": { "fallback_models": ["provider/preferred", "provider/fallback"] },
      "concord-design": { "fallback_models": ["provider/preferred", "provider/fallback"] },
      "concord-review": { "fallback_models": ["provider/preferred", "provider/fallback"] },
      "concord-verify": { "fallback_models": ["provider/preferred", "provider/fallback"] }
    }
  }]]
}
```

This configuration is purely host-owned. Concord neither reads nor asserts it;
the adapter does not pass `--model` on the spawn argv and does not carry a
resolution set. What runs is whatever the host's routing chain selects, and
the adapter records that selection only as `readback_model`. CD-0058.
