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

printf '%s\n' '{"client_ref":"demo-client","key_id":"demo-key-1","principal_ref":"demo-operator","public_key":"11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=","capabilities":["product_read"],"product_scope":["demo-product"],"project_scope":["demo-project"]}' | concord client-register
printf '%s\n' '{"product_id":"demo-product","display_name":"Demo Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only","project_id":"demo-project","project_display_name":"Demo Repository","role":"primary"}' | concord product-create
printf '%s\n' '{"project_id":"demo-project","locator_id":"demo-path","kind":"canonical_path","value":"workspace/concord-demo","expected_version":1}' | concord project-locator-add
```

The three commands print one JSON result each. Use the `changed_refs` versions
from the Product result when composing later setup mutations; the example's
new Project version is `1`, so its locator mutation uses `expected_version: 1`.
After installing the adapter, run an adapter tool from that repository. It
resolves the matching locator and scope, then invokes the tool after core
authorization.

The `worker-*` verbs are internal orchestrator verbs. They use the same strict
JSON-stdin boundary but are not capability-gated agent tools and do not expand TS8.

## Typed worker lane dispatch

CD-0017 lane workers are dispatched by the hand-written `dispatch.ts` module,
not by the TS8 tool surface. It resolves a packet's registered lane to the
generated `concord-<lane>` agent and validates the closed `agent-lane-packet.v1`
shape before spawning, then invokes:

```text
opencode run --agent concord-<lane> --format json <packet>
```

The adapter asserts no model identifier. OpenCode resolves the executing model
from host configuration (`agent.<name>.model`, an OMR plugin entry, or an
inheritance rule) — Concord does not read, validate, or carry that decision.
The adapter does assert the executor identity: lane agents generate with
`mode: all` so run mode resolves the named agent rather than falling back to
the default agent (CD-0064), and the sanitized session export readback must
report an executing agent equal to `concord-<lane>`. A substituted executor
returns a typed `agent_identity_mismatch` failure and records no worker
evidence. The adapter reads one consistent session identity from the closed
JSON event stream, then runs `opencode export <session> --sanitize` and reads
the latest typed assistant `providerID`/`modelID` as `readback_model`
evidence. Whatever the host reports is what Concord records; an undeclared
model, an ambiguous/malformed event, or an ambiguous/malformed export shape
fails closed. Worker lifecycle evidence is recorded by the internal
`worker-dispatch`, `worker-complete`, and `worker-fail` CLI verbs; workers
never record workflow transitions, verdicts, or completion.

The adapter also admits the worker's `agent-lane-report.v1` report from that
event stream (CD-0056 D7). The report is read from the text of a `text` event, at
`part.text`, which is the only place the host carries model text; the last text
part that parses as a JSON object is the report, optionally inside one Markdown
fence. The report must satisfy the closed report schema and
echo the dispatched `attempt_id`, `lane_id`, `lane_version`, and `lane_digest`.
An admitted `completed` report becomes a `worker-complete` carrying
`evidence_origin: reported` and the reported evidence. A report that is absent,
malformed, schema-invalid, or bound to another packet becomes a `worker-fail`
with the `invalid_report` kind, and a `failed` report becomes a `worker-fail`
carrying the worker's own failure.

### Recommended host permission and fallback configuration

Keep Concord lane dispatch closed to generic host agents. In the OpenCode
configuration that owns the `task` permission, use an explicit map like this:

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
