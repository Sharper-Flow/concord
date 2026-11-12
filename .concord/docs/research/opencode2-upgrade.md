# Concord and toolbox move to OpenCode 2.2.7

This is the canonical research record for [CON-1052](https://linear.app/sharper-flow/issue/CON-1052/design-the-move-of-concord-and-the-toolbox-host-to-opencode-2x). It proposes an implementation and recovery contract; it does not enact one. Research ran on 2026-11-12 against Concord commit [`6b97613972297cafaf67479d5a67e07a7fd09831`](https://github.com/Sharper-Flow/concord/commit/6b97613972297cafaf67479d5a67e07a7fd09831). No package installation, host upgrade, configuration change, deployment, or law change occurred.

## Decision

Pin the stable npm `@opencode/cli@2.2.7` native platform executable (`@opencode/cli-linux-x64@2.2.7` on Linux amd64; record whether the installer selected the baseline or musl variant instead) and `@opencode/plugin@2.2.7`, with source tag [`v2.2.7`](https://github.com/anomalyco/opencode/tree/v2.2.7) at commit [`fedc6f208aa2bd54c1019227214568083f6380ad`](https://github.com/anomalyco/opencode/commit/fedc6f208aa2bd54c1019227214568083f6380ad). Use matching `@opencode/client` or `@opencode/sdk` versions only where an existing consumer needs them; Concord itself needs only the plugin package's types.

The native executable is the migration artifact, not the Node build. V1 history import is Bun-only in this tag, while the Node shim reports completion without importing V1 history ([`v1-migration.bun.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/database/v1-migration.bun.ts), [`v1-migration.noop.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/database/v1-migration.noop.ts)). This makes the Node build unsuitable for the first start against a V1 database.

The design keeps Concord's thin TypeScript adapter and short-lived Go CLI. It replaces only host coupling: plugin registration, tool results, session-directory readback, the agent-switch hook, installer registration, agent identity, and the registry probe. It does not restore worker dispatch, session movement, Concord-owned worktrees, CI waiting, closure receipts, or other retired machinery. The toolbox needs separate provider, plugin, database-reader, TUI, and operational qualification before one controlled host cutover.

The pin is a qualification candidate, not evidence that 2.2.7 already works here. All 2.2.7 behavior below is source- or documentation-derived unless explicitly marked otherwise. The only executed 2.x host baseline remains the isolated 2.0.31 run recorded on [CON-1052](https://linear.app/sharper-flow/issue/CON-1052#comment-a9c9d42c-3081-47e2-83cc-404938e4f577). It proved a test plugin and model requests in a sandbox, not Concord, toolbox integration, V1 data survival, or 2.2.7 behavior.

## Evidence and scope

Evidence labels used below:

- **Executed:** a registry, Git, Linear, version, or other read-only command ran during this research. No 2.2.7 binary ran.
- **Source:** the pinned source establishes a code path; runtime behavior remains unverified.
- **Docs:** current upstream documentation states the behavior. Live documentation is not tag-locked unless the link names the commit.
- **Host inspection:** current operator-host source or deployed non-secret configuration was read. Toolbox paths below are repository-relative and omit private absolute paths, service addresses, and credentials.
- **Inference:** a conclusion follows from evidence but has not been exercised.

The operator's 2026-11-06 decision resumed research, treated the removal start gate as met, and added the toolbox host to scope ([decision](https://linear.app/sharper-flow/issue/CON-1052#comment-268f16c6-da74-488c-9514-45ed0dbcc833)). Existing decisions still govern implementation: one 2.x adapter, no dual-generation Concord entrypoint, no 1.x feature-parity exercise, controlled 1.x recovery rather than automatic fallback, no replacement closure receipt, and no enablement of background subagents in the migration. Every implementation slice must recheck the removal-project gate before it starts; the research decision is not a permanent waiver.

The affected Concord Domains are `agent-adapter`, `agent-call-boundary`, `agent-evolution`, `operator-surface`, and `repository-verification`. The toolbox repositories remain host-owned and outside Concord's Domain registry. This report adds no Domain and changes no architecture reference or law.

## Release selection

### Observed package and source facts

On 2026-11-12, read-only `npm view` calls returned `latest=2.2.7` for `@opencode/cli` and 2.2.7 for the matching plugin, client, and SDK packages. The old `opencode-ai` package remains the 1.x line at 1.19.5. The v2.2.7 tag resolves to the commit above and was published on 2026-11-10. The installed host's `opencode --version` returns `1.18.35`.

Use exact versions and the npm native platform package. Do not float `latest`, `beta`, or `dev`, and do not enable the self-updater. Channel-specific database naming makes an accidental channel switch a state-isolation risk ([`database-path.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/cli/src/database-path.ts)). Record the selected platform package's integrity and executable hash during qualification; the source tag alone is not build attestation.

### Changes after the 2.2.5 lead

The tag-locked [`CHANGELOG.md`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/CHANGELOG.md) records these material changes:

- 2.2.6 added MCP connection pooling, a bounded wait for connecting MCP servers before model calls, `opencode run --worktree`, `opencode run --archived`, background-job waiting and SIGTERM cancellation, and SDK job listing ([#61185](https://github.com/anomalyco/opencode/pull/61185), [#60490](https://github.com/anomalyco/opencode/pull/60490), [#60712](https://github.com/anomalyco/opencode/pull/60712), [#61270](https://github.com/anomalyco/opencode/pull/61270), [#60699](https://github.com/anomalyco/opencode/pull/60699), [#61911](https://github.com/anomalyco/opencode/pull/61911), [#60698](https://github.com/anomalyco/opencode/pull/60698)). `opencode run` now waits for background jobs with no time limit, so every scripted qualification run needs its own timeout.
- 2.2.7 added Code Mode catalog deltas and MCP OAuth refresh concurrency fixes ([#62176](https://github.com/anomalyco/opencode/pull/62176), [#62827](https://github.com/anomalyco/opencode/pull/62827), [#63012](https://github.com/anomalyco/opencode/pull/63012)).
- 2.2.7 also changed SDK readiness behavior: some list endpoints return partial results during plugin loading instead of waiting, so consumers must refresh on update events ([#62796](https://github.com/anomalyco/opencode/pull/62796), [#63040](https://github.com/anomalyco/opencode/pull/63040)). Herdr and other launchers must not treat one partial agent or tool listing as complete.
- The `@opencode/ai` `ImageResponse.image` removal is breaking for direct consumers but has no known Concord consumer. Each host repository must typecheck its own SDK usage rather than infer compatibility from Concord's result.

Version 2.2.7 is two patches beyond the latest prior research and contains the relevant MCP improvements. Waiting for every upstream gap would leave the pin permanently moving. Qualify this exact version; requalify a later version as a new pin rather than silently updating this report's evidence.

## What remains after the removals

The shipped Concord adapter is now the 14-file set in `scripts/install.py:120-135`. Its entry exports one factory, seven `concord_*` tools, and three hooks (`adapter/opencode/concord-plugin.ts:38-72`). Each tool call reads the session directory, resolves the Project, invokes the stamped Go binary, and returns one bounded JSON envelope (`adapter/opencode/concord.ts:685-782`, `adapter/opencode/session-directory.ts:17-29`). The adapter has no host event subscription, metadata write, session-title write, compaction hook, worker dispatcher, or session mover (`adapter/opencode/concord-plugin.test.ts:41-50`).

The surviving host obligations are therefore narrow:

1. Register the generated tools and preserve their names, descriptions, and generated JSON Schemas.
2. Supply `sessionID`, `agent`, `messageID`, and the current session directory on every call. Resolve the directory anew; setup-time location is not session authority.
3. Preserve the exact core release pair, abort propagation, one-shot transport retry, bounded envelope, and typed mutation reconciliation.
4. Preserve the agent-switch notice through the host's supported context hook.
5. Install versioned adapter files, agents, skills, and instructions; verify the resolved agent registry before managed action.
6. Preserve model-owned provider selection and native Task ownership. Concord does not add routing, dispatch, completion, or recovery bridges.

The removal pull requests ([#2164](https://github.com/Sharper-Flow/concord/pull/2164), [#2168](https://github.com/Sharper-Flow/concord/pull/2168), [#2209](https://github.com/Sharper-Flow/concord/pull/2209), [#2210](https://github.com/Sharper-Flow/concord/pull/2210), [#2383](https://github.com/Sharper-Flow/concord/pull/2383)) already deleted the adapter code for worker launch, host steering, automatic receipts, session moves, continuity injection, Concord-owned worktrees, and host CI waiting. Those behaviors are excluded from the target design, not compatibility gaps.

Current version declarations also need consolidation. The adapter typecheck and runtime probe pin 1.18.23, Code Mode compensation names 1.18.30 behavior, and instruction evals pin 1.18.35 (`.concord/docs/adapter-host-pin.v1.json`, `adapter/opencode/concord.ts:47-77`, `adapter/opencode/evals/instructions/host.py:27`). The migration should leave one exact host pin and one evidence set. The runtime result probe (`scripts/probe-adapter-host-result.py`) runs `opencode-ai@<version> debug agent build --tool`; 2.2.7's `debug` command has only `agents`, `config`, `heap-dump`, and `paths`, so the probe needs a new full-session or SDK route, not a version bump.

## Capability comparison and adoption classes

“Required” means the migration cannot complete without it. “Same-cutover improvement” is included only with its own passing acceptance. “Later” is deliberately outside migration completion. “Not adopted” has no target implementation.

| Surface | OpenCode 1.x now | OpenCode 2.2.7 evidence | Class and design consequence |
|---|---|---|---|
| Plugin entry | One function-valued default export; a single TypeScript file under `tools/` | `Plugin.define({id, setup})`; configured local plugin paths are directories. V1 plugin code does not run on V2 ([migration guide](https://opencode.ai/docs/build/plugins/migrate-v1/), [`plugin.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/plugin/src/promise/plugin.ts#L64)) | **Required.** Rewrite the entry and install a directory/package plugin, not a second dual-generation entry. |
| Tool registration | Returned `tool` map; the seven tools are direct model tools | `ctx.tool.transform` adds `Tool.Info`; names allow letters, digits, `_`, and `-`, and a `namespace` option prefixes the name ([`tool.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/tool.ts#L404), [`runtime.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/tool/runtime.ts#L290)). While an agent allows `execute`, only `codemode: false` tools are direct; every other tool is reachable only inside Code Mode ([`tool.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/tool.ts#L263)) | **Required.** Register the same seven names with no namespace and `codemode: false`, so they stay direct with their generated JSON Schemas and the tool-surface budget is unchanged. Code Mode exposure would be a separate, measured change. |
| Tool arguments | Adapter removes a Code Mode `request` double wrap | Tool schemas are converted at registration and Code Mode renders signatures from JSON Schema ([`runtime.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/tool/runtime.ts#L175), [`tool-schema.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/codemode/src/tool-schema.ts)) | **Required.** With `codemode: false`, Code Mode cannot deliver Concord calls, so delete the V1 argument unwrap once direct calls prove the published payload shape. Do not add another compatibility layer. |
| Model-visible schemas | `tool.definition` rewrites generated schemas for the provider | Raw JSON Schema is accepted; V2 normalizes registration output, but source review does not prove every Anthropic root composition case | **Required.** Start with generated schemas and remove the hook. Retain only a failing-case-driven provider transform if qualification proves it necessary. Real Claude calls must cover every schema composition family. |
| Tool result | `{title, output, metadata}`; `output` is the only serialized-envelope carrier | `Tool.Result` accepts `content` and optional typed `output`. A result that carries an `output` key at all, even `undefined`, from a tool with no declared output schema is a host defect (`Effect.die`), not a `Tool.Error`; plain string `content` is valid ([`schema/tool.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/schema/src/tool.ts#L86), [`runtime.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/tool/runtime.ts#L47)) | **Required.** Carry the envelope once as string `content`, with metadata and no `output` key. Amend TS7 §8.1 and CD-0084 D2/D3 through the implementation item's law route. Preserve the single-carrier and bounded-error invariants. |
| Output bounds | 2,000 lines / 51,200 bytes; oversize becomes a valid error envelope | 2,000 lines / 50 KiB (51,200 bytes, the same limit), with spill to a retained tool-output file. The host skips its bound whenever `metadata.truncated` is present, whatever its value ([`tool-output.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/tool-output.ts#L13)) | **Required.** Keep Concord's 51,200-byte cap and pre-host oversize refusal. Never set `metadata.truncated`; do not rely on host spill to transport a truncated JSON envelope. |
| Tool context | `context.agent`; private client `GET /session/{id}` returns `directory` | Context still supplies `sessionID`, `agent`, `messageID`, and `id`, but no directory; the promise API's `signal` replaces V1's `abort` ([`plugin tool.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/plugin/src/promise/tool.ts#L11)). `ctx.session.get(sessionID)` returns `location.directory` ([`session.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/plugin/src/promise/session.ts#L215), [`location.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/schema/src/location.ts#L9)) | **Required.** Replace the private client with the public session read on every call. Fail closed when directory is absent. |
| Plugin lifetime | Effectively process-oriented in Concord's lease assumptions | The plugin supervisor is a per-Location node, so one process can run `setup` once per Location; setup `ctx.location` is not every observed session's location ([`supervisor.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/plugin/supervisor.ts#L248), [migration guide](https://opencode.ai/docs/build/plugins/migrate-v1/)) | **Required.** Requalify release-lease claim/liveness under location-scoped instances. Never cache setup location for Project resolution. Preserve CD-0111's exact release pair and cleanup fences. |
| Agent-switch notice | `chat.message` records the agent; `experimental.chat.system.transform` mutates `system[0]` | The migration guide maps `experimental.chat.system.transform` to `ctx.session.hook("context")` and `chat.message` to `"prompt"`. The `context` event itself carries `agent` and a mutable `system: SystemPart[]`, so it alone can detect and announce a switch ([`session.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/plugin/src/promise/session.ts#L25), [`subagent.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/tool/plugin/subagent.ts#L282)) | **Required.** Use one context hook and structured system parts. Prove one notice on switch and no duplication on repeated requests. |
| Agent identity | File name plus frontmatter `name`; `debug config` exposes an agent map | Agent ID comes from the file path; `debug agents` prints a sorted `Agent.Info[]` with `id`, `mode`, `hidden`, and `permissions` ([debug handler](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/cli/src/commands/handlers/debug/agents.ts), [`schema/agent.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/schema/src/agent.ts#L23)) | **Required.** Derive identity from the path and parse the new array. Preserve CD-0049's fail-closed registry assertion and unknown-agent tests. |
| Permissions | `permission{bash,task}`, `disable` | Ordered `permissions[{action,resource,effect}]`; `bash→shell`, `task→subagent`, `disable→disabled` ([permissions](https://opencode.ai/docs/permissions/)). An agent file with any V2-only key (`permissions`, `request`, `system`, `disabled`) is read as V2, and its legacy keys, including `permission`, are dropped with only a log warning ([`agent.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/config/plugin/agent.ts#L33)) | **Required.** Generate V2-only rules and prove allowed, denied, and last-match cases against the resolved `permissions` that `debug agents` prints, not the file text, so a dropped legacy key cannot fail open. Keep `steps` unset for Opus agents. |
| Skills and instructions | Installer writes `skills.paths` and project instructions | Tag source makes `skills` a string array of paths or URLs and `instructions` a string array; a V1 `skills.{paths,urls}` object is normalized into the array ([`schema/config.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/schema/src/config.ts#L91), [`normalize.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/config/normalize.ts#L234)) | **Required.** Write the V2 `skills` array, not the normalized V1 object, and prove discovery of the versioned directory. Keep versioned installed skills and CD-0007 D7's no-plugin-injection boundary; amend D7's `skills.paths` text and its restart statement to the qualified V2 behavior. |
| Nested subagents | Generated subagent profiles admit one more hop (review, implement, design, research, and verify helpers may call explore or lookup); the host config sets top-level `subagent_depth: 2` | The limit is `experimental.subagent_depth`, default 1, which refuses any subagent launched by a subagent. V2 normalization omits a top-level `subagent_depth` as an unsupported legacy setting ([`subagent.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/tool/plugin/subagent.ts#L135), [`normalize.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/config/normalize.ts#L45)) | **Required.** The host configuration sets `experimental.subagent_depth: 2`, or a separate profile-manifest change removes nested delegation. Prove one admitted helper-to-utility call on the pinned binary. |
| Core subprocess | `Bun.spawn` with abort propagation | The selected native artifact must prove its Bun API at 2.2.7; the separate Node build lacks V1 import and was not selected | **Required.** Keep `Bun.spawn` for this artifact and exercise real transport, abort, stderr, and exit behavior. Node-runtime portability is **not adopted**. |
| Session metadata | No surviving Concord writer | `setMetadata` replaces the complete metadata object; children inherit parent metadata ([`session/session.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/session/session.ts#L85), [`projector.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/session/projector.ts#L984)) | **Not adopted.** No new metadata authority or compatibility write. If a later consumer appears, it must read-modify-write the whole object. |
| Events and compaction | No surviving subscription or compaction hook | Typed event subscription and compaction hooks exist | **Not adopted.** No new event cache, projection, or compaction authority. Existing explicit Concord reads remain canonical. |
| Closure receipt | Already removed | No direct equivalent is needed | **Not adopted.** The operator's decision stands. |
| Mid-run input | V1 can append late assistant output after an injected user message, causing Anthropic prefill failures | Inbox items promote steering only at step boundaries; assistant content originates in the runner ([`inbox.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/session/inbox.ts#L53), [`runner/step.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/session/runner/step.ts#L88)) | **Same-cutover improvement.** Source suggests the observed 28-case race is structurally removed; replay that case suite before claiming the fix. The separate max-steps failure remains: V2 appends `Message.assistant(MAX_STEPS_PROMPT)` ([`runner/llm.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/session/runner/llm.ts#L272)). |
| Native subagents | Native Task is host-owned | V2's direct `subagent` tool prefixes new child prompts with `You are a subagent spawned by another session.`; foreground calls can return a background result ([`subagent.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/tool/plugin/subagent.ts#L19)) | **Required to preserve host ownership; background enablement is later.** Do not infer completion from the first tool return or add Concord readback. See the dedicated evaluation below. |
| Debug subagent probe | V1 debug runner reaches missing `promptOps` | V2's headless `Tool.check` excludes `codemode:false` tools, including `subagent`; no CLI debug subagent route exists in the tag | **Not adopted.** Use full-session `opencode run` or SDK prompt tests for admitted native subagent dispatch. |
| `opencode run --worktree` | Host tools already own worktrees | New in 2.2.6 | **Later.** A host-tooling simplification needs its own design; it is not a migration prerequisite. |
| MCP catalog refresh | Expired HTTP recovery can leave stale tools and validators | V2 reconciles tools-changed events and adds catalog deltas, but read-after-write reconciliation gap [#39902](https://github.com/anomalyco/opencode/issues/39902) remains open | **Same-cutover improvement, not closure.** Run the expired-session and mutation/readback cases; retain AL-46 until they pass or the operator disposes of the residue. |
| Compaction pruning | Toolbox decision 0013 disables `compaction.prune` after cache-cost evidence | V2 omits `compaction.prune` as an unsupported legacy setting and the tag's core source has no prune pass; compaction offers `auto`, `keep`, and `buffer` ([`normalize.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/config/normalize.ts#L354)). No billing behavior was executed here | **Required config conversion; tuning is a later decision.** Remove the dead `prune` key in the converted configuration, keep `auto: true`, and re-measure Claude Max cache writes under V2 compaction before amending decision 0013. |

## Related host issues

Linear renamed two referenced issues while this research ran. The current keys are AL-46 (formerly CON-1132) and AL-45 (formerly CON-1576).

| Issue | Disposition at 2.2.7 | Required evidence or remaining work |
|---|---|---|
| [VSN-16](https://linear.app/sharper-flow/issue/VSN-16/review-vision-v1v2-mcp-plugin-entrypoint-and-controls) | **Changes, not fixed by the host.** Vision already has a dual entrypoint and V2 controls; 2.2.7 supplies the target plugin and MCP contracts. No live V2 run establishes that Vision's controls work. | Load the installed Vision package under the exact pin; exercise V1/V2 registration as applicable, MCP controls, tool refresh, and output schemas. Keep VSN-16 open until live evidence exists. |
| [VSN-39](https://linear.app/sharper-flow/issue/VSN-39/resolve-pinned-plugin-sdk-opentelemetry-advisory-after-the-opencode-20) | **Changes the dependency evidence.** Executed registry reads show `@opencode/util@2.2.7` depends on OpenTelemetry SDK packages 2.8.0, which resolve `@opentelemetry/core@2.8.0`, the patched version for [GHSA-8988-4f7v-96qf](https://github.com/advisories/GHSA-8988-4f7v-96qf). | Align Vision's SDK and lockfile, then require both `npm audit --omit=dev --json` and `npm audit --json` to report no affected packages for this advisory. Registry metadata alone does not close VSN-39. |
| [AL-46](https://linear.app/sharper-flow/issue/AL-46/resolve-the-remaining-upstream-mcp-tool-refresh-gap-after-expired-http) | **Changes but does not fully fix.** The bounded initial wait and Code Mode deltas address parts of the stale-catalog problem. The source still debounces registry reconciliation, and upstream read-after-write issue #39902 remains open. | Reproduce expired HTTP recovery, server replacement, add/remove/connect/disconnect followed immediately by Code Mode search/call, and validator refresh. Do not bypass the signature guard or patch OpenCode locally. |
| [AL-45](https://linear.app/sharper-flow/issue/AL-45/opencode-debug-task-probes-fail-before-dispatch-with-missing-promptops) | **Leaves the requested debug dispatch unsupported.** The V2 headless checker excludes the native subagent tool, so the release does not supply the missing debug-runner capability. | Record the unsupported surface and keep the proven full-session route. No local core patch or permission change. |
| [LGREP-43](https://linear.app/sharper-flow/issue/LGREP-43/opencode-rewrites-tracked-opencodepackage-lockjson-at-session-start) | **Changes the likely mechanism, but runtime evidence is absent.** V2 has separate Bun and Node plugin loaders and the reviewed source does not follow the V1 project dependency-install path. This does not prove that a real session leaves every tracked lockfile untouched. | Start the pinned binary in a disposable copy of an affected worktree, hash tracked files before/after, and require no host-generated diff. The owner decision to update or untrack the lockfile remains independent. |

## Toolbox host inventory

Host inspection refreshed the 2026-11-06 inventory on 2026-11-12. One material correction: Claude Max now has a V2 `server.js` path in toolbox source; the earlier “not started” statement is stale. That path remains unexecuted here.

### Provider and model path

| Surface | Current evidence | Migration acceptance |
|---|---|---|
| Claude Max (`plugins/opencode-claude-max`) | V1 auth implementation remains; a V2 session-HTTP-hook entry now exists. No live V2 Opus evidence exists. | Authenticate without copying credentials into artifacts; complete Opus requests, preserve cost transformation, and replay prefill and cache-cost cases. |
| OpenCode Model Routing (`plugin/src/plugin.ts`, `plugin/index.js`) | A dual-shaped default export and installed-package V2 test exist. The SDK is pinned at 2.0.14; the real-binary script accepts `OMR_V2_BIN` but otherwise resolves an unpinned CLI. | Run `OMR_V2_BIN=<2.2.7-native-binary> scripts/e2e-v2-runtime.sh` and the installed-package test against the exact pin. Assert model selection, provider auth, fallback, and no V1 entry registration. |
| Advisor and Opus agent definitions | Advisor and several integration tests assert 1.18.35; the advisor's project registration is separate from the global plugin list. | Update pins only with revalidated wire behavior. Run an admitted advisor child on Opus with `steps` unset and verify denied-subagent cases. |
| Provider hints and prefix probe | Both are deployed host integrations with version-sensitive tests. | Load under 2.2.7, verify provider selection remains host-owned, and run each real-binary integration test. |

### Server, autoload, and TUI plugins

Every configured server entry must either pass V2 qualification or be explicitly removed by its host owner before cutover. The configured set is:

- Concord adapter, OMR, Claude Max, Vision, Morph Fast Apply, and third-party `opencode-cmd-provider@2.2.5`;
- project-skills, execute-output, ci-watch, cache-meter, tab-bell, provider-hints, task-delegation, and voice-contract;
- the oc-run run-context entry.

The additional deployed or project-scoped set is advisor, capacity-context, codemode-signature-guard, execute-errors, and prefix-probe. Their absence from the global list is intentional, not permission to forget their project registration. In particular, codemode-signature-guard must qualify against V2 catalog deltas without weakening signature admission.

Autoload and interactive assets are herdr-agent-state, moshi-hooks server/TUI, opencode-session-state, restart, herdr-tui-session, herdr-opencode TUI, and the same third-party command provider used by the TUI configuration. V2 loader differences can remove status without failing the server. Each asset needs a load-and-behavior probe; zj-radar must receive a live session-state row before cutover. Toolbox decision 0017's V1 loader rationale must be amended or retired with the producer port.

Herdr's V2 `cli.json` registration remains deferred in its installer and no deployed registration was observed. The cutover must install a Herdr release that writes the V2 registration and then reinstall the integration. A single partial `agent.list` response is not proof of readiness under 2.2.7's new partial-result behavior.

Morph Fast Apply declares both SDK generations, but a dual dependency declaration is not behavioral evidence. Vision and OMR likewise need real-host qualification despite existing V2-shaped entries. The third-party command provider must be pinned to a version that supports the same host; if it cannot load, the operator-visible command workflow needs an explicit replacement before cutover.

### Database, wrapper, and operational surfaces

V2 uses `session_v2`, `session_message`, and `session_inbox` rather than the V1 `session`, `message`, and `part` layout used by toolbox readers. The 2.0.31 baseline observed that new schema, but no V1 row-survival claim followed. Source at 2.2.7 confirms a Bun-only V1 import and `OPENCODE_DB` override; it does not prove lossless migration.

The `oc` wrapper creates one XDG data shard per project root and supports `OC_FORCE_GLOBAL_DB=1`. The wrapper does not set `OPENCODE_DB`, but the host shell profile exports the relative default `OPENCODE_DB=opencode.db`. V2 resolves a relative value inside each shard's data directory, so shards stay isolated; the export also disables channel-specific database naming, so any non-stable 2.x binary started from that shell would open and migrate the stable `opencode.db` ([`database-path.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/cli/src/database-path.ts)). Keep the shard model, verify the V2 binary computes the same database path for every shard, and decide explicitly whether the export stays. Never set an absolute shared `OPENCODE_DB`: that would collapse the isolation the wrapper provides.

V2 configuration normalization omits unsupported legacy settings with only a diagnostic. Two such settings carry host decisions today: top-level `subagent_depth: 2`, which V2 reads only as `experimental.subagent_depth`, and `compaction.prune: false` (decision 0013). The configuration conversion must record every `omitted unsupported legacy setting` diagnostic and resolve each one explicitly.

The following readers need schema-specific qualification:

- `opencode-db-maint`: reads and deletes V1 session/event rows; it is the highest-risk reader because it writes.
- `oc-session-cost`: reads V1 session/message JSON and must be checked against V2 token and cost fields.
- `oc-stop-audit`: reads V1 session/message/part history.
- `oc-cutover`: read-only database identity checks, idle gating, exit, installer, and resume behavior; V1 column and session assumptions need replacement.
- `zellij-project-launcher`: delegates database work to db-maint rather than querying directly.
- `oc-session-open`: uses Herdr only; no OpenCode database dependency was found.

The `scripts/deploy-oc-run.sh` fence deliberately refuses any host other than 1.18.35. Update that constant only with new oc-run evidence, and re-run retained-release validation. The restore playbook and deploy map also pin 1.18.35; they must change in the same host cutover change set so recovery documentation cannot reinstall an unintended mixture.

## Smallest 2.x-native design

### Concord adapter and installer

1. **Entry:** one `Plugin.define` directory plugin. Setup captures only the public plugin context. Tool execution reads the current session through `ctx.session.get` and derives `location.directory` every time.
2. **Registration:** add the existing generated definitions through `ctx.tool.transform` with `codemode: false` and no namespace. Keep Go and generated TypeScript as the semantic authority. Delete the V1 Code Mode argument unwrap and `tool.definition` compensation when native probes pass.
3. **Results:** return one JSON string in `content` plus bounded metadata, with no `output` key and no `metadata.truncated`. Preserve TS7's single carrier, exact parse, 51,200-byte envelope cap, and valid oversize error. The future implementation contract must amend TS7 §8.1 and CD-0084's `output`-only wording before code changes.
4. **Hooks:** replace the two V1 agent-switch hooks with one session `context` hook. Add no event, metadata, title, compaction, or receipt route.
5. **Transport:** keep the stamped core path, manifest digest, `Bun.spawn`, abort propagation from the context `signal`, bounded stderr, transport retry, and mutation-result reconciliation. No daemon, cache, adapter database, or second runtime.
6. **Agents:** generate V2-only permission rules and file-path identities, with no legacy frontmatter keys. Parse `debug agents` in `session-prepare`; preserve refusal on missing, hidden/disabled, subagent-only, or unexpected identities. Keep nested helper-to-utility delegation working through `experimental.subagent_depth: 2` or remove it from the profile manifest.
7. **Installation:** register the plugin directory and the V2 `skills` and `instructions` arrays. Preserve versioned skill assets and release leases. Update the host pin, eval runner, and stale pin prose together, and move the runtime result probe to a route 2.2.7 still has.

Two designs were compared. A dual V1/V2 entry would reduce cutover coordination but permanently retain two host contracts and the compensation code the operator asked to remove. A clean V2 entry with a complete external 1.x recovery environment has one runtime interface and matches the target decision. The clean entry is selected. A Node subprocess portability rewrite was also rejected: it adds a second runtime target and cannot perform the required Bun-only V1 history import.

### Host gaps that remain after the design

- V2.2.7 has not executed in this environment. Plugin, result, session-location, agent registry, and native-runtime claims need real-binary proof.
- V1 row survival and reader equivalence are unknown until migration runs on copies of every shard shape.
- MCP read-after-write reconciliation remains an upstream gap. A process restart remains the operator-accepted fallback after MCP redeployment; Concord must not build a second refresh mechanism.
- Claude Max, OMR, Vision, Morph, command provider, Herdr/TUI, and zj-radar have source-level V2 paths at varying maturity, not an integrated working set.
- The native subagent prompt prefix changes child context. Review generated helper instructions against it, but do not add a Concord wrapper to suppress it.
- Background-subagent restart has a source path but no runtime proof, and unfinished-work ownership is not established; see below.

## Background subagents: evaluation without adoption

V2.2.7 source shows four distinct states that a later decision must test:

1. A foreground call normally returns the child's completed output.
2. A foreground call can become backgrounded and return notification guidance before completion. That first return is not a completion signal.
3. Interrupting the parent call cancels the child and job; error and cancelled results include the child session ID.
4. Background completion arrives as a synthetic message carrying child and state metadata, with notification deduplication.

Restart has a source path but no runtime evidence. A backgrounded job is persisted as a job row, and server start runs `resumeSuspendedSessions`, which restarts running background children and suspended sessions and delivers their notifications ([`job.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/job.ts#L208), [`restart.ts`](https://github.com/anomalyco/opencode/blob/fedc6f208aa2bd54c1019227214568083f6380ad/packages/core/src/session/execution/restart.ts#L222)). Whether that path survives a real process kill, and who owns a resumed child's unfinished work, remain open.

A later enablement item must execute a matrix covering normal foreground completion, foreground-to-background transition, real child completion after the first return, cancellation before and after backgrounding, parent process restart, duplicate notification suppression, and explicit ownership of unfinished children. It must also verify the fixed child-prompt prefix against every generated helper. No migration slice may turn background execution on or make Concord responsible for its recovery.

## Recovery and cutover plan

Recovery is a controlled restoration of a matched environment, not an adapter fallback. The older binary must never open a migrated database.

### Before any production change

1. Recheck the operator's removal-project start gate and all slice evidence. Freeze the exact package, platform integrity, source commit, Concord release, toolbox/Herdr/Vision/OMR commits, and configuration manifests in the cutover record.
2. Drain new session admission and let active work reach safe stopping points. Preserve worktrees, branches, artifacts, and Concord's own store through their owning tools; do not treat host-session history as work authority.
3. Inventory every OpenCode XDG shard, including the forced-global route. Take SQLite-consistent online backups while WAL is active, not plain file copies. Verify integrity, schema, counts, and hashes for every shard. Keep a separate verified Concord snapshot through Concord's snapshot route.
4. Preserve the exact 1.18.35 executable and package metadata, Concord release, plugin sources and lockfiles, agent/config files, Herdr assets, and dependency installation route. Keep credentials in their existing secret store; do not copy secrets into the evidence bundle.
5. Run the entire migration against copies first. Compare V1 sessions, messages, parts, projects, event/KV data where imported, and per-shard identity before and after. Record every loss or transformation; an unexamined count mismatch blocks cutover.

### Rehearsal and cutover

1. Start the pinned native binary in an isolated environment with copied shards and converted configuration. Prove plugin loading, provider auth, model routing, Concord reads and mutations, native Task, MCP refresh, TUI status, database readers, and session resume.
2. Rehearse recovery from the same snapshots and prove the restored 1.x environment before touching production. A backup without a passing restore rehearsal is not recovery evidence.
3. At the controlled cutover, stop V1 writers, take final per-shard snapshots, switch the binary and all host-owned configuration/plugins as one coordinated operation, and start 2.2.7 without automatic update. Do not set an absolute shared `OPENCODE_DB` across project shards.
4. Run smoke checks per shard before reopening general admission: version/hash, database identity, migrated history, resolved agents, provider/model route, Concord read and idempotent mutation, native subagent, MCP catalog, Herdr/TUI status, oc-run, and resume.
5. Keep the migrated V2 databases and all pre-migration snapshots until the operator accepts the result. Reconcile any post-cutover work through Concord, Git, and artifacts rather than attempting to downgrade V2 session rows.

### Recovery trigger and procedure

Stop on credential failure, missing history, shard cross-contamination, Concord identity/transport failure, provider unavailability, uncontrolled plugin writes, or inability to resume sessions. Stop all V2 processes first. Preserve the V2 databases for diagnosis, restore every shard from its matched pre-migration snapshot, restore the V1 binary, Concord release, configuration, plugins, dependencies, and Herdr registration together, then verify version, hashes, database integrity, provider auth, Concord smoke, and session resume before reopening admission. Never run 1.18.35 against a database that 2.x has migrated.

The same rule applies to Concord's own store: an older Concord release must never open a store a newer release has migrated. The S2 release must therefore carry no store migration beyond the V1 recovery release, so recovery keeps the live store and its post-cutover writes. If a store migration does land between the two releases, recovery must restore the matched Concord snapshot and record every later Concord write for re-entry before reopening admission.

## Implementation slices

These are separately ticketable outcomes, not authorization to implement now. Each owner must shape its own contract, name law changes where applicable, and recheck the start gate. S1 blocks all later slices. S2–S7 may proceed independently after S1; S8 requires all of them. Once S2 merges, every later Concord release is V2-only and cannot be installed on the 1.x host, so S2 merges inside the S8 cutover window; until then it stays a reviewed branch kept current with the default branch.

### S1. Qualify the native 2.2.7 lab and V1 data migration

**Owner:** toolbox host lab, with upstream issue evidence. **Outcome:** a reproducible isolated runner proves the chosen artifact and all host-critical assumptions before repository migrations begin.

- Pin CLI/plugin integrity, native executable hash, and source commit; retain the existing sandbox controls and secret-scan assertions.
- Add probes for plugin setup/context/tool transform, `codemode: false` direct exposure, string-content results, oversize results, converted-configuration diagnostics, nested subagent depth, per-call session location, agent-switch context, `debug agents`, permissions, skills/instructions, abort, and native subagent behavior.
- Migrate disposable copies of every observed V1 shard shape and compare content, not just schema creation.
- Run AL-46, AL-45, and LGREP-43 dispositions and record fixed/changed/unsupported outcomes.

**Executable acceptance:** the lab's existing probe/version and verification commands exit 0 against 2.2.7; every new probe has a failing V1 or negative control and passes on 2.2.7; the migration verifier exits nonzero on a deliberately omitted message and passes on unmodified copies; no live path is writable and the secret scan finds no credential. No production package installation occurs.

### S2. Deliver the Concord V2 adapter and installer

**Owner:** Concord. **Outcome:** one installed V2-only adapter performs the current seven-tool contract against the stamped Go core.

- Implement the smallest design above and regenerate agent contracts/profiles from their manifests.
- Update the host pin, runtime result probe, installer, session-prepare registry parser, eval host, and tests together.
- Propose and approve the implementation contract's exact TS7, CD-0084, CD-0007 D7, and any CD-0111 wording changes before editing code. No Bun-to-Node rewrite is included.
- Ship no Concord store migration in the same release, so V1 recovery can keep the live Concord store.

**Executable acceptance:** `python3 scripts/check-agent-contracts.py` (including host typecheck), `python3 scripts/test-installer.py`, `python3 scripts/test-agent-contracts.py`, `bin/oc-test broad -- env -u CONCORD_SELECTED_PRODUCT_ID bun test adapter/opencode/`, the result probe on its new 2.2.7 route, and instruction/coordinator evals pass. In the S1 lab, every tool operation succeeds as a direct call and the seven tools are absent from the Code Mode catalog; directory moves resolve correctly; abort and oversize cases fail safely; `session-prepare` rejects missing/wrong agents; resolved `permissions` match the generated rules for every agent; and one admitted helper-to-utility subagent call succeeds. Required repository CI passes.

### S3. Qualify provider authentication and model routing

**Owner:** toolbox and OMR. **Outcome:** Claude Max and OMR work together on the exact host without provider-selection changes in Concord.

- Qualify Claude Max `server.js`, OMR's installed V2 package, advisor, provider-hints, and prefix-probe.
- Re-run the observed prefill corpus and keep all Opus `steps` fields unset. Measure cache behavior before reconsidering decision 0013.

**Executable acceptance:** `OMR_V2_BIN=<pinned-binary> scripts/e2e-v2-runtime.sh` and the OMR installed-package test pass; toolbox real-binary provider/advisor suites pass; isolated Opus requests complete with the expected routed model and no secret leakage; the 28-case prefill fixture produces no assistant-ending provider request, while the max-steps negative control remains documented.

### S4. Qualify Vision and MCP refresh behavior

**Owner:** Vision plus its host integration. **Outcome:** the installed Vision plugin and MCP consumers survive ordinary refresh and expired-session recovery at the pinned release.

- Align Vision's plugin SDK and lockfile, exercise VSN-16 controls, and close the VSN-39 dependency evidence.
- Test AL-46 without modifying OpenCode or bypassing Code Mode guards.

**Executable acceptance:** Vision's V2 package loads and its MCP control suite passes against 2.2.7; both audit commands show no GHSA-8988-4f7v-96qf affected package; expired HTTP recovery and immediate mutation/readback tests show current tool schemas and validators, or AL-46 remains explicitly open with the restart fallback and cutover cannot claim that gap closed.

### S5. Qualify the remaining server and project plugins

**Owner:** toolbox, with third-party owners where needed. **Outcome:** every retained configured plugin loads and performs its one host behavior; no inert V1 entry remains.

- Cover project-skills, execute-output, ci-watch, cache-meter, tab-bell, task-delegation, voice-contract, Morph, command provider, oc-run run-context, capacity-context, codemode-signature-guard, and execute-errors.
- Test direct tools and Code Mode deltas, including signature refusal for unseen paths and no refusal after a legitimate catalog update.

**Executable acceptance:** a manifest-driven real-binary suite runs each plugin's existing integration test and records load, one positive behavior, and one negative control. Every configured entry maps to a passing result or an approved removal in the same host change. No plugin is marked passed from TypeScript compilation alone. Loading the converted host configuration yields no unresolved `omitted unsupported legacy setting` diagnostic.

### S6. Qualify Herdr, TUI, and status integration

**Owner:** Herdr and toolbox/zj-radar. **Outcome:** interactive session control and visible status survive the V2 loader and registration changes.

- Install V2 `cli.json` registration, then reinstall Herdr integration. Qualify herdr-agent-state, moshi-hooks, session-state, restart, herdr-tui-session, herdr-opencode TUI, and command provider.
- Update decision 0017 with the new producer contract.

**Executable acceptance:** a fresh V2 session appears in Herdr and zj-radar; state transitions and restart resume the correct session; TUI plugins load without a V1 fallback; partial agent-list responses are refreshed to completion; the full-session native-subagent test passes, replacing the unsupported AL-45 debug probe.

### S7. Migrate host database and operational tooling

**Owner:** toolbox. **Outcome:** wrapper sharding, readers, oc-run, cutover/resume, and restore documentation understand the selected V2 schema and exact pin.

- Port or deliberately retire each V1 SQL reader; make db-maint prove its write path on a migrated copy before any live use.
- Verify `oc --oc-where` and V2 `debug paths db` agree for ordinary and forced-global shards, with and without the shell's relative `OPENCODE_DB` export; never introduce an absolute shared value.
- Update oc-run's deploy fence, oc-cutover, restore playbook, and deploy map as one evidence set.

**Executable acceptance:** schema fixtures and migrated-copy integration tests pass for db-maint, session-cost, stop-audit, and cutover; a deliberate wrong-schema invocation refuses; path identity matches for every fixture shard; `scripts/deploy-oc-run.sh` admits 2.2.7 only after its new runtime suite passes; restore documentation names the matched V1 and V2 recovery bundles without ambiguity.

### S8. Rehearse and execute the controlled host cutover

**Owner:** toolbox host operation with the operator's required production approvals. **Outcome:** one coordinated host runs the qualified stack, with a tested complete V1 recovery route.

- Freeze all slice versions, drain sessions, take final snapshots, perform the final isolated rehearsal, then cut over and run per-shard smoke checks.
- Keep automatic update and background subagents off. Do not couple this to unrelated feature adoption.

**Executable acceptance:** the rehearsal's migration and restore verifiers both exit 0 before production; the cutover checklist records every shard and plugin; post-cutover smoke and one controlled Concord mutation pass; a forced recovery drill restores the matched 1.18.35 environment without opening migrated data; worktrees and active artifacts remain present. Any missing check blocks admission or triggers the recovery procedure.

## Remaining limits and non-goals

This report does not claim V1 history survives, 2.2.7 fixes all prefill failures, upstream MCP issue #39902 is resolved, every toolbox plugin has a V2 implementation, or background children survive parent restart. Those are acceptance questions, not facts to assume away.

The migration does not add a second adapter generation, Node-build support, automatic rollback, a Concord session/metadata store, a new MCP refresh daemon, background-subagent orchestration, native worktree adoption, or a closure-receipt replacement. Later enhancements need separate evidence and scope.
