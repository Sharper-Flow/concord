import { test, expect } from "bun:test"
import { Database } from "bun:sqlite"
import { createPrivateKey, createPublicKey } from "node:crypto"
import { mkdir, mkdtemp, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { configureConcordAdapter, invokeConcordOperation, laneDispatchRequest } from "./concord"
import { configureCoreBinary } from "./dispatch"

// The route test drives the real core through its own runner, so argv[0] is
// replaced there. Bind the nominal path the transport resolves instead of the
// unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")
import { completeDispatchedWorker } from "./lane_completion"
import { dispatchLaneWorker } from "./lane_dispatch"
import { DispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import type { CredentialStore } from "./credentials"
import type { DispatchRunner } from "./dispatch"
import { agentLanes } from "./generated-agent-lanes"
import { hostControlPlane, MANAGED_TASK_SCOPE_KEY, SESSION_MESSAGES_ROUTE, SESSION_ROUTE } from "./move-session"

const PRODUCT_ID = "product-e2e"
const PROJECT_ID = "project-e2e"
const CLIENT_REF = "opencode"
const SESSION_ID = "e2e-session"
const MESSAGE_ID = "e2e-message"
const READBACK_MODEL = "openai/gpt-5.6-luna"
const PRIVATE_SEED = new Uint8Array(32).fill(7)
const PRIVATE_KEY_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex")
const WORKFLOW_PREDICATE = {
  predicate_id: "predicate:route-e2e-passes",
  ordinal: 0,
  outcome_kind: "check",
  outcome_payload: {
    kind: "check",
    check_ref: "check:bun-test/adapter/opencode/dispatch_route_end_to_end.test.ts",
    expected_result: "pass",
    immutable_subject_ref: "commit:f076cef390c13944b831b9024334b291a435588b",
  },
}
// #903/#904 shared regression: the approved objective is a concrete requested
// change, while the predicate above already passes on the unmodified baseline.
// A worker that satisfies only the predicate has not delivered the objective,
// so the packet must carry both and keep them distinct.
const APPROVED_OBJECTIVE = "Add the session marker docs/dispatch-marker.txt describing the shipped route."

type JSONRecord = Record<string, any>

function privateKeyObject() {
  return createPrivateKey({ key: Buffer.concat([PRIVATE_KEY_PREFIX, Buffer.from(PRIVATE_SEED)]), format: "der", type: "pkcs8" })
}

function publicKeyBase64(): string {
  return createPublicKey(privateKeyObject()).export({ format: "der", type: "spki" }).subarray(-32).toString("base64")
}

async function runProcess(argv: string[], input = "", cwd?: string, env: Record<string, string> = {}): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  const child = Bun.spawn(argv, { cwd, env: { ...process.env, ...env }, stdin: "pipe", stdout: "pipe", stderr: "pipe" })
  await child.stdin.write(input)
  await child.stdin.end()
  const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
  return { exitCode, stdout, stderr }
}

async function runJSON(binary: string, dbPath: string, command: string, value: JSONRecord, cwd?: string): Promise<JSONRecord> {
  const result = await runProcess([binary, command], JSON.stringify(value), cwd, { CONCORD_DB_PATH: dbPath })
  expect(result.exitCode, `${command}: ${result.stderr}`).toBe(0)
  const lines = result.stdout.trim().split("\n").filter(Boolean)
  expect(lines).toHaveLength(1)
  return JSON.parse(lines[0]) as JSONRecord
}

async function git(cwd: string, ...args: string[]): Promise<void> {
  const result = await runProcess(["git", ...args], "", cwd)
  expect(result.exitCode, `git ${args.join(" ")}: ${result.stderr}`).toBe(0)
}

function dbValue(dbPath: string, sql: string): any {
  const db = new Database(dbPath, { readonly: true })
  try {
    return db.query(sql).get()
  } finally {
    db.close()
  }
}

function dbRows(dbPath: string, sql: string): any[] {
  const db = new Database(dbPath, { readonly: true })
  try {
    return db.query(sql).all()
  } finally {
    db.close()
  }
}


function seedInvestigationArtifact(dbPath: string, workID: string): void {
  const db = new Database(dbPath)
  try {
    db.run("INSERT INTO fold_guard(active) VALUES(1)")
    const registryHash = "sha256:" + "e".repeat(64)
    db.run("INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('e2e-locator',?,'canonical_path','/fixture','/fixture','2026-09-09T00:00:00Z','2026-09-09T00:00:00Z')", [PROJECT_ID])
    db.run("INSERT OR IGNORE INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES(?,?,'e2e-locator',?,'root','1.0',?,'test')", [PRODUCT_ID, PROJECT_ID, PRODUCT_ID, registryHash])
    db.run("INSERT OR IGNORE INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES(?,'e2e-locator',?,'root','Root','Fixture Domain','current',?,'test')", [PROJECT_ID, PRODUCT_ID, registryHash])
    db.run("INSERT OR IGNORE INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','Investigation comparison','needed',0,1,'2026-09-09T00:00:00Z','2026-09-09T00:00:00Z')", [`${workID}-compared`])
    db.run("INSERT OR IGNORE INTO work_projects(work_id,project_id,role) VALUES(?,?,'secondary')", [`${workID}-compared`, PROJECT_ID])
    const investigationRefs = JSON.stringify(["root", `${workID}-compared`])
    db.run("INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) VALUES(?,?,?,?,?,'2026-09-09T00:00:00Z')", ["obs:" + workID.slice(-12) + "cmp0", workID, "investigation before question", investigationRefs, "[]"])
    db.run("DELETE FROM fold_guard")
  } finally {
    db.close()
  }
}

function contextFor(directory: string) {
  return {
    sessionID: SESSION_ID,
    messageID: MESSAGE_ID,
    agent: "concord-implement",
    directory,
    worktree: directory,
    abort: new AbortController().signal,
    metadata() {},
    ask: async () => {},
  } as any
}

function taskResult(report: JSONRecord): string {
  return [
    `<task id="worker-session" state="completed">`,
    "<task_result>",
    JSON.stringify(report),
    "</task_result>",
    "</task>",
  ].join("\n")
}

// CD-0102: an authorized worker session opens with the dispatch packet as its
// first user message, so the fixture takes the bound packet once the test
// captures it from the rewritten Task call.
function exportedSession(opening: JSONRecord | null = null, bulkTextBytes = 0): string {
  return JSON.stringify({
    info: { id: "worker-session" },
    messages: [
      ...(opening ? [{ info: { id: "worker-message-open", sessionID: "worker-session", role: "user", agent: "concord-implement", time: { created: 0 } }, parts: [{ type: "text", text: JSON.stringify(opening) }] }] : []),
      ...(bulkTextBytes > 0 ? [{ info: { id: "worker-message-bulk", sessionID: "worker-session", role: "user", agent: "concord-implement", time: { created: 0.5 } }, parts: [{ type: "text", text: "x".repeat(bulkTextBytes) }] }] : []),
      { info: { id: "worker-message", sessionID: "worker-session", role: "assistant", agent: "concord-implement", providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] },
    ],
  })
}

function requiredFields(value: JSONRecord, fields: string[]): void {
  for (const field of fields) expect(value[field], `missing ${field}`).toBeDefined()
}

// The route test drives the real `concord` binary against a real store. A
// clean checkout has no binary on PATH; declare the test skipped when neither
// CONCORD_BIN nor a Go toolchain can produce one, so a suite run without the
// toolchain reports a skip, never a silent pass.
const routeDeclaration =
  process.env.CONCORD_BIN || Bun.spawnSync(["go", "version"]).exitCode === 0
    ? test
    : test.skip

interface RouteFixture {
  binary: string
  repo: string
  dbPath: string
  configPath: string
  workID: string
  worktree: string
  lane: (typeof agentLanes)[number]
}

// bootRouteFixture builds the real store and repository fixture every route
// test shares: one built core binary, one real git repository with the
// synthetic knowledge home, one bootstrapped work item, and one registered
// client carrying the worker evidence and dispatch capabilities. The host
// control-plane binding and the transport runner stay per-test, because each
// scenario answers the host session routes differently.
async function bootRouteFixture(root: string): Promise<RouteFixture> {
  let binRoot = ""
  let binary = process.env.CONCORD_BIN ?? ""
  if (!binary) {
    binRoot = join(root, "bin")
    await mkdir(binRoot)
    binary = join(binRoot, "concord")
    const build = await runProcess(["go", "build", "-o", binary, "./cmd/concord"], "", join(import.meta.dir, "..", ".."))
    expect(build.exitCode, `go build: ${build.stderr}`).toBe(0)
  }
  const repo = join(root, "repo")
  const dbPath = join(root, "concord.db")
  const configPath = join(repo, "opencode.jsonc")
  const lane = agentLanes.find((candidate) => candidate.id === "implement")
  if (!lane) throw new Error("implement lane is not registered")

  await mkdir(join(repo, "docs"), { recursive: true })
  await Bun.write(join(repo, "docs", "concord-knowledge-index.v1.json"), JSON.stringify({
    schema_version: "1.2",
    supported_kinds: [],
    indexed_kinds: [],
    knowledge_roots: [],
    domain_registry: {
      schema_version: "1.0",
      product_key: PRODUCT_ID,
      root_domain_id: `product-root:${PRODUCT_ID}`,
      domains: [{ domain_id: `product-root:${PRODUCT_ID}`, name: "Synthetic root", purpose: "Synthetic test domain", status: "current", architecture_relations: [] }],
    },
    records: [],
  }, null, 2))
  await Bun.write(configPath, JSON.stringify({ instructions: ["https://example.invalid/synthetic-instructions"] }))
  await Bun.write(join(repo, "README.md"), "synthetic dispatch fixture\n")
  await git(repo, "init", "--quiet", "--initial-branch=main")
  await git(repo, "config", "user.email", "test@example.invalid")
  await git(repo, "config", "user.name", "Synthetic Test")
  await git(repo, "add", ".")
  await git(repo, "commit", "--quiet", "-m", "fixture")
  await git(repo, "remote", "add", "origin", "https://example.invalid/synthetic.git")
  await git(repo, "update-ref", "refs/remotes/origin/main", "HEAD")
  await git(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

  await runJSON(binary, dbPath, "product-create", {
    product_id: PRODUCT_ID,
    display_name: "Synthetic Product",
    stage_maturity: "prototype",
    stage_audience_commitment: "operator_only",
    project_id: PROJECT_ID,
    project_display_name: "Synthetic Project",
    role: "primary",
  })
  await runJSON(binary, dbPath, "project-locator-add", { project_id: PROJECT_ID, locator_id: "repo", kind: "canonical_path", value: repo, expected_version: 1 })
  await runJSON(binary, dbPath, "product-knowledge-home-designate", { product_id: PRODUCT_ID, project_id: PROJECT_ID, locator_id: "repo", expected_version: 2 })
  const bootstrap = await runJSON(binary, dbPath, "work-bootstrap", {
    product_id: PRODUCT_ID,
    project_id: PROJECT_ID,
    title: "Synthetic dispatch route",
    value_statement: "The route completes a real worker attempt.",
    kind: "bug",
    task: "Exercise the dispatch route.",
    idempotency_key: "dispatch-route-e2e",
    priority: 1,
    urgency: "standard",
    tags: [],
    workflow_type_ref: "",
    external_ref: "issue-840",
    governing_requirements: [],
    ref: "HEAD",
  }, repo)
  await runJSON(binary, dbPath, "client-register", {
    client_ref: CLIENT_REF,
    key_id: "dispatch-e2e-key",
    principal_ref: "operator-1",
    public_key: publicKeyBase64(),
    capabilities: ["product_read", "work_define", "work_transition", "work_relate", "work_compact", "worker_evidence", "worker_dispatch"],
    product_scope: [PRODUCT_ID],
    project_scope: [PROJECT_ID],
    agent_scope: ["concord-implement", "concord-review"],
  })
  return { binary, repo, dbPath, configPath, workID: bootstrap.work_id as string, worktree: bootstrap.worktree.path as string, lane }
}

// realStoreRunner spawns the real core binary against the real store and
// records the evidence verbs the route observes. This is the transport the
// review demanded: no accepting stub stands between the adapter and the core
// on the evidence path. The `session` leg stays a host-shaped stub because it
// names the `opencode` CLI, not the core, and the host control plane answers
// the liveness read when it is bound.
function realStoreRunner(binary: string, dbPath: string, realCalls: Array<{ argv: string[]; input: JSONRecord }>, worktree: string): DispatchRunner {
  return {
    async run(argv, input, signal) {
      if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "worker-session", directory: worktree }, { id: SESSION_ID, directory: worktree }]), stderr: "" }
      if (argv[1] === "worker-dispatch" || argv[1] === "worker-complete" || argv[1] === "worker-fail" || argv[1] === "invoke") {
        realCalls.push({ argv, input: JSON.parse(input) as JSONRecord })
      }
      const child = Bun.spawn([binary, ...argv.slice(1)], { env: { ...process.env, CONCORD_DB_PATH: dbPath }, stdin: "pipe", stdout: "pipe", stderr: "pipe" })
      if (signal.aborted) child.kill()
      await child.stdin.write(input)
      await child.stdin.end()
      const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
      return { exitCode, stdout, stderr }
    },
  }
}

// driveWorkflowToContract walks the real workflow from capture to the
// approved-contract state that makes dispatch_worker the next action, using
// the transition sequence every route test shares.
async function driveWorkflowToContract(
  workID: string,
  invoke: (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) => Promise<any>,
  context: any,
): Promise<void> {
  const transition = (version: number, actionID: string, idempotencyKey: string, fields: Record<string, unknown>) => invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version, action_id: actionID, idempotency_key: idempotencyKey, fields } }, context)
  let response = await transition(5, "record_reproduction", "e2e-reproduction", {})
  expect(response.outcome).toBe("ok")
  response = await transition(7, "record_alignment", "e2e-alignment", { searched: "Searched the backlog for duplicate defect work.", outcome: "none_found" })
  expect(response.outcome).toBe("ok")
  response = await transition(9, "record_root_cause", "e2e-root-cause", {})
  expect(response.outcome).toBe("ok")
  const domainList = await invoke("concord_domain", { operation: "list", input: { product_id: PRODUCT_ID, page: { cursor: null, limit: 10 } } }, context)
  expect(domainList.outcome).toBe("ok")
  const registry = domainList.result as JSONRecord
  const registryHash = (registry.registry as JSONRecord).content_hash as string
  response = await transition(10, "approve_contract", "e2e-approve-contract", {
    premise: APPROVED_OBJECTIVE,
    outcome_predicates: [WORKFLOW_PREDICATE],
    required_evidence: [],
    route_conventions: [],
    spec_mandate: [],
    law_modifies: [],
    architecture_binding: {
      domain_registry_content_hash: registryHash,
      home_domain_id: `product-root:${PRODUCT_ID}`,
      affected_domain_ids: [`product-root:${PRODUCT_ID}`],
      domain_modifies: [],
      domain_relation_modifies: [],
      law_additions: [],
      verification_obligations: [],
    },
  })
  expect(response.outcome).toBe("ok")
  const stepRead = (await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID, page: { cursor: null, limit: 1 } } }, context)).result as JSONRecord
  expect((stepRead.pinned as JSONRecord).workflow_step).toBe("repair")
}

routeDeclaration("dispatches a real store route through Task completion and workflow gates", async () => {
  const root = await mkdtemp(join(tmpdir(), "concord-dispatch-e2e-"))
  const previousConfig = process.env.OPENCODE_CONFIG
  try {
    const { binary, repo, dbPath, configPath, workID, worktree, lane } = await bootRouteFixture(root)
    process.env.OPENCODE_CONFIG = configPath
    const context = contextFor(worktree)
    let sessionMetadata: Record<string, unknown> = {}
    let boundPacket: JSONRecord | null = null
    hostControlPlane().bind({
      get: async ({ url, path }) => {
        if (url === SESSION_MESSAGES_ROUTE) {
          // The worker readback serves the transcript through the bounded
          // message page: the opening packet, then the assistant identity.
          const id = path?.id
          expect(id).toBe("worker-session")
          const parsed = JSON.parse(exportedSession(boundPacket)) as { messages: unknown[] }
          return { data: parsed.messages, response: new Response("[]", { status: 200 }) }
        }
        expect(url).toBe(SESSION_ROUTE)
        const id = path?.id
        expect(id === SESSION_ID || id === "worker-session").toBe(true)
        return { data: { id, directory: worktree, metadata: id === SESSION_ID ? sessionMetadata : {}, ...(id === "worker-session" ? { parentID: SESSION_ID } : {}) }, response: new Response(null, { status: 200 }) }
      },
      patch: async ({ url, path, body }) => {
        expect(url).toBe(SESSION_ROUTE)
        expect(path).toEqual({ id: SESSION_ID })
        expect(body).toEqual({ metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } })
        sessionMetadata = { [MANAGED_TASK_SCOPE_KEY]: "managed" }
        return { response: new Response(null, { status: 200 }) }
      },
      post: async () => { throw new Error("dispatch scope must not change host permissions or directory") },
    })
    const realCalls: Array<{ argv: string[]; input: JSONRecord }> = []
    const realRunner: DispatchRunner = {
      async run(argv, input, signal) {
        if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "worker-session", directory: worktree, parentID: SESSION_ID }, { id: SESSION_ID, directory: worktree }]), stderr: "" }
        if (argv[1] === "worker-dispatch" || argv[1] === "worker-complete" || argv[1] === "worker-fail" || argv[1] === "invoke") {
          realCalls.push({ argv, input: JSON.parse(input) as JSONRecord })
        }
        const child = Bun.spawn([binary, ...argv.slice(1)], { env: { ...process.env, CONCORD_DB_PATH: dbPath }, stdin: "pipe", stdout: "pipe", stderr: "pipe" })
        if (signal.aborted) child.kill()
        await child.stdin.write(input)
        await child.stdin.end()
        const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
        return { exitCode, stdout, stderr }
      },
    }
    configureConcordAdapter({ runner: realRunner })

    const invoke = (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) => {
      if (toolName === "concord_work_transition" && args.input.action_id === "dispatch_worker") expect(sessionDirectory).toBe(worktree)
      return invokeConcordOperation(toolName, args as any, callContext, sessionDirectory)
    }
    const transition = (version: number, actionID: string, idempotencyKey: string, fields: Record<string, unknown>) => invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version, action_id: actionID, idempotency_key: idempotencyKey, fields } }, context)

    await driveWorkflowToContract(workID, invoke, context)

    let response: JSONRecord
    const routed = laneDispatchRequest({ operation: "workflow_action", input: { work_id: workID, expected_version: 12, action_id: "dispatch_worker", idempotency_key: "e2e-dispatch", fields: { lane_id: "implement" } } })
    expect(routed).toEqual({ work_id: workID, expected_version: 12, idempotency_key: "e2e-dispatch", lane_id: "implement" })
    const windows = new DispatchWindows()
    let dispatchResponse: JSONRecord | undefined
    const dispatchResult = await dispatchLaneWorker(routed as any, {
      context,
      invoke: async (toolName, args, callContext, sessionDirectory) => {
        if (toolName === "concord_work_transition" && args.input.action_id === "dispatch_worker") {
          expect(sessionMetadata).toEqual({ [MANAGED_TASK_SCOPE_KEY]: "managed" })
        }
        const result = await invoke(toolName, args, callContext, sessionDirectory)
        if (toolName === "concord_work_transition" && args.input.action_id === "dispatch_worker") dispatchResponse = result
        return result
      },
      credentials: { async getPrivateKey() { return PRIVATE_SEED } } satisfies CredentialStore,
      windows,
    })
    expect(dispatchResult.outcome).toBe("ok")
    expect(dispatchResult.dispatch_state).toBe("awaiting_worker")
    expect(await hostControlPlane().taskScope(SESSION_ID)).toBe("managed")
    expect(await hostControlPlane().taskScope("worker-session")).toBe("managed")
    expect(windows.has(SESSION_ID)).toBe(true)
    const taskArgs: Record<string, unknown> = { subagent_type: "general", prompt: "model input", description: "model task" }
    await windows.bind(TASK_TOOL_ID, SESSION_ID, taskArgs, undefined, async () => worktree)
    const packet = JSON.parse(taskArgs.prompt as string) as JSONRecord
    boundPacket = packet
    expect(taskArgs.subagent_type).toBe("concord-implement")
    expect(packet.step_id).toBe("repair")
    // This non-Initiative fixture has no narrative. The task still carries
    // the objective and version binding; numbered constraints carry the
    // mandate. The context carries the contract's resolved home Domain.
    expect(packet.inputs.context).toBe(`Approved law and Domains (binding Product law):\n- Domain product-root:${PRODUCT_ID}: Synthetic root — Synthetic test domain\n\n`)
    expect(packet.inputs.task).toContain("Approved objective:")
    expect(packet.inputs.task).toContain(APPROVED_OBJECTIVE)
    expect(packet.inputs.task).toContain("(work v12, contract v1)")
    expect(packet.inputs.task).not.toContain(WORKFLOW_PREDICATE.predicate_id)
    const mandate = packet.inputs.constraints
      .filter((entry: string) => entry.startsWith("Approved end-state mandate (join parts in order) "))
      .map((entry: string) => entry.slice(entry.indexOf(": ") + 2)).join("")
    const projectedPredicates = JSON.parse(mandate).map((predicate: { outcome_payload: string }) => ({
      ...predicate,
      outcome_payload: JSON.parse(predicate.outcome_payload),
    }))
    expect(projectedPredicates).toEqual([WORKFLOW_PREDICATE])
    expect(dispatchResponse?.result?.worker_packet_digest).toMatch(/^sha256:[0-9a-f]{64}$/)
    const report = {
      schema_version: "1.0",
      readback_model: READBACK_MODEL,
      status: "completed",
      evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `discharged ${obligation}` })),
    }
    const completionOutput = { title: "task", output: taskResult(report), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION_ID, callID: "e2e-task-call", args: taskArgs }, completionOutput, { windows, credentials: { async getPrivateKey() { return PRIVATE_SEED } }, runner: realRunner, concordBinary: binary })
    expect(completionOutput.output).toContain("<concord_attempt>")
    const attempt = dbValue(dbPath, `SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id='${packet.attempt_id}'`)
    expect(attempt.lifecycle_state).toBe("completed")
    expect(attempt.readback_model).toBe(READBACK_MODEL)
    const provenance = dbRows(dbPath, `SELECT payload FROM domain_events WHERE kind='worker.dispatched' AND subject_id='${workID}' ORDER BY seq DESC LIMIT 1`)
    const provenancePayload = JSON.parse(provenance[0].payload as string)
    expect(provenancePayload.host_provenance.sources).toContainEqual({ kind: "unenumerated", path: "https://example.invalid/synthetic-instructions" })

    const evidenceVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    response = await transition(evidenceVersion, "bind_evidence", "e2e-bind-evidence", { evidence_kind: "verification" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const currentVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    response = await transition(currentVersion, "accept_worker_result", "e2e-accept-worker", { attempt_id: packet.attempt_id, attempt_epoch: 1 })
    expect(response.outcome).toBe("ok")
    const refineStartVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    response = await transition(refineStartVersion, "start_refine", "e2e-start-refine", {})
    expect(response.outcome).toBe("ok")
    expect(dbValue(dbPath, `SELECT definition_version FROM workflow_instances WHERE work_id='${workID}'`).definition_version).toBe(14)
    expect(dbValue(dbPath, `SELECT current_step FROM workflow_instances WHERE work_id='${workID}'`).current_step).toBe("refine")
    const refineEvidenceVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    response = await transition(refineEvidenceVersion, "bind_evidence", "e2e-bind-refine-artifact", { evidence_kind: "artifact" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const refineDeliveryVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    response = await transition(refineDeliveryVersion, "record_delivery", "e2e-record-refine-delivery", { delivery_artifact: "docs/dispatch-marker.txt", delivery_state: "asserted" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const gateVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    expect(dbValue(dbPath, `SELECT current_step FROM workflow_instances WHERE work_id='${workID}'`).current_step).toBe("delivery")
    response = await transition(gateVersion, "record_delivery", "e2e-record-gate-delivery", { delivery_artifact: "docs/dispatch-marker.txt", delivery_state: "asserted" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    expect(dbValue(dbPath, `SELECT current_step FROM workflow_instances WHERE work_id='${workID}'`).current_step).toBe("verify")
    const verifyVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    // CD-0116 after a lane exit: the session that accepted the worker result
    // submits its own verdict, the adapter mints the operator challenge, the
    // host approval signs it, and the verdict records under the operator
    // identity rather than a distinct agent session.
    response = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: verifyVersion, action_id: "record_verdict", idempotency_key: "e2e-record-verdict", fields: { contract_version: 1, predicate_id: WORKFLOW_PREDICATE.predicate_id, evaluation_evidence: [packet.attempt_id] } } }, context)
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const verdictActor = dbValue(dbPath, `SELECT json_extract(payload,'$.verdict_actor_ref') AS actor FROM domain_events WHERE subject_id='${workID}' AND kind='workflow.verdict_recorded' ORDER BY seq DESC LIMIT 1`).actor as string
    const verdictActorClass = dbValue(dbPath, `SELECT actor_class FROM workflow_actors WHERE actor_ref='${verdictActor}'`).actor_class as string
    expect(verdictActorClass).toBe("operator")
    const verdictVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    // The operator question gate requires an investigation observation naming
    // a current Domain of the Product and another work item.
    seedInvestigationArtifact(dbPath, workID)
    const continuity = await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID, page: { cursor: null, limit: 1 } } }, context)
    const continuityResult = continuity.result as JSONRecord
    const decisionDigest = (((continuityResult.pinned as JSONRecord).pending_operator_decision as JSONRecord).decision_context_digest) as string
    response = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: verdictVersion, action_id: "confirm_premise", selected_choice: "confirm", decision_context_digest: decisionDigest, idempotency_key: "e2e-confirm" } }, context)
    expect(response.outcome).toBe("ok")
    const completeVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    response = await transition(completeVersion, "complete", "e2e-complete", { impact_verdict: "non-breaking" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    expect(dbValue(dbPath, `SELECT instance_state FROM workflow_instances WHERE work_id='${workID}'`).instance_state).toBe("completed")
    const verdicts = dbRows(dbPath, `SELECT payload FROM domain_events WHERE subject_id='${workID}' AND kind='workflow.verdict_recorded'`)
    expect(verdicts.map((row) => JSON.parse(row.payload as string).predicate_id)).toContain(WORKFLOW_PREDICATE.predicate_id)
    const workerDispatch = realCalls.find((call) => call.argv[1] === "worker-dispatch")?.input
    const workerComplete = realCalls.find((call) => call.argv[1] === "worker-complete")?.input
    requiredFields(workerDispatch ?? {}, ["event_id", "work_id", "attempt_id", "lane_id", "lane_version", "lane_digest", "packet_schema_version", "report_schema_version", "packet_digest", "host_provenance", "assertion"])
    requiredFields(workerComplete ?? {}, ["event_id", "work_id", "attempt_id", "readback_model", "report_schema_version", "evidence_origin", "evidence", "assertion"])
    const invokeCalls = realCalls.filter((call) => call.argv[1] === "invoke")
    for (const call of invokeCalls) {
      requiredFields(call.input.call_envelope, ["schema_version", "request_id", "client_ref", "principal_ref", "session_ref", "agent_ref", "directory", "worktree", "ambient_project_id", "scope_version", "manifest_digest"])
      expect(call.input.call_envelope.directory).toBe(worktree)
      expect(call.input.call_envelope.worktree).toBe(worktree)
    }
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    if (previousConfig === undefined) delete process.env.OPENCODE_CONFIG
    else process.env.OPENCODE_CONFIG = previousConfig
    await rm(root, { recursive: true, force: true })
  }
}, 120_000)

// The oversized-session completion is verified against the real core and
// store: a transcript past the fixed 8 MiB export bound the readback once
// crossed must complete through the bounded message pages, and the evidence
// verbs must land on the real CLI and its worker_attempts row.
routeDeclaration("records an oversized worker session through the real CLI and store", async () => {
  const root = await mkdtemp(join(tmpdir(), "concord-dispatch-oversized-"))
  const previousConfig = process.env.OPENCODE_CONFIG
  try {
    const { binary, dbPath, configPath, workID, worktree, lane } = await bootRouteFixture(root)
    process.env.OPENCODE_CONFIG = configPath
    const context = contextFor(worktree)
    let boundPacket: JSONRecord | null = null
    const BULK_TEXT_BYTES = 14_000_000
    let sessionMetadata: Record<string, unknown> = {}
    hostControlPlane().bind({
      get: async ({ url, path }) => {
        if (url === SESSION_MESSAGES_ROUTE) {
          const parsed = JSON.parse(exportedSession(boundPacket, BULK_TEXT_BYTES)) as { messages: unknown[] }
          return { data: parsed.messages, response: new Response("[]", { status: 200 }) }
        }
        expect(url).toBe(SESSION_ROUTE)
        const id = path?.id
        expect(id === SESSION_ID || id === "worker-session").toBe(true)
        return { data: { id, directory: worktree, metadata: id === SESSION_ID ? sessionMetadata : {}, ...(id === "worker-session" ? { parentID: SESSION_ID } : {}) }, response: new Response(null, { status: 200 }) }
      },
      patch: async ({ url, path, body }) => {
        expect(url).toBe(SESSION_ROUTE)
        expect(path).toEqual({ id: SESSION_ID })
        expect(body).toEqual({ metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } })
        sessionMetadata = { [MANAGED_TASK_SCOPE_KEY]: "managed" }
        return { response: new Response(null, { status: 200 }) }
      },
      post: async () => { throw new Error("dispatch scope must not change host permissions or directory") },
    })
    const realCalls: Array<{ argv: string[]; input: JSONRecord }> = []
    const realRunner = realStoreRunner(binary, dbPath, realCalls, worktree)
    configureConcordAdapter({ runner: realRunner })

    const invoke = (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) =>
      invokeConcordOperation(toolName, args as any, callContext, sessionDirectory)
    await driveWorkflowToContract(workID, invoke, context)
    const routed = laneDispatchRequest({ operation: "workflow_action", input: { work_id: workID, expected_version: 12, action_id: "dispatch_worker", idempotency_key: "e2e-oversized-dispatch", fields: { lane_id: "implement" } } })
    const windows = new DispatchWindows()
    const dispatchResult = await dispatchLaneWorker(routed as any, {
      context, invoke,
      credentials: { async getPrivateKey() { return PRIVATE_SEED } } satisfies CredentialStore,
      windows,
    })
    expect(dispatchResult.outcome).toBe("ok")
    const taskArgs: Record<string, unknown> = { subagent_type: "general", prompt: "model input", description: "model task" }
    await windows.bind(TASK_TOOL_ID, SESSION_ID, taskArgs, undefined, async () => worktree)
    const packet = JSON.parse(taskArgs.prompt as string) as JSONRecord
    boundPacket = packet
    // The transcript the readback walks crosses the fixed 8 MiB export bound
    // that born the failure under repair.
    expect(Buffer.byteLength(exportedSession(packet, BULK_TEXT_BYTES))).toBeGreaterThan(8_388_608)
    const report = {
      schema_version: "1.0",
      readback_model: READBACK_MODEL,
      status: "completed",
      evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `discharged ${obligation}` })),
    }
    const completionOutput = { title: "task", output: taskResult(report), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION_ID, callID: "e2e-oversized-call", args: taskArgs }, completionOutput, { windows, credentials: { async getPrivateKey() { return PRIVATE_SEED } }, runner: realRunner, concordBinary: binary })
    expect(completionOutput.output).toContain('"outcome":"ok"')
    const attempt = dbValue(dbPath, `SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id='${packet.attempt_id}'`)
    expect(attempt.lifecycle_state).toBe("completed")
    expect(attempt.readback_model).toBe(READBACK_MODEL)
    const verbs = realCalls.filter((call) => call.argv[1] === "worker-dispatch" || call.argv[1] === "worker-complete").map((call) => call.argv[1])
    expect(verbs).toEqual(["worker-dispatch", "worker-complete"])
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    if (previousConfig === undefined) delete process.env.OPENCODE_CONFIG
    else process.env.OPENCODE_CONFIG = previousConfig
    await rm(root, { recursive: true, force: true })
  }
}, 120_000)

// The born-failed recording is verified against the real core and store: a
// refused session read must leave one durable failed attempt row written by
// the real CLI, with the refusal diagnosable from the store alone — the exact
// surface that once answered 'worker evidence assertion timestamp invalid'
// and left no attempt row.
routeDeclaration("records a refused readback as a durable failed attempt through the real CLI and store", async () => {
  const root = await mkdtemp(join(tmpdir(), "concord-dispatch-refused-"))
  const previousConfig = process.env.OPENCODE_CONFIG
  try {
    const { binary, dbPath, configPath, workID, worktree, lane } = await bootRouteFixture(root)
    process.env.OPENCODE_CONFIG = configPath
    const context = contextFor(worktree)
    // The host session read fails only at completion time: the dispatch flow
    // reads the same session routes, and the refusal under test is the
    // readback, not the dispatch.
    let refuseWorkerSessionRead = false
    let sessionMetadata: Record<string, unknown> = {}
    hostControlPlane().bind({
      get: async ({ url, path }) => {
        expect(url).toBe(SESSION_ROUTE)
        const id = path?.id
        if (id === "worker-session" && refuseWorkerSessionRead) {
          return { data: { id }, response: new Response("session read unavailable", { status: 500 }) }
        }
        expect(id === SESSION_ID || id === "worker-session").toBe(true)
        return { data: { id, directory: worktree, metadata: id === SESSION_ID ? sessionMetadata : {}, ...(id === "worker-session" ? { parentID: SESSION_ID } : {}) }, response: new Response(null, { status: 200 }) }
      },
      patch: async ({ url, path, body }) => {
        expect(url).toBe(SESSION_ROUTE)
        expect(path).toEqual({ id: SESSION_ID })
        expect(body).toEqual({ metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } })
        sessionMetadata = { [MANAGED_TASK_SCOPE_KEY]: "managed" }
        return { response: new Response(null, { status: 200 }) }
      },
      post: async () => { throw new Error("dispatch scope must not change host permissions or directory") },
    })
    const realCalls: Array<{ argv: string[]; input: JSONRecord }> = []
    const realRunner = realStoreRunner(binary, dbPath, realCalls, worktree)
    configureConcordAdapter({ runner: realRunner })

    const invoke = (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) =>
      invokeConcordOperation(toolName, args as any, callContext, sessionDirectory)
    await driveWorkflowToContract(workID, invoke, context)
    const routed = laneDispatchRequest({ operation: "workflow_action", input: { work_id: workID, expected_version: 12, action_id: "dispatch_worker", idempotency_key: "e2e-refused-dispatch", fields: { lane_id: "implement" } } })
    const windows = new DispatchWindows()
    const dispatchResult = await dispatchLaneWorker(routed as any, {
      context, invoke,
      credentials: { async getPrivateKey() { return PRIVATE_SEED } } satisfies CredentialStore,
      windows,
    })
    expect(dispatchResult.outcome).toBe("ok")
    const taskArgs: Record<string, unknown> = { subagent_type: "general", prompt: "model input", description: "model task" }
    await windows.bind(TASK_TOOL_ID, SESSION_ID, taskArgs, undefined, async () => worktree)
    const packet = JSON.parse(taskArgs.prompt as string) as JSONRecord
    refuseWorkerSessionRead = true
    const report = {
      schema_version: "1.0",
      readback_model: READBACK_MODEL,
      status: "completed",
      evidence: lane.evidence_obligations.map((obligation) => ({ obligation, detail: `discharged ${obligation}` })),
    }
    const completionOutput = { title: "task", output: taskResult(report), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION_ID, callID: "e2e-refused-call", args: taskArgs }, completionOutput, { windows, credentials: { async getPrivateKey() { return PRIVATE_SEED } }, runner: realRunner, concordBinary: binary })
    expect(completionOutput.output).toContain('"outcome":"error"')
    expect(completionOutput.output).toContain("readback_refusal")
    // The born-failed dispatch write durably records the attempt as failed:
    // an attempt row exists, its failure is typed, and the refusal detail is
    // readable from the store alone.
    const attempt = dbValue(dbPath, `SELECT lifecycle_state,failure_kind,readback_model,failure_detail FROM worker_attempts WHERE attempt_id='${packet.attempt_id}'`)
    expect(attempt.lifecycle_state).toBe("failed")
    expect(attempt.failure_kind).toBe("model_readback_missing")
    expect(attempt.readback_model).toBe("")
    expect(attempt.failure_detail).toContain("readback predicate session_read refused")
    // Only the born-failed dispatch write happened; no terminal completion
    // evidence was signed or written.
    const verbs = realCalls.filter((call) => call.argv[1] === "worker-dispatch" || call.argv[1] === "worker-complete" || call.argv[1] === "worker-fail").map((call) => call.argv[1])
    expect(verbs).toEqual(["worker-dispatch"])
    const bornFailed = realCalls.find((call) => call.argv[1] === "worker-dispatch")?.input
    expect(bornFailed?.terminal).toBe("failed")
    expect(bornFailed?.terminal_failure_kind).toBe("model_readback_missing")
    const assertion = (bornFailed?.assertion ?? {}) as JSONRecord
    expect(assertion.readback_model).toBe("")
    expect(typeof assertion.signature).toBe("string")
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    if (previousConfig === undefined) delete process.env.OPENCODE_CONFIG
    else process.env.OPENCODE_CONFIG = previousConfig
    await rm(root, { recursive: true, force: true })
  }
}, 120_000)
