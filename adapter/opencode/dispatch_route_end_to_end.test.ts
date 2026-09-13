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
import { hostControlPlane, MANAGED_TASK_SCOPE_KEY, SESSION_ROUTE } from "./move-session"

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

function exportedSession(agent = "concord-implement"): string {
  return JSON.stringify({
    info: { id: "worker-session" },
        messages: [{ info: { id: "worker-message", sessionID: "worker-session", role: "assistant", agent, providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] }],
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

routeDeclaration("dispatches a real store route through Task completion and workflow gates", async () => {
  const root = await mkdtemp(join(tmpdir(), "concord-dispatch-e2e-"))
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
  const previousConfig = process.env.OPENCODE_CONFIG
  const lane = agentLanes.find((candidate) => candidate.id === "implement")
  const reviewLane = agentLanes.find((candidate) => candidate.id === "review")
  if (!lane || !reviewLane) throw new Error("implement and review lanes are not registered")
  let activeAgent = "concord-implement"

  try {
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
    const workID = bootstrap.work_id as string
    const worktree = bootstrap.worktree.path as string
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

    process.env.OPENCODE_CONFIG = configPath
    const context = contextFor(worktree)
    let sessionMetadata: Record<string, unknown> = {}
    hostControlPlane().bind({
      get: async ({ url, path }) => {
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
        if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(activeAgent), stderr: "" }
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

    const invoke = (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any) => invokeConcordOperation(toolName, args as any, callContext)
    const transition = (version: number, actionID: string, idempotencyKey: string, fields: Record<string, unknown>) => invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version, action_id: actionID, idempotency_key: idempotencyKey, fields } }, context)

    let response = await transition(5, "record_reproduction", "e2e-reproduction", {})
    expect(response.outcome).toBe("ok")
    response = await transition(7, "record_root_cause", "e2e-root-cause", {})
    expect(response.outcome).toBe("ok")
    const domainList = await invoke("concord_domain", { operation: "list", input: { product_id: PRODUCT_ID, page: { cursor: null, limit: 10 } } }, context)
    expect(domainList.outcome).toBe("ok")
    const registry = domainList.result as JSONRecord
    const registryHash = (registry.registry as JSONRecord).content_hash as string
    response = await transition(8, "approve_contract", "e2e-approve-contract", {
      premise: "Exercise the route.",
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

    const windows = new DispatchWindows()
    let dispatchResponse: JSONRecord | undefined
    const runWorker = async (workerLane: typeof lane | typeof reviewLane, result?: "pass" | "block") => {
      activeAgent = workerLane.id === "review" ? "concord-review" : "concord-implement"
      const expectedVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
      const routed = laneDispatchRequest({ operation: "workflow_action", input: { work_id: workID, expected_version: expectedVersion, action_id: "dispatch_worker", idempotency_key: `e2e-dispatch-${workerLane.id}-${expectedVersion}`, fields: { lane_id: workerLane.id } } })
      const dispatchResult = await dispatchLaneWorker(routed as any, {
        context,
        invoke: async (toolName, args, callContext) => {
          if (toolName === "concord_work_transition" && args.input.action_id === "dispatch_worker") {
            expect(sessionMetadata).toEqual({ [MANAGED_TASK_SCOPE_KEY]: "managed" })
          }
          const result = await invoke(toolName, args, callContext)
          if (toolName === "concord_work_transition" && args.input.action_id === "dispatch_worker") dispatchResponse = result
          return result
        },
        credentials: { async getPrivateKey() { return PRIVATE_SEED } } satisfies CredentialStore,
        windows,
        now: () => 1_700_000_000_000,
      })
      expect(dispatchResult.outcome).toBe("ok")
      expect(dispatchResult.dispatch_state).toBe("awaiting_worker")
      expect(await hostControlPlane().taskScope(SESSION_ID)).toBe("managed")
      expect(await hostControlPlane().taskScope("worker-session")).toBe("managed")
      expect(windows.has(SESSION_ID)).toBe(true)
      const taskArgs: Record<string, unknown> = { subagent_type: "general", prompt: "model input", description: "model task" }
      windows.bind(TASK_TOOL_ID, SESSION_ID, taskArgs)
      const packet = JSON.parse(taskArgs.prompt as string) as JSONRecord
      expect(taskArgs.subagent_type).toBe(activeAgent)
      expect(packet.step_id).toBe(result ? "review" : "repair")
      expect(dispatchResponse?.result?.worker_packet_digest).toMatch(/^sha256:[0-9a-f]{64}$/)
      const report = {
        schema_version: "1.0",
        attempt_id: packet.attempt_id,
        lane_id: packet.lane_id,
        lane_version: packet.lane_version,
        lane_digest: packet.lane_digest,
        readback_model: READBACK_MODEL,
        status: "completed",
        ...(result ? { review_result: result } : {}),
        evidence: workerLane.evidence_obligations.map((obligation) => ({ obligation, detail: `discharged ${obligation}` })),
      }
      const completionOutput = { title: "task", output: taskResult(report), metadata: {} }
      await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION_ID, callID: `e2e-task-call-${workerLane.id}`, args: taskArgs }, completionOutput, { windows, credentials: { async getPrivateKey() { return PRIVATE_SEED } }, runner: realRunner, concordBinary: binary })
      expect(completionOutput.output).toContain("<concord_attempt>")
      const attempt = dbValue(dbPath, `SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id='${packet.attempt_id}'`)
      expect(attempt.lifecycle_state, completionOutput.output).toBe("completed")
      expect(attempt.readback_model).toBe(READBACK_MODEL)
      if (!result) {
        const evidenceVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
        response = await transition(evidenceVersion, "bind_evidence", `e2e-bind-evidence-${workerLane.id}`, { evidence_kind: "verification" })
        expect(response.outcome, JSON.stringify(response)).toBe("ok")
      }
      const currentVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
      response = await transition(currentVersion, "accept_worker_result", `e2e-accept-worker-${workerLane.id}`, { attempt_id: packet.attempt_id, attempt_epoch: 1 })
      expect(response.outcome).toBe("ok")
      return packet
    }

    const packet = await runWorker(lane)
    const reviewPacket = await runWorker(reviewLane, "pass")
    const verifyVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    // CD-0116 after a lane exit: the session that accepted the worker result
    // submits its own verdict, the adapter mints the operator challenge, the
    // host approval signs it, and the verdict records under the operator
    // identity rather than a distinct agent session.
    response = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: verifyVersion, action_id: "record_verdict", idempotency_key: "e2e-record-verdict", fields: { contract_version: 1, predicate_id: WORKFLOW_PREDICATE.predicate_id, evaluation_evidence: [packet.attempt_id, reviewPacket.attempt_id] } } }, context)
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const verdictActor = dbValue(dbPath, `SELECT json_extract(payload,'$.verdict_actor_ref') AS actor FROM domain_events WHERE subject_id='${workID}' AND kind='workflow.verdict_recorded' ORDER BY seq DESC LIMIT 1`).actor as string
    const verdictActorClass = dbValue(dbPath, `SELECT actor_class FROM workflow_actors WHERE actor_ref='${verdictActor}'`).actor_class as string
    expect(verdictActorClass).toBe("operator")
    const verdictVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
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
    for (const call of invokeCalls) requiredFields(call.input.call_envelope, ["schema_version", "request_id", "client_ref", "principal_ref", "session_ref", "agent_ref", "directory", "worktree", "ambient_project_id", "scope_version", "manifest_digest"])
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    if (previousConfig === undefined) delete process.env.OPENCODE_CONFIG
    else process.env.OPENCODE_CONFIG = previousConfig
    await rm(root, { recursive: true, force: true })
  }
}, 120_000)
