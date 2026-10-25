import { expect } from "bun:test"
import { Database } from "bun:sqlite"
import { chmod, mkdir, unlink } from "node:fs/promises"
import { delimiter, join } from "node:path"
import { createInterface } from "node:readline"
import { Readable } from "node:stream"
import { fixtureTempRoot, ownedFixtureRunRoot, requireOwnedFixtureRun, runFixtureProcess } from "./fixture-temp-root"
import { configureConcordAdapter, invokeConcordOperation, laneDispatchRequest, work_transition } from "./concord"
import { configureCoreBinary, validateAgentLanePacket } from "./dispatch"

// The route test drives the real core through its own runner, so argv[0] is
// replaced there. Bind the nominal path the transport resolves instead of the
// unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")
requireOwnedFixtureRun()
import { completeDispatchedWorker } from "./lane_completion"
import { dispatchLaneWorker } from "./lane_dispatch"
import { dispatchWindows, DispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import type { CredentialStore } from "./credentials"
import type { DispatchRunner } from "./dispatch"
import { agentLanes } from "./generated-agent-lanes"
import { contractOperations } from "./generated-contracts"
import { WORKER_REPORT_PROTOCOL } from "./worker-report-protocol.js"
import { hostControlPlane, MANAGED_TASK_SCOPE_KEY, SESSION_MESSAGES_ROUTE, SESSION_ROUTE } from "./move-session"
import { dbRows, dbValue, fixturePublicKeyBase64, git, nativeTaskSelector, reportText, reportWorkerJob, requiredFields, routeDeclaration, runJSON, runProcess, workerEffects, type JSONRecord } from "./end-to-end.fixture"

const PRODUCT_ID = "product-e2e"
const PROJECT_ID = "project-e2e"
const CLIENT_REF = "opencode"
const SESSION_ID = "e2e-session"
const MESSAGE_ID = "e2e-message"
const READBACK_MODEL = "openai/gpt-5.6-luna"
const PRIVATE_SEED = new Uint8Array(32).fill(7)
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
const APPROVED_OBJECTIVE = "Add the session marker .concord/docs/dispatch-marker.txt describing the shipped route."
let sharedCoreBinary: Promise<string> | undefined

// dbExec writes one statement on a dedicated connection, for the
// distinguished direct corpus controls the route injects and restores. No
// admission path runs through it: every final proof in the route drives the
// public tools.
function dbExec(dbPath: string, sql: string, args: unknown[] = []): void {
  const db = new Database(dbPath)
  try {
    db.run("PRAGMA foreign_keys = ON")
    db.run(sql, ...(args as any[]))
  } finally {
    db.close()
  }
}


function seedInvestigationArtifact(dbPath: string, workID: string): void {
  const db = new Database(dbPath)
  try {
    db.run("INSERT INTO write_guard(active) VALUES(1)")
    const registryHash = "sha256:" + "e".repeat(64)
    db.run("INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('e2e-locator',?,'canonical_path','/fixture','/fixture','2026-09-09T00:00:00Z','2026-09-09T00:00:00Z')", [PROJECT_ID])
    db.run("INSERT OR IGNORE INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES(?,?,'e2e-locator',?,'root','1.0',?,'test')", [PRODUCT_ID, PROJECT_ID, PRODUCT_ID, registryHash])
    db.run("INSERT OR IGNORE INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES(?,'e2e-locator',?,'root','Root','Fixture Domain','current',?,'test')", [PROJECT_ID, PRODUCT_ID, registryHash])
    db.run("INSERT OR IGNORE INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','Investigation comparison','needed',0,1,'2026-09-09T00:00:00Z','2026-09-09T00:00:00Z')", [`${workID}-compared`])
    db.run("INSERT OR IGNORE INTO work_projects(work_id,project_id,role) VALUES(?,?,'secondary')", [`${workID}-compared`, PROJECT_ID])
    const investigationRefs = JSON.stringify(["root", `${workID}-compared`])
    db.run("INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) VALUES(?,?,?,?,?,'2026-09-09T00:00:00Z')", ["obs:" + workID.slice(-12) + "cmp0", workID, "investigation before question", investigationRefs, "[]"])
    db.run("DELETE FROM write_guard")
  } finally {
    db.close()
  }
}

function contextFor(directory: string, sessionID = SESSION_ID) {
  return {
    sessionID,
    messageID: MESSAGE_ID,
    agent: "concord-implement",
    directory,
    worktree: directory,
    abort: new AbortController().signal,
    metadata() {},
    ask: async () => {},
  } as any
}

function taskResultRaw(reportTextValue: string, workerSessionID = "worker-session"): string {
  return [
    `<task id="${workerSessionID}" state="completed">`,
    "<task_result>",
    reportTextValue,
    "</task_result>",
    "</task>",
  ].join("\n")
}

function taskResult(report: JSONRecord, workerSessionID = "worker-session"): string {
  return taskResultRaw(reportText(report), workerSessionID)
}

// CD-0102: an authorized worker session opens with the dispatch packet as its
// first user message, so the fixture takes the bound packet once the test
// captures it from the rewritten Task call.
function exportedSession(opening: JSONRecord | null = null, bulkTextBytes = 0, workerAgent = "concord-implement"): string {
  return JSON.stringify({
    info: { id: "worker-session" },
    messages: [
      ...(opening ? [{ info: { id: "worker-message-open", sessionID: "worker-session", role: "user", agent: workerAgent, time: { created: 0 } }, parts: [{ type: "text", text: JSON.stringify(opening) }] }] : []),
      ...(bulkTextBytes > 0 ? [{ info: { id: "worker-message-bulk", sessionID: "worker-session", role: "user", agent: workerAgent, time: { created: 0.5 } }, parts: [{ type: "text", text: "x".repeat(bulkTextBytes) }] }] : []),
      { info: { id: "worker-message", sessionID: "worker-session", role: "assistant", agent: workerAgent, providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] },
    ],
  })
}

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
  // The CLI's native producer uses the installed local toolchain, not Go's
  // module-triggered launcher switch. Use the toolchain that builds this test.
  const toolchain = await runProcess(["go", "env", "GOROOT"], "", join(import.meta.dir, "..", ".."))
  expect(toolchain.exitCode, toolchain.stderr).toBe(0)
  const toolchainBin = join(toolchain.stdout.trim(), "bin")
  const searchPath = (process.env.PATH ?? "").split(delimiter).filter((entry) => entry !== toolchainBin)
  process.env.PATH = [toolchainBin, ...searchPath].join(delimiter)
  sharedCoreBinary ??= (async () => {
    const binRoot = join(ownedFixtureRunRoot(), "shared-core-bin")
    await mkdir(binRoot, { recursive: true })
    const path = join(binRoot, "concord")
    const build = await runProcess(["go", "build", "-o", path, "./cmd/concord"], "", join(import.meta.dir, "..", ".."))
    expect(build.exitCode, `go build: ${build.stderr}`).toBe(0)
    return path
  })()
  const binary = await sharedCoreBinary
  const repo = join(root, "repo")
  const dbPath = join(root, "concord.db")
  const configPath = join(repo, "opencode.jsonc")
  const lane = agentLanes.find((candidate) => candidate.id === "implement")
  if (!lane) throw new Error("implement lane is not registered")

  await mkdir(join(repo, ".concord/docs/knowledge"), { recursive: true })
  await Bun.write(join(repo, ".concord/docs/knowledge", "manifest.json"), JSON.stringify({
    schema_version: "1.2",
    supported_kinds: [],
    indexed_kinds: [],
    knowledge_roots: [],
  }, null, 2))
  await Bun.write(join(repo, ".concord/docs/knowledge", "domain-registry.json"), JSON.stringify({
    schema_version: "1.0",
    product_key: PRODUCT_ID,
    root_domain_id: `product-root:${PRODUCT_ID}`,
    domains: [{ domain_id: `product-root:${PRODUCT_ID}`, name: "Synthetic root", purpose: "Synthetic test domain", status: "current", architecture_relations: [] }],
  }, null, 2))
  await Bun.write(configPath, JSON.stringify({ instructions: ["https://example.invalid/synthetic-instructions"] }))
  await Bun.write(join(repo, "README.md"), "synthetic dispatch fixture\n")
  await mkdir(join(repo, "oraclekit"))
  await mkdir(join(repo, "test-oracle"))
  await Bun.write(join(repo, "go.mod"), "module example.test/route\n\ngo 1.26.0\ntoolchain go1.26.7\n")
  await Bun.write(join(repo, "oraclekit/oracle.go"), "package oraclekit\n\nfunc RouteReady() bool { return true }\n")
  await Bun.write(join(repo, "oraclekit/oracle_test.go"), 'package oraclekit\n\nimport "testing"\n\nfunc TestRouteOraclePasses(t *testing.T) { if !RouteReady() { t.Fatal("route not ready") } }\n')
  await Bun.write(join(repo, "test-oracle/manifest.json"), JSON.stringify({
    kind: "go_top_level_v1",
    package_cwd: "oraclekit",
    test_files: ["oraclekit/oracle_test.go"],
    fixture_files: [],
    cases: { "case:route-e2e": ["TestRouteOraclePasses"] },
  }))
  await git(repo, "init", "--quiet", "--initial-branch=main")
  await git(repo, "config", "user.email", "test@example.invalid")
  await git(repo, "config", "user.name", "Synthetic Test")
  await git(repo, "add", ".")
  await git(repo, "commit", "--quiet", "-m", "fixture")
  await git(repo, "commit", "--quiet", "--allow-empty", "-m", "synthetic subject")
  await git(repo, "remote", "add", "origin", "https://example.invalid/synthetic.git")
  // The bootstrap preflight fetches origin's default branch, so the remote
  // maps onto a local bare repository through insteadOf: the fetch stays
  // hermetic while the URL stays one ResolveProject accepts.
  await git(root, "init", "--quiet", "--bare", "--initial-branch=main", "origin.git")
  await git(repo, "config", `url.${join(root, "origin.git")}.insteadOf`, "https://example.invalid/synthetic.git")
  await git(repo, "push", "--quiet", "origin", "main")
  await git(repo, "fetch", "--quiet", "origin")
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
    defect_intake: { failure_shape: "dispatch-route-e2e", reproduction: "run the synthetic dispatch route", searched: "the synthetic store", related_defect_ids: [] },
    priority: 1,
    urgency: "standard",
    tags: [],
    workflow_type_ref: "",
    external_ref: "CON-898",
    governing_requirements: [],
    ref: "HEAD",
  }, repo)
  await runJSON(binary, dbPath, "client-register", {
    client_ref: CLIENT_REF,
    key_id: "dispatch-e2e-key",
    principal_ref: "operator-1",
    public_key: fixturePublicKeyBase64(PRIVATE_SEED),
    capabilities: ["product_read", "work_define", "work_transition", "work_relate", "work_compact", "research", "worker_evidence", "worker_dispatch"],
    product_scope: [PRODUCT_ID],
    project_scope: [PROJECT_ID],
    agent_scope: ["concord-implement", "concord-review", "concord-verify"],
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
      return runFixtureProcess([binary, ...argv.slice(1)], input, { env: { ...process.env, CONCORD_DB_PATH: dbPath }, signal })
    },
  }
}

// driveWorkflowToContract walks the real workflow from capture to the
// approved-contract state that makes dispatch_worker the next action, using
// the transition sequence every route test shares. The premise and predicates
// are parameters so capacity tests can approve a maximum-size premise with
// eight synthetic predicates through the same real route. It returns the
// work version the record_worker_job action reported, so dispatch callers
// consume the store's own dispatch-ready version instead of pinning it.
async function driveWorkflowToContract(
  workID: string,
  invoke: (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) => Promise<any>,
  context: any,
  premise: string = APPROVED_OBJECTIVE,
  outcomePredicates: JSONRecord[] = [WORKFLOW_PREDICATE],
  requiredEvidence: string[] = [],
  jobPathScope: string[] = ["internal/store"],
  jobChecks: string[] = ["go test ./internal/store/"],
): Promise<number> {
  const transition = (version: number, actionID: string, idempotencyKey: string, fields: Record<string, unknown>) => invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version, action_id: actionID, idempotency_key: idempotencyKey, fields } }, context)
  // CD-0198 D2: one typed record_diagnosis carries the reproduction,
  // alignment-search, and root-cause sections the three separate records
  // carried, so the diagnosis step takes one action instead of three.
  let response = await transition(5, "record_diagnosis", "e2e-diagnosis", {
    reproduction: "The defect reproduces from a clean store with the seeded project.",
    searched: "Searched the backlog for duplicate defect work.",
    outcome: "none_found",
    root_cause: "The fold recorded the acceptance without its delivery assertion.",
  })
  expect(response.outcome, JSON.stringify(response)).toBe("ok")
  const domainList = await invoke("concord_domain", { operation: "list", input: { product_id: PRODUCT_ID, page: { cursor: null, limit: 10 } } }, context)
  expect(domainList.outcome).toBe("ok")
  const registry = domainList.result as JSONRecord
  const registryHash = (registry.registry as JSONRecord).content_hash as string
  response = await transition(8, "approve_contract", "e2e-approve-contract", {
    premise,
    outcome_predicates: outcomePredicates,
    required_evidence: requiredEvidence,
    route_conventions: [],
    spec_mandate: [],
    law_modifies: [],
    // The consequence_v1 head requires the decision fact; none declares
    // engineering work, which records without a consent question.
    decision_domains: [],
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
  expect(response.outcome, JSON.stringify(response)).toBe("ok")
  const stepRead = (await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID, page: { cursor: null, limit: 1 } } }, context)).result as JSONRecord
  expect((stepRead.pinned as JSONRecord).workflow_step).toBe("repair")
  const head = await runProcess(["git", "rev-parse", "HEAD"], "", context.directory)
  expect(head.exitCode, head.stderr).toBe(0)
  const subjectCommit = head.stdout.trim()
  const owner = {
    owner_id: "owner:route-e2e",
    domain_id: `product-root:${PRODUCT_ID}`,
    mechanism: { project_id: PROJECT_ID, path: "oraclekit/oracle.go", entry_point: "RouteReady" },
    obligation: "The synthetic route readiness check passes.",
    predicate_ids: [outcomePredicates[0].predicate_id],
    law_bindings: [],
  }
  const oracleCase = {
    case_id: "case:route-e2e", owner_id: owner.owner_id,
    entry_point: "RouteReady", input_class: "synthetic route fixture",
    expected_state: "The route is ready.", control_ids: ["control:route-e2e"],
  }
  const control = {
    control_id: "control:route-e2e", owner_id: owner.owner_id,
    predicate_ids: owner.predicate_ids, case_ids: [oracleCase.case_id],
    recipe_source: { kind: "repository_file", project_id: PROJECT_ID, path: "test-oracle/manifest.json", commit_oid: subjectCommit },
    argv: ["go", "test", "-count=1", "-run", "^(TestRouteOraclePasses)$", "."],
    cwd: "oraclekit", expected_result: "pass", required_evidence_role: "reported",
  }
  const verify = (idempotencyKey: string, input: Record<string, unknown>) => invoke("concord_work_transition", {
    operation: "worktree_verify", input: { work_id: workID, idempotency_key: idempotencyKey, requested_budget_seconds: 300, ...input },
  }, context)
  response = await verify("e2e-oracle-subject", { command: ["git", "status", "--porcelain"] })
  expect(response.outcome, JSON.stringify(response.error ?? null)).toBe("ok")
  expect(response.result.exit_code).toBe(0)
  expect(response.result.tracked_files_changed).toBe(false)
  response = await verify("e2e-oracle-prepare", { oracle: {
    phase: "prepare", expected_contract_version: 1,
    control_bundle: { owner, cases: [oracleCase], control },
  } })
  expect(response.outcome, JSON.stringify(response.error ?? null)).toBe("ok")
  expect(response.result.oracle.qualification).toBe("ready")
  expect(response.result.oracle.subject_commit).toBe(subjectCommit)
  const preparationRef = response.result.oracle.run_ref as string
  // The job-capable break-fix definition dispatches the implement lane only
  // under a recorded ready worker-job revision (CD-0205).
  response = await transition(10, "record_worker_job", "e2e-worker-job", {
    job_id: ROUTE_WORKER_JOB_ID,
    predicate_ids: owner.predicate_ids,
    objective: "Apply the bounded route repair.",
    stopping_condition: "The route repair is applied and its checks pass.",
    path_scope: jobPathScope,
    checks: jobChecks,
    ready: true,
    readiness_evidence: ["evidence:route-e2e-ready"],
    acceptance_oracle: { owners: [owner], cases: [oracleCase], controls: [{ ...control, readiness_evidence_refs: [preparationRef] }] },
  })
  expect(response.outcome, JSON.stringify(response.error ?? null)).toBe("ok")
  const jobRead = (await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID, page: { cursor: null, limit: 1 } } }, context)).result as JSONRecord
  const ready = (jobRead.pinned as JSONRecord).ready_worker_jobs as JSONRecord[]
  expect(ready.map((job) => [job.job_id, job.revision])).toEqual([[ROUTE_WORKER_JOB_ID, 1]])
  const dispatchVersion = ((response.result as JSONRecord).changed_refs as JSONRecord[]).find((changed) => changed.entity_kind === "work_item")!.version as number
  return dispatchVersion
}

const ROUTE_WORKER_JOB_ID = "job:route-e2e-repair"

function routeWorkerReport(lane: (typeof agentLanes)[number], packet: JSONRecord, subjectCommit: string, review?: JSONRecord): JSONRecord {
  return {
    schema_version: packet.schema_version,
    readback_model: READBACK_MODEL,
    status: "completed",
    ...((packet.inputs as JSONRecord).worker_job ? { worker_job: reportWorkerJob(packet) } : {}),
    ...(review ? { review } : {}),
    evidence: lane.evidence_obligations
      .filter((obligation: string) => !(review && obligation === "severity"))
      .map((obligation: string, index: number) => ({
        obligation,
        detail: `verified ${obligation} on commit:${subjectCommit}`,
        ...(index === 0 && (packet.inputs as JSONRecord).outcome_predicates
          ? { predicate_ids: [(((packet.inputs as JSONRecord).outcome_predicates as JSONRecord[])[0]).predicate_id] }
          : {}),
      })),
  }
}

async function routeReviewBlock(packet: JSONRecord, worktree: string, disposition: string, invoke: any, context: any, findings: JSONRecord[] = [], receiptRefs: string[] = []): Promise<JSONRecord> {
  const reviewInput = packet.inputs.review_input
  expect(reviewInput).toBeDefined()
  const dependencies = reviewInput.dependencies
  const reported = new Set(findings.map((finding) => finding.finding_id))
  const hasEarlierRanked = packet.inputs.work_context?.findings?.some((finding: JSONRecord) => finding.source_kind === "review_finding")
  const previous = hasEarlierRanked ? (await invoke("concord_work_trace", { operation: "continuity", input: { work_id: packet.work_id } }, context)).result.pinned.work_pin.review.findings : []
  for (const finding of previous) {
    if (finding.status !== "open" || reported.has(finding.finding_id)) continue
    const closure = receiptRefs.length > 0 && disposition === "ready_for_final_proof"
    const earlier = packet.inputs.work_context?.findings?.find((entry: JSONRecord) => entry.oracle?.owner_id === finding.oracle?.owner_id && entry.oracle?.failure_family === finding.oracle?.failure_family)
    findings.push({ ...finding, status: closure ? "closed" : "open", ...(closure ? { closure_refs: receiptRefs } : {}),
      ...(closure && finding.oracle && earlier ? { oracle: { ...finding.oracle, continues_finding_id: earlier.finding_id } } : {}) })
  }
  expect(reviewInput.input_candidate.commit).toBe(packet.inputs.work_context.subject_commit)
  const output = await runProcess(["git", "rev-parse", "HEAD", "HEAD^{tree}"], "", worktree)
  expect(output.exitCode, output.stderr).toBe(0)
  const [commit, tree] = output.stdout.trim().split("\n")
  return {
    disposition, input_candidate: reviewInput.input_candidate,
    output_candidate: { project_id: PROJECT_ID, commit, tree }, dependencies,
    assessments: receiptRefs.length ? packet.inputs.outcome_predicates.map((predicate: JSONRecord) => ({ predicate_id: predicate.predicate_id, assessment: "supported", receipt_refs: receiptRefs })) : [],
    findings: findings.map((finding) => ({ closure_refs: [], ...finding })), remaining_obligations: [],
  }
}

interface RouteTaskDispatch {
  dispatchResult: JSONRecord
  windows: DispatchWindows
  windowWasOpen: boolean
  taskArgs: JSONRecord
  packet: JSONRecord
  callID: string
  sessionID: string
  workerSessionID: string
  retainedOutput?: string
}

async function dispatchRouteTask(
  lane: (typeof agentLanes)[number],
  workID: string,
  worktree: string,
  expectedVersion: number,
  idempotencyKey: string,
  callID: string,
  context: any,
  invoke: (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) => Promise<any>,
  setReadback: (packet: JSONRecord, agent: string) => void,
): Promise<RouteTaskDispatch> {
  const routed = laneDispatchRequest({ operation: "workflow_action", input: { work_id: workID, expected_version: expectedVersion, action_id: "dispatch_worker", idempotency_key: idempotencyKey, fields: { lane_id: lane.id } } })
  expect(routed).toEqual({ work_id: workID, expected_version: expectedVersion, idempotency_key: idempotencyKey, lane_id: lane.id })
  const windows = new DispatchWindows()
  const dispatchResult = await dispatchLaneWorker(routed as any, {
    context,
    invoke,
    credentials: { async getPrivateKey() { return PRIVATE_SEED } } satisfies CredentialStore,
    windows,
  })
  expect(dispatchResult.outcome, JSON.stringify(dispatchResult)).toBe("ok")
  expect(dispatchResult.dispatch_state).toBe("awaiting_worker")
  const windowWasOpen = windows.has(context.sessionID)
  const taskArgs = nativeTaskSelector(dispatchResult)
  await windows.bind(TASK_TOOL_ID, context.sessionID, taskArgs, callID, async () => worktree, worktree)
  const packet = JSON.parse(taskArgs.prompt as string) as JSONRecord
  setReadback(packet, `concord-${lane.id}`)
  expect(taskArgs.subagent_type).toBe(`concord-${lane.id}`)
  return { dispatchResult, windows, windowWasOpen, taskArgs, packet, callID, sessionID: context.sessionID, workerSessionID: context.workerSessionID ?? "worker-session" }
}

async function completeRouteTask(
  task: RouteTaskDispatch,
  report: JSONRecord | string,
  runner: DispatchRunner,
  binary: string,
): Promise<JSONRecord> {
  const output = typeof report === "string" ? taskResultRaw(report, task.workerSessionID) : taskResult(report, task.workerSessionID)
  const completionOutput = { title: "task", output, metadata: {} }
  await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: task.sessionID, callID: task.callID, args: task.taskArgs }, completionOutput, {
    windows: task.windows,
    credentials: { async getPrivateKey() { return PRIVATE_SEED } },
    runner,
    concordBinary: binary,
  })
  expect(completionOutput.output).toContain("<concord_attempt>")
  task.retainedOutput = completionOutput.output
  return JSON.parse(completionOutput.output.split("<concord_attempt>\n")[1].split("\n</concord_attempt>")[0]) as JSONRecord
}

function bindRouteSessions(worktree: string, coordinatorIDs: string[]) {
  const sessions = new Map<string, JSONRecord>(coordinatorIDs.map((id) => [id, { info: { id, directory: worktree, metadata: {} }, messages: [] }]))
  let sessionCreates = 0
  hostControlPlane().bind({
    get: async ({ url, path }) => {
      const session = sessions.get(path!.id as string)
      expect(session, `unregistered synthetic session ${path?.id}`).toBeDefined()
      if (url === SESSION_MESSAGES_ROUTE) return { data: session!.messages, response: new Response("[]", { status: 200 }) }
      expect(url).toBe(SESSION_ROUTE)
      return { data: session!.info, response: new Response(null, { status: 200 }) }
    },
    patch: async ({ path, body }) => {
      sessions.get(path!.id as string)!.info.metadata = (body as JSONRecord).metadata
      return { response: new Response(null, { status: 200 }) }
    },
    post: async request => {
      if (request.url === SESSION_MESSAGES_ROUTE) throw new Error("dispatch performs zero subtask writes")
      sessionCreates++
      throw new Error("recovery and dispatch must not create a coordinator session")
    },
  })
  let taskLaunches = 0
  const opening = (context: any, packet: JSONRecord, agent: string) => {
    const child = JSON.parse(exportedSession(packet, 0, agent))
    child.info = { id: context.workerSessionID, parentID: context.sessionID, directory: worktree, agent }
    for (const message of child.messages) message.info.sessionID = context.workerSessionID
    sessions.set(context.workerSessionID, child)
    taskLaunches++
  }
  const retain = (task: RouteTaskDispatch, report: JSONRecord) => {
    const child = sessions.get(task.workerSessionID)!
    child.messages.at(-1).parts = [{ type: "text", text: reportText(report) }]
    const parent = sessions.get(task.sessionID)!
    parent.messages.push({ info: { id: task.callID, sessionID: task.sessionID, role: "assistant" }, parts: [{ id: task.callID, sessionID: task.sessionID, type: "tool", tool: TASK_TOOL_ID, state: { status: "completed", input: task.taskArgs, output: task.retainedOutput } }] })
    return { parent, child }
  }
  return { sessions, opening, retain, counts: () => ({ coordinatorSessions: coordinatorIDs.length, taskLaunches, sessionCreates }) }
}

routeDeclaration("G01 composes concurrent coordinators with fresh production independent review verify and retained reports", async () => {
  const root = await fixtureTempRoot("dispatch-g01")
  const previousConfig = process.env.OPENCODE_CONFIG
  try {
    const { binary, dbPath, configPath, workID, worktree, lane } = await bootRouteFixture(root)
    process.env.OPENCODE_CONFIG = configPath
    const a = contextFor(worktree, "g01-coordinator-a")
    const b = contextFor(worktree, "g01-coordinator-b")
    const c = contextFor(worktree, "g01-coordinator-c")
    const host = bindRouteSessions(worktree, [a.sessionID, b.sessionID, c.sessionID])
    const calls: Array<{ argv: string[]; input: JSONRecord }> = []
    const baseRunner = realStoreRunner(binary, dbPath, calls, worktree)
    let loseAcknowledgement = false
    const runner: DispatchRunner = { async run(argv, input, signal) {
      const result = await baseRunner.run(argv, input, signal)
      if (loseAcknowledgement && argv[1] === "worker-complete") {
        loseAcknowledgement = false
        expect(result.exitCode, result.stderr).toBe(0)
        return { exitCode: 1, stdout: "", stderr: "synthetic lost completed-report acknowledgement" }
      }
      return result
    } }
    configureConcordAdapter({ runner })
    const invoke = (tool: string, args: any, context: any, directory?: string) => invokeConcordOperation(tool, args, context, directory) as Promise<JSONRecord>
    const version = () => dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version
    const events = () => dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n
    const action = async (context: any, name: string, key: string, fields: JSONRecord) => {
      const response = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version(), action_id: name, idempotency_key: key, fields } }, context)
      expect(response.outcome, JSON.stringify(response)).toBe("ok")
      return response
    }
    const verify = async (context: any, key: string, input: JSONRecord = { command: ["git", "rev-parse", "HEAD"] }) => {
      const response = await invoke("concord_work_transition", { operation: "worktree_verify", input: { work_id: workID, idempotency_key: key, requested_budget_seconds: 300, ...input } }, context)
      expect(response.outcome, JSON.stringify(response)).toBe("ok")
      expect(response.result.exit_code).toBe(0)
      return response.result
    }
    const dispatch = async (context: any, laneID: string, key: string) => {
      const typedLane = agentLanes.find((item) => item.id === laneID)!
      context.workerSessionID = `${key}-worker`
      return dispatchRouteTask(typedLane, workID, worktree, version(), key, `${key}-task`, context, invoke, (packet, agent) => host.opening(context, packet, agent))
    }
    const epoch = (task: RouteTaskDispatch) => dbValue(dbPath, `SELECT json_extract(payload,'$.attempt_epoch') AS n FROM worker_authorizations WHERE work_id='${workID}' AND attempt_id='${task.packet.attempt_id}'`).n
    const accept = (context: any, task: RouteTaskDispatch, key: string, name = "accept_worker_result") => action(context, name, key, { attempt_id: task.packet.attempt_id, attempt_epoch: epoch(task) })
    const predicate = { ...WORKFLOW_PREDICATE, outcome_kind: "exists", outcome_payload: { kind: "exists", surface: "git:tree", subjects: ["path:.concord/docs/dispatch-marker.txt"] } }
    const observations = await Promise.all([a, b].map((context) => invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID } }, context)))
    expect(observations.map((response) => response.outcome)).toEqual(["ok", "ok"])
    await driveWorkflowToContract(workID, invoke, a, APPROVED_OBJECTIVE, [predicate], ["verification", "review"], [".concord/docs/dispatch-marker.txt"], ["go test ./oraclekit"])
    const contestedVersion = version()
    const boundBefore = dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}' AND kind='workflow.evidence_bound'`).n
    const contenders = await Promise.all([a, b].map((context) => invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: contestedVersion, action_id: "bind_evidence", fields: { evidence_kind: "verification" }, idempotency_key: `g01-contested-${context.sessionID}` } }, context)))
    expect(contenders.filter((response) => response.outcome === "ok")).toHaveLength(1)
    const loser = contenders.find((response) => response.outcome === "error")!
    expect(loser.error.kind).toBe("version_conflict")
    expect(loser.error.effect_state).toBe("none")
    expect(dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}' AND kind='workflow.evidence_bound'`).n).toBe(boundBefore + 1)
    let oracle: JSONRecord
    let previousSubject = (await runProcess(["git", "rev-parse", "HEAD"], "", worktree)).stdout.trim()
    let firstProducer: RouteTaskDispatch | undefined
    let lastProducer: RouteTaskDispatch | undefined
    let lastVerify: RouteTaskDispatch | undefined
    let lastProof: string | undefined
    for (let pass = 0; pass < 2; pass++) {
      const key = `g01-pass-${pass}`
      if (pass === 1) {
        const before = events()
        const refused = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version(), action_id: "complete", idempotency_key: "g01-complete-before-production", fields: { impact_verdict: "non-breaking" } } }, a)
        expect(refused.outcome).toBe("error")
        expect(refused.error.effect_state).toBe("none")
        expect(events()).toBe(before)
        await verify(a, `${key}-producer-subject`)
        const { readiness_evidence_refs: _, ...control } = oracle!.controls[0]
        const prepared = await verify(a, `${key}-producer-prepare`, { oracle: { phase: "prepare", expected_contract_version: 1, control_bundle: { owner: oracle!.owners[0], cases: oracle!.cases, control } } })
        oracle = { ...oracle!, controls: [{ ...control, readiness_evidence_refs: [prepared.oracle.run_ref] }] }
        await action(a, "record_worker_job", `${key}-job`, { job_id: `${key}-producer`, predicate_ids: oracle!.owners[0].predicate_ids, objective: "Apply the corrected marker.", stopping_condition: "The corrected marker is committed.", path_scope: [".concord/docs/dispatch-marker.txt"], checks: ["git status --short"], ready: true, readiness_evidence: ["evidence:corrected-marker-ready"], acceptance_oracle: oracle!,  })
        const correctionPin = await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID } }, a)
        expect(correctionPin.outcome, JSON.stringify(correctionPin)).toBe("ok")
        expect(correctionPin.result.pinned.work_pin.correction?.diagnosis, JSON.stringify(correctionPin)).toBe("The checkpoint review blocks this subject.")
      }
      const producer = await dispatch(a, "implement", `${key}-implement`)
      oracle = producer.packet.inputs.worker_job.acceptance_oracle
      await Bun.write(join(worktree, ".concord/docs/dispatch-marker.txt"), `synthetic marker pass ${pass}\n`)
      await git(worktree, "add", ".concord/docs/dispatch-marker.txt")
      await git(worktree, "-c", "user.name=Synthetic Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", `synthetic marker ${pass}`)
      const subject = (await runProcess(["git", "rev-parse", "HEAD"], "", worktree)).stdout.trim()
      expect(subject).not.toBe(previousSubject)
      expect(await Bun.file(join(worktree, ".concord/docs/dispatch-marker.txt")).text()).toBe(`synthetic marker pass ${pass}\n`)
      const report = routeWorkerReport(lane, producer.packet, subject)
      if (pass === 1) loseAcknowledgement = true
      const receipt = await completeRouteTask(producer, report, runner, binary)
      const retained = host.retain(producer, report)
      if (pass === 1) {
        expect(receipt.outcome).toBe("error")
        expect(receipt.error.details.effect_state).toBe("unknown")
        const before = { receipts: dbRows(dbPath, `SELECT * FROM effect_receipts WHERE subject_id='${workID}' ORDER BY effect_seq`), workers: workerEffects(dbPath, workID) }
        const counts = host.counts()
        const recovery = { binary, dbPath, worktree, sessionID: a.sessionID, ...retained, request: { operation: "worker_reconcile", input: { work_id: workID, attempt_id: producer.packet.attempt_id, task_part_id: producer.callID, idempotency_key: "g01-retained-reconcile" } } }
        for (let replay = 0; replay < 2; replay++) {
          const recovered = await runProcess([process.execPath, "-e", RECOVERY_PROCESS], JSON.stringify(recovery), join(import.meta.dir, "../.."))
          expect(recovered.exitCode, recovered.stderr).toBe(0)
          const result = JSON.parse(recovered.stdout)
          expect(result.envelope.outcome, JSON.stringify(result)).toBe("ok")
          expect(result.envelope.result.worker_recovery.lifecycle_state).toBe("completed")
          expect(result.envelope.result.worker_recovery.dispatch.attempt_id).toBe(producer.packet.attempt_id)
          expect(result.envelope.result.worker_recovery.coordinator_session).toBe(a.sessionID)
          expect(retained.parent.messages.at(-1).parts[0].state.input.prompt).toBe(producer.taskArgs.prompt)
          expect(retained.parent.messages.at(-1).parts[0].state.output).toBe(producer.retainedOutput)
          expect(retained.child.messages.at(-1).parts[0].text).toBe(reportText(report))
        }
        expect({ receipts: dbRows(dbPath, `SELECT * FROM effect_receipts WHERE subject_id='${workID}' ORDER BY effect_seq`), workers: workerEffects(dbPath, workID) }).toEqual(before)
        expect(host.counts()).toEqual(counts)
        expect(producer.packet.attempt_id).not.toBe(firstProducer!.packet.attempt_id)
        expect(producer.packet.inputs.correction.diagnosis).toBe("The checkpoint review blocks this subject.")
      } else expect(receipt.outcome, JSON.stringify(receipt)).toBe("ok")
      await action(a, "bind_evidence", `${key}-bind`, { evidence_kind: "verification" })
      await accept(a, producer, `${key}-accept`)
      const productionEvent = dbRows(dbPath, `SELECT d.event_id,d.payload FROM worker_attempt_dispositions d JOIN effect_receipts r ON r.event_id=d.event_id AND r.effect_seq=d.seq WHERE d.work_id='${workID}' AND d.attempt_id='${producer.packet.attempt_id}' AND d.action_id='accept_worker_result' AND r.kind='workflow.action_completed'`)[0]
      expect(JSON.parse(productionEvent.payload).worker_attempt_id).toBe(producer.packet.attempt_id)
      expect(dbValue(dbPath, `SELECT satisfied_result_ref FROM worker_job_revisions WHERE work_id='${workID}' AND job_id='${producer.packet.inputs.worker_job.job_id}'`).satisfied_result_ref).toBe(productionEvent.event_id)
      expect(calls.find((call) => call.argv[1] === "worker-complete" && call.input.attempt_id === producer.packet.attempt_id)!.input.worker_job).toEqual(reportWorkerJob(producer.packet))
      const completedProducer = JSON.parse(workerEffects(dbPath, workID).find((row) => row.kind === "worker.completed" && JSON.parse(row.payload).attempt_id === producer.packet.attempt_id)!.payload)
      expect(completedProducer.worker_job).toEqual(reportWorkerJob(producer.packet))
      expect(completedProducer.evidence).toEqual(report.evidence)
      expect(completedProducer.evidence[0].detail).toContain(`commit:${subject}`)
      expect((await verify(a, `${key}-current-subject`)).output.trim()).toBe(subject)
      const evaluations = pass === 0 ? ["verify", "review", "review"] : ["verify", "review"]
      for (const [evaluationIndex, laneID] of evaluations.entries()) {
        const evaluationKey = `${key}-${laneID}-${evaluationIndex}`
        const corrective = pass === 0 && evaluationIndex === 1
        const control = oracle.controls[0]
        const { readiness_evidence_refs: _, ...preparationControl } = control
        const prepared = await verify(b, `${evaluationKey}-prepare`, { oracle: { phase: "prepare", expected_contract_version: 1, control_bundle: { owner: oracle.owners[0], cases: oracle.cases, control: preparationControl } } })
        const freshOracle = { ...oracle, controls: [{ ...control, readiness_evidence_refs: [prepared.oracle.run_ref] }] }
        await action(b, "record_worker_job", `${evaluationKey}-job`, { job_id: evaluationKey, predicate_ids: oracle.owners[0].predicate_ids, objective: `Independently ${laneID} the corrected commit.`, stopping_condition: "The pinned subject has an independent report.", path_scope: [".concord/docs/dispatch-marker.txt"], checks: ["go test ./oraclekit"], ready: true, readiness_evidence: [prepared.oracle.run_ref], acceptance_oracle: freshOracle })
        const evaluator = await dispatch(b, laneID, evaluationKey)
        expect(evaluator.workerSessionID).not.toBe(producer.workerSessionID)
        expect(evaluator.packet.inputs.outcome_predicates).toEqual([predicate])
        const boundControl = evaluator.packet.inputs.worker_job.acceptance_oracle.controls[0]
        const execution = await verify(b, `${evaluationKey}-execute`, { oracle: { phase: "execute", attempt_id: evaluator.packet.attempt_id, attempt_epoch: epoch(evaluator), worker_packet_digest: evaluator.dispatchResult.result.worker_packet_digest, worker_job_binding: reportWorkerJob(evaluator.packet), control_id: boundControl.control_id, preparation_run_ref: boundControl.readiness_evidence_refs[0] } })
        expect(execution.oracle.subject_commit).toBe(subject)
        expect(execution.oracle.qualification).toBe("pass")
        const open = evaluator.packet.inputs.work_context?.findings.filter((finding: JSONRecord) => finding.status === "open") ?? []
        if (pass === 0 && laneID === "review" && !corrective) expect(open.length).toBeGreaterThan(0)
        const packetOnlyInvoke = corrective ? async () => { throw new Error("A first Review must construct its report from the production packet alone") } : invoke
        const review = laneID === "review" ? await routeReviewBlock(evaluator.packet, worktree, corrective ? "correction_required" : "ready_for_final_proof", packetOnlyInvoke, b,
          corrective ? [{ finding_id: "finding:g01-marker", in_scope: true, blocking: true, status: "open", evidence_refs: [execution.operation_ref], severity: "P1", confidence: "high", detail: "The synthetic marker needs repair.", oracle: { classification: "delivery_blocker", owner_id: "owner:route-e2e", failure_family: "synthetic-route-readiness", predicate_ids: [predicate.predicate_id], control_ids: [boundControl.control_id], case_ids: boundControl.case_ids, evidence_refs: [execution.operation_ref] } }] : [], [execution.operation_ref]) : undefined
        if (review && !corrective) review.resolved_findings = open.map((finding: JSONRecord) => ({ finding_id: finding.finding_id, evidence_refs: [execution.operation_ref] }))
        const evaluation = routeWorkerReport(agentLanes.find((item) => item.id === laneID)!, evaluator.packet, subject, laneID === "review" ? review : undefined)
        evaluation.evidence[0].oracle_receipt = { control_ids: [boundControl.control_id], case_ids: boundControl.case_ids, recipe_source: boundControl.recipe_source, result: "pass", exit_code: 0, run_ref: execution.operation_ref, evidence_refs: [] }
        const evaluationReceipt = await completeRouteTask(evaluator, evaluation, runner, binary)
        expect(evaluationReceipt.outcome, JSON.stringify(evaluationReceipt)).toBe("ok")
        host.retain(evaluator, evaluation)
        await accept(b, evaluator, `${evaluationKey}-accept`)
        if (corrective) {
          await action(b, "bind_evidence", `${evaluationKey}-bind`, { evidence_kind: "verification", evidence_ref: (await verify(b, `${evaluationKey}-delivery-verify`)).operation_ref })
          const before = events()
          const refused = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version(), action_id: "record_delivery", idempotency_key: "g01-corrective-delivery", fields: { delivery_artifact: `commit:${subject}`, delivery_state: "asserted" } } }, b)
          expect(refused.outcome).toBe("error")
          expect(refused.error.message).toContain("open in-scope blocking finding")
          expect(refused.error.effect_state).toBe("none")
          expect(events()).toBe(before)
        }
        if (laneID === "verify") lastVerify = evaluator
        if (laneID === "review" && !corrective) lastProof = execution.operation_ref
      }
      await action(a, "bind_evidence", `${key}-integrated`, { evidence_kind: "verification", evidence_ref: (await verify(a, `${key}-integration`)).operation_ref })
      await action(a, "record_delivery", `${key}-repair-delivery`, { delivery_artifact: `commit:${subject}`, delivery_state: "asserted" })
      await action(a, "checkpoint_refine", `${key}-refine`, {})
      await action(a, "bind_evidence", `${key}-artifact`, { evidence_kind: "artifact" })
      const postReviewRef = (await verify(b, `${key}-post-review`)).operation_ref
      await action(b, "bind_evidence", `${key}-post-review-bind`, { evidence_kind: "verification", evidence_ref: postReviewRef })
      if (pass === 0) {
        firstProducer = producer
        const { readiness_evidence_refs: _, ...control } = oracle.controls[0]
        const prepared = await verify(b, "g01-checkpoint-prepare", { oracle: { phase: "prepare", expected_contract_version: 1, control_bundle: { owner: oracle.owners[0], cases: oracle.cases, control } } })
        await action(b, "record_worker_job", "g01-checkpoint-job", { job_id: "job:g01-checkpoint", predicate_ids: oracle.owners[0].predicate_ids, objective: "Review the current marker before delivery.", stopping_condition: "Return the marker's attributed disposition.", path_scope: [".concord/docs/dispatch-marker.txt"], ready: true, readiness_evidence: [prepared.operation_ref], acceptance_oracle: { ...oracle, controls: [{ ...control, readiness_evidence_refs: [prepared.operation_ref] }] } })
        const review = await dispatch(b, "review", "g01-checkpoint-corrective")
        const checkpointControl = review.packet.inputs.worker_job.acceptance_oracle.controls[0]
        const checkpointExecution = await verify(b, "g01-checkpoint-execute", { oracle: { phase: "execute", attempt_id: review.packet.attempt_id, attempt_epoch: epoch(review), worker_packet_digest: review.dispatchResult.result.worker_packet_digest, worker_job_binding: reportWorkerJob(review.packet), control_id: checkpointControl.control_id, preparation_run_ref: checkpointControl.readiness_evidence_refs[0] } })
        expect(checkpointExecution.oracle.qualification).toBe("pass")
        const correctiveReport = routeWorkerReport(agentLanes.find((item) => item.id === "review")!, review.packet, subject, await routeReviewBlock(review.packet, worktree, "correction_required", invoke, b,
          [{ finding_id: "finding:g01-checkpoint", in_scope: true, blocking: true, status: "open", evidence_refs: [checkpointExecution.operation_ref], severity: "P1", confidence: "high", detail: "The checkpoint review blocks this subject.", oracle: { classification: "delivery_blocker", owner_id: "owner:route-e2e", failure_family: "synthetic-checkpoint-readiness", predicate_ids: [predicate.predicate_id], control_ids: [checkpointControl.control_id], case_ids: checkpointControl.case_ids, evidence_refs: [checkpointExecution.operation_ref] } }]))
        correctiveReport.evidence[0].oracle_receipt = { control_ids: [checkpointControl.control_id], case_ids: checkpointControl.case_ids, recipe_source: checkpointControl.recipe_source, result: "pass", exit_code: 0, run_ref: checkpointExecution.operation_ref, evidence_refs: [] }
        const correctiveReceipt = await completeRouteTask(review, correctiveReport, runner, binary)
        expect(correctiveReceipt.outcome, JSON.stringify(correctiveReceipt)).toBe("ok")
        host.retain(review, correctiveReport)
        await accept(b, review, "g01-checkpoint-review-accept")
        const before = events()
        const refused = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version(), action_id: "complete", idempotency_key: "g01-corrective-complete", fields: { impact_verdict: "non-breaking" } } }, a)
        expect(refused.outcome).toBe("error")
        expect(events()).toBe(before)
        const correctionState = await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID } }, a)
        expect(correctionState.outcome, JSON.stringify(correctionState)).toBe("ok")
        const openFindingIDs = correctionState.result.pinned.work_pin.work_context.findings
          .filter((finding: JSONRecord) => finding.status === "open" && finding.oracle?.classification === "delivery_blocker")
          .map((finding: JSONRecord) => finding.finding_id)
        expect(openFindingIDs.length).toBeGreaterThan(0)
        await action(a, "request_correction", "g01-authorized-correction", { diagnosis: "The checkpoint review blocks this subject.", strategy: "Produce a corrected marker and obtain independent review and verification.", predicate_ids: [predicate.predicate_id], evidence_refs: [postReviewRef], open_finding_ids: openFindingIDs })
        expect(dbValue(dbPath, `SELECT current_step FROM workflow_instances WHERE work_id='${workID}'`).current_step).toBe("repair")
      } else {
        await action(b, "record_delivery", `${key}-refine-delivery`, { delivery_artifact: `commit:${subject}`, delivery_state: "asserted" })
        await action(b, "record_delivery", `${key}-gate-delivery`, { delivery_artifact: `commit:${subject}`, delivery_state: "asserted" })
        await action(c, "record_verdict", `${key}-verdict`, { verdicts: [{ predicate_id: predicate.predicate_id, receipt_refs: [lastProof] }] })
      }
      previousSubject = subject
      lastProducer = producer
    }
    seedInvestigationArtifact(dbPath, workID)
    const confirmation = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version(), action_id: "confirm_premise", selected_choice: "confirm", idempotency_key: "g01-confirm" } }, b)
    expect(confirmation.outcome, JSON.stringify(confirmation)).toBe("ok")
    await action(b, "bind_evidence", "g01-reviewed-closure", { evidence_kind: "review", immutable_subject_ref: "sha256:" + "a".repeat(64) })
    const completeVersion = version()
    await action(b, "complete", "g01-complete", { impact_verdict: "non-breaking" })
    const finalEvents = events()
    const replay = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: completeVersion, action_id: "complete", idempotency_key: "g01-complete", fields: { impact_verdict: "non-breaking" } } }, b)
    expect(replay.outcome).toBe("ok")
    expect(events()).toBe(finalEvents)
    expect(dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}' AND kind='workflow.completed'`).n).toBe(1)
    expect(host.counts()).toEqual({ coordinatorSessions: 3, taskLaunches: 8, sessionCreates: 0 })
    expect(dbValue(dbPath, `SELECT count(*) AS n FROM worker_attempts WHERE work_id='${workID}'`).n).toBe(8)
    expect(workerEffects(dbPath, workID).filter((row) => row.kind === "worker.completed")).toHaveLength(8)
    expect(new Set(calls.filter((call) => call.argv[1] === "invoke").map((call) => call.input.call_envelope.session_ref))).toEqual(new Set([a.sessionID, b.sessionID, c.sessionID]))
    expect(lastProducer!.workerSessionID).not.toBe(lastVerify!.workerSessionID)
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    if (previousConfig === undefined) delete process.env.OPENCODE_CONFIG
    else process.env.OPENCODE_CONFIG = previousConfig
  }
}, 300_000)

routeDeclaration("composes implementation and independent review through one ordinary workflow completion", async () => {
  const root = await fixtureTempRoot("dispatch-e2e")
  const previousConfig = process.env.OPENCODE_CONFIG
  try {
    const { binary, repo, dbPath, configPath, workID, worktree, lane } = await bootRouteFixture(root)
    process.env.OPENCODE_CONFIG = configPath
    const context = contextFor(worktree)
    const staleSubjectResult = await runProcess(["git", "rev-parse", "HEAD^"], "", worktree)
    expect(staleSubjectResult.exitCode, staleSubjectResult.stderr).toBe(0)
    const staleSubjectCommit = staleSubjectResult.stdout.trim()
    const subjectCommitResult = await runProcess(["git", "rev-parse", "HEAD"], "", worktree)
    expect(subjectCommitResult.exitCode, subjectCommitResult.stderr).toBe(0)
    const subjectCommit = subjectCommitResult.stdout.trim()
    expect(subjectCommit).toMatch(/^[0-9a-f]{40}$/)
    expect(dbValue(dbPath, `SELECT json_extract(intent_json,'$.external_ref') AS external_ref FROM work_items WHERE id='${workID}'`).external_ref).toBe("CON-898")
    const reviewLane = agentLanes.find((candidate) => candidate.id === "review")
    if (!reviewLane) throw new Error("review lane is not registered")
    const predicate: JSONRecord = {
      ...WORKFLOW_PREDICATE,
      outcome_payload: { ...WORKFLOW_PREDICATE.outcome_payload, immutable_subject_ref: `commit:${subjectCommit}` },
    }
    let sessionMetadata: Record<string, unknown> = {}
    let boundPacket: JSONRecord | null = null
    let activeWorkerAgent = "concord-implement"
    hostControlPlane().bind({
      get: async ({ url, path }) => {
        if (url === SESSION_MESSAGES_ROUTE) {
          // The worker readback serves the transcript through the bounded
          // message page: the opening packet, then the assistant identity.
          const id = path?.id
          expect(id).toBe("worker-session")
          const parsed = JSON.parse(exportedSession(boundPacket, 0, activeWorkerAgent)) as { messages: unknown[] }
          return { data: parsed.messages, response: new Response("[]", { status: 200 }) }
        }
        expect(url).toBe(SESSION_ROUTE)
        const id = path?.id
        expect(id === SESSION_ID || id === "worker-session").toBe(true)
        return { data: { id, directory: worktree, metadata: id === SESSION_ID ? sessionMetadata : {}, ...(id === "worker-session" ? { parentID: SESSION_ID, agent: activeWorkerAgent } : {}) }, response: new Response(null, { status: 200 }) }
      },
      patch: async ({ url, path, body }) => {
        expect(url).toBe(SESSION_ROUTE)
        expect(path).toEqual({ id: SESSION_ID })
        expect(body).toEqual({ metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } })
        sessionMetadata = { [MANAGED_TASK_SCOPE_KEY]: "managed" }
        return { response: new Response(null, { status: 200 }) }
      },
      post: async () => { throw new Error("dispatch performs zero subtask writes") },
    })
    const realCalls: Array<{ argv: string[]; input: JSONRecord }> = []
    const realRunner: DispatchRunner = {
      async run(argv, input, signal) {
        if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "worker-session", directory: worktree, parentID: SESSION_ID }, { id: SESSION_ID, directory: worktree }]), stderr: "" }
        if (argv[1] === "worker-dispatch" || argv[1] === "worker-complete" || argv[1] === "worker-fail" || argv[1] === "invoke") {
          realCalls.push({ argv, input: JSON.parse(input) as JSONRecord })
        }
        return runFixtureProcess([binary, ...argv.slice(1)], input, { env: { ...process.env, CONCORD_DB_PATH: dbPath }, signal })
      },
    }
    configureConcordAdapter({ runner: realRunner })

    const invoke = (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) => {
      if (toolName === "concord_work_transition" && args.input.action_id === "dispatch_worker") expect(sessionDirectory).toBe(worktree)
      return invokeConcordOperation(toolName, args as any, callContext, sessionDirectory)
    }
    const transition = (version: number, actionID: string, idempotencyKey: string, fields: Record<string, unknown>): Promise<JSONRecord> => invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version, action_id: actionID, idempotency_key: idempotencyKey, fields } }, context)

    await driveWorkflowToContract(workID, invoke, context, APPROVED_OBJECTIVE, [predicate], ["verification", "review"])
    const definitionDigest = dbValue(dbPath, `SELECT definition_digest FROM workflow_instances WHERE work_id='${workID}'`).definition_digest as string

    let response: JSONRecord
    for (const reportFailure of ["missing", "malformed"] as const) {
      const failedDispatch = await dispatchRouteTask(lane, workID, worktree, dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, `e2e-${reportFailure}-report-dispatch`, `e2e-${reportFailure}-report-call`, context, invoke,
        (packet, agent) => { boundPacket = packet; activeWorkerAgent = agent })
      let report: JSONRecord | string
      report = reportFailure === "missing"
        ? "no structured worker report"
        : `\`\`\`${WORKER_REPORT_PROTOCOL}\n{"schema_version":\n\`\`\``
      const terminal = await completeRouteTask(failedDispatch, report, realRunner, binary)
      expect(terminal.outcome, JSON.stringify(terminal)).toBe("error")
      const failedAttempt = dbValue(dbPath, `SELECT lifecycle_state,failure_kind,failure_detail FROM worker_attempts WHERE attempt_id='${failedDispatch.packet.attempt_id}'`)
      expect(failedAttempt.lifecycle_state).toBe("failed")
      expect(failedAttempt.failure_kind).toBe("invalid_report")
      expect(failedAttempt.failure_detail).toContain(reportFailure === "missing" ? "no agent-lane-report.v1 report" : "malformed designated report frame")
      expect(dbValue(dbPath, `SELECT count(*) AS n FROM worker_attempts WHERE work_id='${workID}' AND terminal_kind='worker.completed' AND attempt_id='${failedDispatch.packet.attempt_id}'`).n).toBe(0)
    }
    let dispatchResponse: JSONRecord | undefined
    const implementationVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    const implementation = await dispatchRouteTask(lane, workID, worktree, implementationVersion, "e2e-dispatch", "e2e-task-call", context,
      async (toolName, args, callContext, sessionDirectory) => {
        if (toolName === "concord_work_transition" && args.input.action_id === "dispatch_worker") {
          expect(sessionMetadata).toEqual({ [MANAGED_TASK_SCOPE_KEY]: "managed" })
        }
        const result = await invoke(toolName, args, callContext, sessionDirectory)
        if (toolName === "concord_work_transition" && args.input.action_id === "dispatch_worker") dispatchResponse = result
        return result
      },
      (packet, agent) => { boundPacket = packet; activeWorkerAgent = agent })
    const { dispatchResult, windowWasOpen, taskArgs, packet } = implementation
    expect(dispatchResult.outcome).toBe("ok")
    expect(dispatchResult.dispatch_state).toBe("awaiting_worker")
    expect(await hostControlPlane().taskScope(SESSION_ID)).toBe("managed")
    expect(await hostControlPlane().taskScope("worker-session")).toBe("managed")
    expect(windowWasOpen).toBe(true)
    expect(packet.inputs.report_protocol).toBe(WORKER_REPORT_PROTOCOL)
    boundPacket = packet
    expect(taskArgs.subagent_type).toBe("concord-implement")
    expect(packet.step_id).toBe("repair")
    // This non-Initiative fixture has no narrative. The task is the approved
    // objective verbatim — no adapter header, trailer, or duplicate — and the
    // typed binding carries the objective source, the versions, and the one
    // assigned result. The typed outcome predicates ride
    // inputs.outcome_predicates with each serialized payload decoded. The
    // core-verified law context carries the contract's resolved home Domain
    // with the knowledge home's absolute registry locator, and the work
    // record carries the item's recorded value statement and task.
    const registryLocator = join(repo, ".concord/docs/knowledge/domain-registry.json")
    expect(packet.inputs.law_context).toEqual({ laws: [], domains: [{ domain_id: `product-root:${PRODUCT_ID}`, name: "Synthetic root", purpose: "Synthetic test domain" }], registry_path: registryLocator })
    expect(packet.inputs.work_record).toEqual({ value_statement: "The route completes a real worker attempt.", task: "Exercise the dispatch route." })
    expect(packet.inputs.design_record).toBeUndefined()
    expect(packet.inputs.proposal_record).toBeUndefined()
    expect(await Bun.file(registryLocator).exists()).toBe(true)
    expect(packet.inputs.task).toBe(APPROVED_OBJECTIVE)
    expect(packet.inputs.binding).toEqual({ objective_source: "contract_premise", work_version: implementationVersion, contract_version: 1, assigned_result: "files_touched" })
    expect(packet.inputs.task).not.toContain(predicate.predicate_id)
    expect(packet.inputs.outcome_predicates).toEqual([predicate])
    expect(packet.inputs.constraints).toBeUndefined()
    expect(((packet.inputs as JSONRecord).worker_job as JSONRecord).job_id).toBe(ROUTE_WORKER_JOB_ID)
    expect(dispatchResponse?.result?.worker_packet_digest).toMatch(/^sha256:[0-9a-f]{64}$/)
    const dispatchEvent = dbRows(dbPath, `SELECT payload FROM worker_authorizations WHERE work_id='${workID}' AND attempt_id='${packet.attempt_id}'`)
    expect(JSON.parse(dispatchEvent[0].payload as string).worker_packet_predicate_ids).toEqual([predicate.predicate_id])
    const report = routeWorkerReport(lane, packet, subjectCommit)
    const implementationReceipt = await completeRouteTask(implementation, report, realRunner, binary)
    expect(implementationReceipt.outcome).toBe("ok")
    const attempt = dbValue(dbPath, `SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id='${packet.attempt_id}'`)
    expect(attempt.lifecycle_state).toBe("completed")
    expect(attempt.readback_model).toBe(READBACK_MODEL)
    const provenance = workerEffects(dbPath, workID).filter((row) => row.kind === "worker.dispatched" && JSON.parse(row.payload).attempt_id === packet.attempt_id)
    const provenancePayload = JSON.parse(provenance[0].payload as string)
    expect(provenancePayload.host_provenance.sources).toContainEqual({ kind: "unenumerated", path: "https://example.invalid/synthetic-instructions" })

    const evidenceVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    response = await transition(evidenceVersion, "bind_evidence", "e2e-bind-evidence", { evidence_kind: "verification" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const currentVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    const implementationEpoch = dbValue(dbPath, `SELECT json_extract(start_payload,'$.attempt_epoch') AS epoch FROM worker_authorizations WHERE work_id='${workID}' AND attempt_id='${packet.attempt_id}'`).epoch as number
    response = await transition(currentVersion, "accept_worker_result", "e2e-accept-worker", { attempt_id: packet.attempt_id, attempt_epoch: implementationEpoch })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    // The job-bound accept is local acceptance (CD-0205): it satisfies the
    // recorded job and holds repair. The step exits through its own
    // delivery assertion.
    expect(dbValue(dbPath, `SELECT current_step FROM workflow_instances WHERE work_id='${workID}'`).current_step).toBe("repair")
    expect(dbValue(dbPath, `SELECT state FROM worker_job_revisions WHERE work_id='${workID}' AND job_id='${ROUTE_WORKER_JOB_ID}'`).state).toBe("satisfied")
    const reviewInput = await invoke("concord_work_transition", { operation: "worktree_verify", input: { work_id: workID, command: ["git", "rev-parse", "HEAD"], idempotency_key: "e2e-review-input" } }, context)
    expect(reviewInput.outcome, JSON.stringify(reviewInput)).toBe("ok")
    const reviewInputRef = (reviewInput.result as JSONRecord).operation_ref as string
    const assertDeliveryVerification = async () => {
      const eventsBeforeSkippedVerify = dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n as number
      const skippedVerifyDelivery = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "record_delivery", "e2e-delivery-without-verify", { delivery_artifact: "https://github.com/synthetic/concord/pull/898", delivery_state: "asserted" })
      expect(skippedVerifyDelivery.outcome, JSON.stringify(skippedVerifyDelivery)).toBe("error")
      expect(skippedVerifyDelivery.error.kind).toBe("missing_evidence")
      expect(skippedVerifyDelivery.error.message).toContain("verification")
      expect(dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n).toBe(eventsBeforeSkippedVerify)
      // Integration needs a bound producer receipt for the actual candidate.
      const failedVerify = await invoke("concord_work_transition", { operation: "worktree_verify", input: { work_id: workID, command: ["git", "show-ref", "--verify", "refs/heads/missing-synthetic-branch"], idempotency_key: "e2e-failed-integration-verify" } }, context)
      expect(failedVerify.outcome, JSON.stringify(failedVerify)).toBe("ok")
      expect((failedVerify.result as JSONRecord).exit_code).not.toBe(0)
      const failedVerifyRef = (failedVerify.result as JSONRecord).operation_ref as string
      expect(failedVerifyRef).toMatch(/^worktree_verify:/)
      expect(dbValue(dbPath, `SELECT count(*) AS n FROM durable_operations WHERE op_id='${failedVerifyRef}'`).n).toBe(0)
      response = await invoke("concord_work_transition", { operation: "worktree_verify", input: { work_id: workID, command: ["git", "rev-parse", "HEAD"], idempotency_key: "e2e-repair-integration-verify" } }, context)
      expect(response.outcome, JSON.stringify(response)).toBe("ok")
      const integrationRef = (response.result as JSONRecord).operation_ref as string
      expect(integrationRef).toMatch(/^worktree_verify:/)
      expect(((response.result as JSONRecord).output as string).trim()).toBe(subjectCommit)
      const integrationVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
      const staleSubjectBinding = await transition(integrationVersion, "bind_evidence", "e2e-bind-stale-subject", { evidence_kind: "verification", evidence_ref: integrationRef, immutable_subject_ref: `commit:${staleSubjectCommit}` })
      expect(staleSubjectBinding.outcome, JSON.stringify(staleSubjectBinding)).toBe("ok")
      const eventsAfterStaleSubject = dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n as number
      const staleSubjectDelivery = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "record_delivery", "e2e-delivery-with-stale-subject", { delivery_artifact: "https://github.com/synthetic/concord/pull/898", delivery_state: "asserted" })
      expect(staleSubjectDelivery.outcome, JSON.stringify(staleSubjectDelivery)).toBe("error")
      expect(staleSubjectDelivery.error.kind).toBe("missing_evidence")
      expect(staleSubjectDelivery.error.message).toBe("the delivery assertion requires passing producer verification on the current integrated candidate, covering every required Project")
      expect(dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n).toBe(eventsAfterStaleSubject)
      response = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "bind_evidence", "e2e-bind-integration", { evidence_kind: "verification", evidence_ref: integrationRef })
      expect(response.outcome, JSON.stringify(response)).toBe("ok")
    }
    const sourceOracle = packet.inputs.worker_job.acceptance_oracle
    const { readiness_evidence_refs: previousReadiness, ...reviewControl } = sourceOracle.controls[0]
    const preparedReview = await invoke("concord_work_transition", { operation: "worktree_verify", input: { work_id: workID, idempotency_key: "e2e-review-prepare", oracle: { phase: "prepare", expected_contract_version: 1, control_bundle: { owner: sourceOracle.owners[0], cases: sourceOracle.cases, control: reviewControl } } } }, context)
    expect(preparedReview.outcome, JSON.stringify(preparedReview)).toBe("ok")
    const reviewOracle = { ...sourceOracle, controls: [{ ...reviewControl, readiness_evidence_refs: [(preparedReview.result as JSONRecord).operation_ref] }] }
    const refineDeliveryVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    response = await transition(refineDeliveryVersion, "record_worker_job", "e2e-corrective-worker-job", {
      job_id: "job:route-e2e-corrective-review",
      predicate_ids: packet.inputs.worker_job.predicate_ids,
      objective: "Independently evaluate the pinned synthetic commit.",
      stopping_condition: "The report states whether the subject is safe to ship.",
      path_scope: ["internal/store"],
      checks: ["git status --short"],
      ready: true,
      readiness_evidence: ["evidence:route-e2e-corrective-ready"],
      acceptance_oracle: reviewOracle,
    })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const failedReviewDispatch = await dispatchRouteTask(reviewLane, workID, worktree, dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "e2e-failed-review-dispatch", "e2e-failed-review-call", context, invoke,
      (reviewPacket, agent) => { boundPacket = reviewPacket; activeWorkerAgent = agent })
    const failedReviewReport = { ...routeWorkerReport(reviewLane, failedReviewDispatch.packet, subjectCommit, await routeReviewBlock(failedReviewDispatch.packet, worktree, "correction_required", invoke, context)), status: "failed" }
    const failedReviewReceipt = await completeRouteTask(failedReviewDispatch, failedReviewReport, realRunner, binary)
    expect(failedReviewReceipt.outcome).toBe("error")
    const failedReviewAttempt = dbValue(dbPath, `SELECT lifecycle_state,failure_kind,failure_detail FROM worker_attempts WHERE attempt_id='${failedReviewDispatch.packet.attempt_id}'`)
    expect(failedReviewAttempt.lifecycle_state).toBe("failed")
    expect(failedReviewAttempt.failure_kind).toBe("worker_error")
    expect(failedReviewAttempt.failure_detail).toContain("worker reported failure")
    const correctiveDispatchVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    const correctiveReview = await dispatchRouteTask(reviewLane, workID, worktree, correctiveDispatchVersion, "e2e-corrective-review-dispatch", "e2e-corrective-review-call", context, invoke,
      (reviewPacket, agent) => { boundPacket = reviewPacket; activeWorkerAgent = agent })
    const correctiveReceipt = await completeRouteTask(correctiveReview, routeWorkerReport(reviewLane, correctiveReview.packet, subjectCommit, await routeReviewBlock(correctiveReview.packet, worktree, "correction_required", invoke, context,
      [{ finding_id: "finding:route-blocker", in_scope: true, blocking: true, status: "open", evidence_refs: [reviewInputRef], severity: "P1", confidence: "high", detail: "Synthetic review blocks delivery on the pinned subject.", oracle: {
        classification: "delivery_blocker", owner_id: "owner:route-e2e", failure_family: "synthetic-route-readiness",
        predicate_ids: [predicate.predicate_id], control_ids: ["control:route-e2e"], case_ids: ["case:route-e2e"],
        evidence_refs: ["evidence:route-e2e-corrective"],
      } }],
    )), realRunner, binary)
    expect(correctiveReceipt.outcome, JSON.stringify(correctiveReceipt)).toBe("ok")
    expect(correctiveReceipt.review.disposition).toBe("correction_required")
    expect(correctiveReview.packet.work_id).toBe(workID)
    expect(correctiveReview.packet.attempt_id).not.toBe(packet.attempt_id)
    expect(correctiveReview.packet.inputs.outcome_predicates).toEqual([predicate])
    const correctiveEpoch = dbValue(dbPath, `SELECT json_extract(start_payload,'$.attempt_epoch') AS epoch FROM worker_authorizations WHERE work_id='${workID}' AND attempt_id='${correctiveReview.packet.attempt_id}'`).epoch as number
    const correctiveAcceptVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    const eventsBeforeWrongReviewer = dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n as number
    const implementationAsReview = await transition(correctiveAcceptVersion, "accept_worker_result", "e2e-implementation-as-review", { attempt_id: packet.attempt_id, attempt_epoch: correctiveEpoch })
    expect(implementationAsReview.outcome, JSON.stringify(implementationAsReview)).toBe("error")
    expect(implementationAsReview.error.message).toBe("worker attempt dispatch is stale")
    expect(dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n).toBe(eventsBeforeWrongReviewer)
    const staleCorrectiveEpoch = await transition(correctiveAcceptVersion, "accept_worker_result", "e2e-stale-review-epoch", { attempt_id: correctiveReview.packet.attempt_id, attempt_epoch: correctiveEpoch + 1 })
    expect(staleCorrectiveEpoch.outcome, JSON.stringify(staleCorrectiveEpoch)).toBe("error")
    expect(staleCorrectiveEpoch.error.message).toContain("epoch")
    expect(dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n).toBe(eventsBeforeWrongReviewer)
    response = await invoke("concord_work_transition", { operation: "worktree_verify", input: { work_id: workID, command: ["git", "rev-parse", "HEAD"], idempotency_key: "e2e-corrective-review-verify" } }, context)
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    expect(((response.result as JSONRecord).output as string).trim()).toBe(subjectCommit)
    response = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "accept_worker_result", "e2e-accept-corrective-review", { attempt_id: correctiveReview.packet.attempt_id, attempt_epoch: correctiveEpoch })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const eventsAfterCorrectiveAccept = dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n as number
    const blockedDelivery = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "record_delivery", "e2e-corrective-delivery-refusal", { delivery_artifact: "https://github.com/synthetic/concord/pull/898", delivery_state: "asserted" })
    expect(blockedDelivery.outcome, JSON.stringify(blockedDelivery)).toBe("error")
    expect(blockedDelivery.error.message).toContain("open in-scope blocking finding")
    expect(dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n).toBe(eventsAfterCorrectiveAccept)
    response = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "record_worker_job", "e2e-resolving-worker-job", {
      job_id: "job:route-e2e-resolving-review",
      predicate_ids: packet.inputs.worker_job.predicate_ids,
      objective: "Independently re-review the pinned synthetic commit.",
      stopping_condition: "A fresh ship report settles review evidence for the subject.",
      path_scope: ["internal/store"],
      checks: ["git status --short"],
      ready: true,
      readiness_evidence: ["evidence:route-e2e-resolving-ready"],
      acceptance_oracle: reviewOracle,
    })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const resolvingReviewVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    const resolvingReview = await dispatchRouteTask(reviewLane, workID, worktree, resolvingReviewVersion, "e2e-resolving-review-dispatch", "e2e-resolving-review-call", context, invoke,
      (reviewPacket, agent) => { boundPacket = reviewPacket; activeWorkerAgent = agent })
    const resolvingJob = resolvingReview.packet.inputs.worker_job
    const resolvingControl = resolvingJob.acceptance_oracle.controls[0]
    const resolvingEpoch = dbValue(dbPath, `SELECT json_extract(start_payload,'$.attempt_epoch') AS epoch FROM worker_authorizations WHERE work_id='${workID}' AND attempt_id='${resolvingReview.packet.attempt_id}'`).epoch as number
    const openFinding = resolvingReview.packet.inputs.work_context.findings.find((finding: JSONRecord) => finding.status === "open" && finding.oracle?.classification === "delivery_blocker")
    expect(openFinding).toBeDefined()
    response = await invoke("concord_work_transition", { operation: "worktree_verify", input: {
      work_id: workID, idempotency_key: "e2e-resolving-oracle-execute", requested_budget_seconds: 300,
      oracle: { phase: "execute", attempt_id: resolvingReview.packet.attempt_id, attempt_epoch: resolvingEpoch,
        worker_packet_digest: resolvingReview.dispatchResult.result.worker_packet_digest, worker_job_binding: reportWorkerJob(resolvingReview.packet),
        control_id: resolvingControl.control_id, preparation_run_ref: resolvingControl.readiness_evidence_refs[0] },
    } }, context)
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    expect(response.result.oracle.qualification).toBe("pass")
    const resolvingRunRef = response.result.operation_ref as string
    const resolvingReport = routeWorkerReport(reviewLane, resolvingReview.packet, subjectCommit, await routeReviewBlock(resolvingReview.packet, worktree, "ready_for_final_proof", invoke, context, [], [resolvingRunRef]))
    resolvingReport.review.resolved_findings = [{ finding_id: openFinding.finding_id, evidence_refs: [resolvingRunRef] }]
    resolvingReport.evidence[0].oracle_receipt = {
      control_ids: [resolvingControl.control_id], case_ids: resolvingControl.case_ids, recipe_source: resolvingControl.recipe_source,
      result: "pass", exit_code: response.result.exit_code, run_ref: response.result.operation_ref, evidence_refs: [],
    }
    const resolvingReceipt = await completeRouteTask(resolvingReview, resolvingReport, realRunner, binary)
    expect(resolvingReceipt.outcome, JSON.stringify(resolvingReceipt)).toBe("ok")
    expect(resolvingReceipt.review.disposition).toBe("ready_for_final_proof")
    const resolvingAcceptVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    response = await transition(resolvingAcceptVersion, "accept_worker_result", "e2e-accept-resolving-review", { attempt_id: resolvingReview.packet.attempt_id, attempt_epoch: resolvingEpoch })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    await assertDeliveryVerification()
    response = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "record_delivery", "e2e-record-repair-delivery", { delivery_artifact: "https://github.com/synthetic/concord/pull/898", delivery_state: "asserted" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    response = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "checkpoint_refine", "e2e-start-refine", {})
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    expect(dbValue(dbPath, `SELECT definition_digest FROM workflow_instances WHERE work_id='${workID}'`).definition_digest).toBe(definitionDigest)
    expect(dbValue(dbPath, `SELECT current_step FROM workflow_instances WHERE work_id='${workID}'`).current_step).toBe("refine")
    response = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "bind_evidence", "e2e-bind-refine-artifact", { evidence_kind: "artifact" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    response = await invoke("concord_work_transition", { operation: "worktree_verify", input: { work_id: workID, command: ["git", "rev-parse", "HEAD"], idempotency_key: "e2e-resolving-review-verify" } }, context)
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    expect(((response.result as JSONRecord).output as string).trim()).toBe(subjectCommit)
    response = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "bind_evidence", "e2e-bind-resolving-review-verify", { evidence_kind: "verification", evidence_ref: (response.result as JSONRecord).operation_ref })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    response = await invoke("concord_work_transition", { operation: "worktree_verify", input: { work_id: workID, command: ["git", "rev-parse", "HEAD"], idempotency_key: "e2e-post-review-delivery-verify" } }, context)
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    expect(((response.result as JSONRecord).output as string).trim()).toBe(subjectCommit)
    response = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "bind_evidence", "e2e-bind-post-review-delivery-verify", { evidence_kind: "verification", evidence_ref: (response.result as JSONRecord).operation_ref })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    response = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "record_delivery", "e2e-record-refine-delivery", { delivery_artifact: "https://github.com/synthetic/concord/pull/898", delivery_state: "asserted" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const gateVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    expect(dbValue(dbPath, `SELECT current_step FROM workflow_instances WHERE work_id='${workID}'`).current_step).toBe("delivery")
    response = await transition(gateVersion, "record_delivery", "e2e-record-gate-delivery", { delivery_artifact: "https://github.com/synthetic/concord/pull/898", delivery_state: "asserted" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    expect(dbValue(dbPath, `SELECT current_step FROM workflow_instances WHERE work_id='${workID}'`).current_step).toBe("verify")
    const verifyVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    const selfVerdict = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: verifyVersion, action_id: "record_verdict", idempotency_key: "e2e-record-attempt-as-proof", fields: { verdicts: [{ predicate_id: predicate.predicate_id, receipt_refs: [packet.attempt_id] }] } } }, context)
    expect(selfVerdict.outcome).toBe("error")
    expect((selfVerdict.error as JSONRecord).kind).toBe("missing_evidence")
    response = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: verifyVersion, action_id: "record_verdict", idempotency_key: "e2e-record-verdict", fields: { verdicts: [{ predicate_id: predicate.predicate_id, receipt_refs: [resolvingRunRef] }] } } }, context)
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const verdictActor = dbValue(dbPath, `SELECT json_extract(payload,'$.verdict_actor_ref') AS actor FROM workflow_predicate_verdicts WHERE work_id='${workID}' AND predicate_id='${predicate.predicate_id}' ORDER BY verdict_seq DESC LIMIT 1`).actor as string
    const verdictActorClass = dbValue(dbPath, `SELECT actor_class FROM workflow_actors WHERE actor_ref='${verdictActor}'`).actor_class as string
    expect(verdictActorClass).toBe("agent")
    const verdictVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    // The operator question gate requires an investigation observation naming
    // a current Domain of the Product and another work item.
    seedInvestigationArtifact(dbPath, workID)
    const continuity = await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID, page: { cursor: null, limit: 1 } } }, context)
    const continuityResult = continuity.result as JSONRecord
    // Engineering work opens no operator question: the coordinator records
    // its own confirm choice, and the pin requires the closed choice alone.
    expect((continuityResult.pinned as JSONRecord).pending_operator_decision ?? null).toBeNull()
    const pinIntents = ((((continuityResult.pinned as JSONRecord).work_pin as JSONRecord).next_valid_intents ?? []) as JSONRecord[])
    const confirmIntent = pinIntents.find((intent) => intent.action_id === "confirm_premise")
    expect(confirmIntent, JSON.stringify(pinIntents)).toBeDefined()
    expect((confirmIntent as JSONRecord).required_fields).toEqual(["selected_choice"])
    response = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: verdictVersion, action_id: "confirm_premise", selected_choice: "confirm", idempotency_key: "e2e-confirm" } }, context)
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    response = await transition(dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number, "bind_evidence", "e2e-reviewed-closure", { evidence_kind: "review", immutable_subject_ref: "sha256:" + "b".repeat(64) })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    const completeVersion = dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number
    response = await transition(completeVersion, "complete", "e2e-complete", { impact_verdict: "non-breaking" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    expect(dbValue(dbPath, `SELECT instance_state FROM workflow_instances WHERE work_id='${workID}'`).instance_state).toBe("completed")
    const completionEvents = dbRows(dbPath, `SELECT * FROM effect_receipts WHERE subject_id='${workID}' AND kind='workflow.completed'`)
    expect(completionEvents).toHaveLength(1)
    const deliveryEvents = dbRows(dbPath, `SELECT payload FROM workflow_delivery_state WHERE work_id='${workID}'`)
    expect(deliveryEvents.map((row) => JSON.parse(row.payload as string).delivery_artifact)).toContain("https://github.com/synthetic/concord/pull/898")
    const implementationEvent = workerEffects(dbPath, workID).filter((row) => row.kind === "worker.completed" && JSON.parse(row.payload).attempt_id === packet.attempt_id)
    const implementationEvidence = JSON.parse(implementationEvent[0].payload as string).evidence as JSONRecord[]
    expect(implementationEvidence.some((entry) => entry.detail.includes(`commit:${subjectCommit}`))).toBe(true)
    const finalReviewEvent = workerEffects(dbPath, workID).filter((row) => row.kind === "worker.completed" && JSON.parse(row.payload).attempt_id === resolvingReview.packet.attempt_id)
    const finalReviewPayload = JSON.parse(finalReviewEvent[0].payload as string)
    expect(finalReviewPayload.review.disposition).toBe("ready_for_final_proof")
    expect((finalReviewPayload.evidence as JSONRecord[]).some((entry) => entry.detail.includes(`commit:${subjectCommit}`))).toBe(true)
    const renderedReceipt = await runProcess([binary, "receipt"], JSON.stringify({ work_id: workID }), worktree, { CONCORD_DB_PATH: dbPath })
    expect(renderedReceipt.exitCode, renderedReceipt.stderr).toBe(0)
    expect(renderedReceipt.stdout).toContain(workID)
    expect(renderedReceipt.stdout).toContain("Complete")
    expect(renderedReceipt.stdout).toContain("https://github.com/synthetic/concord/pull/898")
    const reopenedContinuity = await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID, page: { cursor: null, limit: 1 } } }, context)
    expect(reopenedContinuity.outcome).toBe("ok")
    const attemptCountBeforeReplay = dbValue(dbPath, `SELECT count(*) AS n FROM worker_attempts WHERE work_id='${workID}'`).n as number
    const dispatchCountBeforeReplay = realCalls.filter((call) => call.argv[1] === "worker-dispatch").length
    const eventCountBeforeReplay = dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n as number
    response = await transition(completeVersion, "complete", "e2e-complete", { impact_verdict: "non-breaking" })
    expect(response.outcome, JSON.stringify(response)).toBe("ok")
    expect(dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE subject_id='${workID}'`).n).toBe(eventCountBeforeReplay)
    expect(dbRows(dbPath, `SELECT * FROM effect_receipts WHERE subject_id='${workID}' AND kind='workflow.completed'`)).toEqual(completionEvents)
    expect(dbValue(dbPath, `SELECT count(*) AS n FROM worker_attempts WHERE work_id='${workID}'`).n).toBe(attemptCountBeforeReplay)
    expect(realCalls.filter((call) => call.argv[1] === "worker-dispatch")).toHaveLength(dispatchCountBeforeReplay)
    const replayedReceipt = await runProcess([binary, "receipt"], JSON.stringify({ work_id: workID }), worktree, { CONCORD_DB_PATH: dbPath })
    expect(replayedReceipt.exitCode, replayedReceipt.stderr).toBe(0)
    expect(replayedReceipt.stdout).toBe(renderedReceipt.stdout)
    const verdicts = dbRows(dbPath, `SELECT payload FROM workflow_predicate_verdicts WHERE work_id='${workID}'`)
    expect(verdicts.map((row) => JSON.parse(row.payload as string).predicate_id)).toContain(predicate.predicate_id)
    const workerDispatch = realCalls.find((call) => call.argv[1] === "worker-dispatch")?.input
    const workerComplete = realCalls.find((call) => call.argv[1] === "worker-complete")?.input
    requiredFields(workerDispatch ?? {}, ["event_id", "work_id", "attempt_id", "lane_id", "lane_digest", "packet_schema_version", "report_schema_version", "packet_digest", "host_provenance", "assertion"])
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
  }
}, 600_000)

routeDeclaration("G07 retains sourced research pinned by another coordinator across worker dispatch and restart", async () => {
  const root = await fixtureTempRoot("dispatch-g07")
  const previousConfig = process.env.OPENCODE_CONFIG
  try {
    const { binary, dbPath, configPath, workID, worktree, lane } = await bootRouteFixture(root)
    process.env.OPENCODE_CONFIG = configPath
    const a = contextFor(worktree, "g07-coordinator-a")
    const b = contextFor(worktree, "g07-coordinator-b")
    const host = bindRouteSessions(worktree, [a.sessionID, b.sessionID])
    const calls: Array<{ argv: string[]; input: JSONRecord }> = []
    const runner = realStoreRunner(binary, dbPath, calls, worktree)
    // The combined rejection's post-commit launch owner probes the worker
    // evidence credential before it opens the window; bind the fixture seed
    // so the probe stays hermetic instead of reading the host keyring.
    configureConcordAdapter({ runner, credentials: { async getPrivateKey() { return PRIVATE_SEED } } })
    const invoke = (tool: string, args: any, context: any) => invokeConcordOperation(tool, args, context) as Promise<JSONRecord>
    const ok = async (tool: string, operation: string, input: JSONRecord, context: any) => {
      const response = await invoke(tool, { operation, input }, context)
      expect(response.outcome, JSON.stringify(response)).toBe("ok")
      return response.result as JSONRecord
    }
    const created = await ok("concord_work_define", "capture", {
      kind: "task", title: "Synthetic sourced research owner", value_statement: "Retain the synthetic source and finding.",
      task: "Author the synthetic research pack.", project_ids: [PROJECT_ID], idempotency_key: "g07-owner",
    }, a)
    const ownerID = created.changed_refs.find((ref: JSONRecord) => ref.entity_kind === "work_item").id as string
    expect(ownerID).not.toBe(workID)
    const createdPack = await ok("concord_work_define", "research_pack_create", {
      owner_work_id: ownerID, revision: { question: "Which synthetic source owns the route?", method: "source inspection" }, idempotency_key: "g07-pack",
    }, a)
    const packID = createdPack.changed_refs.find((ref: JSONRecord) => ref.entity_kind === "research_pack").id as string
    const packVersion = () => dbValue(dbPath, `SELECT expected_version FROM active_research_packs WHERE pack_id='${packID}'`).expected_version
    const workVersion = (id = workID) => dbValue(dbPath, `SELECT version FROM work_items WHERE id='${id}'`).version
    const source = { source_id: "source:g07", kind: "source_code", locator: "oraclekit/oracle.go", title: "Synthetic route source", publisher_or_author: "Synthetic Author", accessed_at: "2026-10-12T00:00:00Z" }
    const finding = { finding_id: "finding:g07", kind: "observation", statement: "The synthetic source returns true.", confidence: "high", freshness: "current" }
    // Negative control: a second revision-one finding the declaration never
    // selects, so the shared view must carry exactly the selection.
    const unselected = { finding_id: "finding:g07-b", kind: "observation", statement: "The unselected sibling finding stays out of the shared view.", confidence: "medium", freshness: "current" }
    const writePack = (operation: string, key: string, input: JSONRecord) => ok("concord_work_define", operation, { pack_id: packID, expected_version: packVersion(), idempotency_key: key, ...input }, a)
    await writePack("research_source_record", "g07-source-1", { source })
    await writePack("research_finding_record", "g07-finding-1", { finding, source_ids: [source.source_id] })
    await writePack("research_finding_record", "g07-finding-1-b", { finding: unselected })
    const readInput = { product_id: PRODUCT_ID, pack_id: packID, result_version: 2, revision: 1 }
    const read = () => ok("concord_work_trace", "research", readInput, b)
    const original = (await read()).pack.revisions as JSONRecord[]
    expect(original).toHaveLength(1)
    expect(original[0].revision).toBe(1)
    expect(original[0].findings).toHaveLength(2)
    expect(original[0].findings[0]).toMatchObject({ ...finding, source_ids: [source.source_id] })
    expect(original[0].findings[1]).toMatchObject(unselected)
    expect(original[0].sources).toHaveLength(1)
    expect(original[0].sources[0]).toMatchObject(source)
    await writePack("research_revision_append", "g07-revision-2", { revision: { question: "Which revised synthetic source owns the route?", method: "source inspection" } })
    await writePack("research_source_record", "g07-source-2", { source: { ...source, title: "Revised source title" } })
    await writePack("research_finding_record", "g07-finding-2", { finding: { ...finding, statement: "Revision two has a different finding." }, source_ids: [source.source_id] })
    const headRead = await ok("concord_work_trace", "research", { ...readInput, revision: 2 }, b)
    expect(headRead.pack.revisions[0].findings[0].statement).not.toBe(finding.statement)
    expect(headRead.pack.revisions[0].sources[0].title).not.toBe(source.title)
    await driveWorkflowToContract(workID, invoke, b)
    const rootDomain = `product-root:${PRODUCT_ID}`
    const binding = { pack_id: packID, revision: 1, use_role: "context", required: true }
    const bindAction = (key: string, required: boolean, revision: number) => ({ work_id: workID, expected_version: workVersion(), action_id: "bind_evidence", idempotency_key: key, fields: { evidence_kind: "verification" }, research_bindings: [{ pack_id: packID, revision, use_role: "context", required }] })
    await ok("concord_work_transition", "workflow_action", bindAction("g07-required-pin", true, 1), b)
    const pins = () => dbRows(dbPath, `SELECT pack_id,revision,consumer_work_id,use_role,required FROM active_research_consumers WHERE pack_id='${packID}'`)
    expect(pins()).toEqual([{ ...binding, consumer_work_id: workID, required: 1 }])
    expect((await read()).pack.revisions).toEqual(original)
    // The coordinator authors the exact selection through the public
    // record_work_context action. The pack pin above satisfies the authoring
    // validation, and the root Domain carries its product-wide rationale
    // exactly as a reading entry does.
    const selectionEntry = { pack_id: packID, revision: 1, finding_id: finding.finding_id, domain_id: rootDomain, product_wide_rationale: "The synthetic route source owns the selected research." }
    const contextAction = (key: string, selections: JSONRecord[]) => ({ work_id: workID, expected_version: workVersion(), action_id: "record_work_context", idempotency_key: key, fields: { finding_refs: [], context_findings: [], research_finding_refs: selections } })
    await ok("concord_work_transition", "workflow_action", contextAction("g07-research-context", [selectionEntry]), b)
    // Coordinator continuity carries the selected exact revision, finding,
    // and source descriptors inside the shared view, with the unselected
    // sibling and the newer revisions as negative controls.
    const sourceView = { pack_id: packID, revision: 1, ...source }
    const findingView = { pack_id: packID, revision: 1, finding_id: finding.finding_id, kind: "observation", statement: "The synthetic source returns true.", confidence: "high", freshness: "current", status: "active", source_ids: [source.source_id], scopes: { mode: "home" } }
    const pinnedContextOf = async () => {
      const continuity = (await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID, page: { cursor: null, limit: 1 } } }, b)).result as JSONRecord
      return ((continuity.pinned as JSONRecord).work_pin as JSONRecord).work_context as JSONRecord
    }
    const pinnedContext = await pinnedContextOf()
    const continuityResearch = pinnedContext.research_findings as JSONRecord[]
    expect(continuityResearch).toHaveLength(1)
    const continuityEntry = continuityResearch[0]
    expect(continuityEntry.view_id).toMatch(/^research:\d+:0$/)
    expect(continuityEntry.selection).toEqual(selectionEntry)
    expect(continuityEntry.required).toBe(true)
    expect(continuityEntry.state).toBe("resolved")
    expect(typeof continuityEntry.selection_event_id).toBe("string")
    expect(Number.isInteger(continuityEntry.selection_event_seq)).toBe(true)
    expect(continuityEntry.ordinal).toBe(0)
    expect(continuityEntry.finding).toEqual(findingView)
    expect(continuityEntry.sources).toEqual([sourceView])
    const continuityResearchText = JSON.stringify(continuityResearch)
    expect(continuityResearchText).not.toContain("finding:g07-b")
    expect(continuityResearchText).not.toContain("Revision two")
    expect(continuityResearchText).not.toContain("Revised source title")
    expect(pinnedContext.domain_groups).toEqual([{ domain_id: rootDomain, required_reading_ordinals: [], finding_ids: [continuityEntry.view_id] }])
    // Zero Task calls, zero new attempts and authorization events, and
    // unchanged operation effects for every required unresolved selection:
    // the dispatch refuses through the same admission owner on the normal
    // route, and no window arms.
    const snapshot = () => ({
      effects: dbRows(dbPath, "SELECT * FROM effect_receipts ORDER BY effect_seq"),
      sequences: dbRows(dbPath, "SELECT * FROM store_sequences ORDER BY name"),
      revision: dbRows(dbPath, "SELECT * FROM store_revision"),
      work: dbRows(dbPath, "SELECT id,version,lifecycle FROM work_items ORDER BY id"),
      packs: dbRows(dbPath, "SELECT * FROM active_research_packs ORDER BY pack_id"),
      revisions: dbRows(dbPath, "SELECT * FROM active_research_revisions ORDER BY pack_id,revision"),
      findings: dbRows(dbPath, "SELECT * FROM active_research_findings ORDER BY pack_id,revision,finding_id"),
      sources: dbRows(dbPath, "SELECT * FROM active_research_sources ORDER BY pack_id,revision,source_id"),
      citations: dbRows(dbPath, "SELECT * FROM active_research_finding_sources ORDER BY pack_id,revision,finding_id,source_id"),
      consumers: dbRows(dbPath, "SELECT * FROM active_research_consumers ORDER BY pack_id,revision,consumer_work_id"),
      attempts: dbRows(dbPath, "SELECT * FROM worker_attempts ORDER BY attempt_id"),
      receipts: dbRows(dbPath, "SELECT * FROM idempotency_records ORDER BY principal_ref,tool,operation_kind,idempotency_key"),
    })
    const refusedDispatch = async (key: string, state: string) => {
      const before = snapshot()
      const beforeCounts = host.counts()
      const windows = new DispatchWindows()
      const response = await dispatchLaneWorker({ work_id: workID, expected_version: workVersion(), idempotency_key: key, lane_id: lane.id } as any, {
        context: b,
        invoke: invoke as any,
        credentials: { async getPrivateKey() { return PRIVATE_SEED } } satisfies CredentialStore,
        windows,
      })
      // The lane route carries the adapter-built packet to the core's
      // dispatch admission, which refuses the required unresolved selection
      // with no effect and no window.
      expect(response.outcome, JSON.stringify(response)).toBe("error")
      const refusalError = response.error
      const refusalCore = (refusalError?.details?.core_error ?? null) as JSONRecord | null
      expect(refusalCore?.kind).toBe("stale_requires_review")
      expect(refusalCore?.effect_state).toBe("none")
      expect(refusalError?.message ?? "").toContain(state)
      expect(snapshot()).toEqual(before)
      expect(host.counts()).toEqual(beforeCounts)
      expect(windows.has(b.sessionID)).toBe(false)
      expect(dispatchWindows().has(b.sessionID)).toBe(false)
    }
    // Rebound (public): rebinding the pin to another revision turns the
    // selection rebound; the view reports the binding's state without its
    // content, and the dispatch refuses until the pin returns.
    await ok("concord_work_transition", "workflow_action", bindAction("g07-rebind-rev2", true, 2), b)
    expect(pins()).toEqual([{ pack_id: packID, revision: 2, consumer_work_id: workID, use_role: "context", required: 1 }])
    const reboundContext = await pinnedContextOf()
    const reboundEntry = (reboundContext.research_findings as JSONRecord[])[0]
    expect(reboundEntry.state).toBe("rebound")
    expect(reboundEntry.finding).toBeUndefined()
    expect(reboundEntry.sources).toBeUndefined()
    await refusedDispatch("g07-dispatch-rebound", "rebound")
    await ok("concord_work_transition", "workflow_action", bindAction("g07-rebind-restore", true, 1), b)
    expect(pins()).toEqual([{ ...binding, consumer_work_id: workID, required: 1 }])
    // Stale (public): a freshness review of the pinned revision turns the
    // selection stale; restoring current resolves it again.
    await writePack("research_freshness_set", "g07-stale-dispatch", { revision: 1, freshness: "stale" })
    await refusedDispatch("g07-dispatch-stale", "stale")
    await writePack("research_freshness_set", "g07-current-restore", { revision: 1, freshness: "current" })
    // Missing (distinguished direct corpus control): the missing state is
    // unreachable through public routes while a pin is held, because the
    // retirement refuses with an active pin and a binding absence cannot be
    // authored fail-closed. This control deletes the consumer row and
    // restores its exact values, the same unreachable-corpus injection the
    // store owner tests use; it is not a public lifecycle route.
    const consumerRow = dbValue(dbPath, `SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers WHERE pack_id='${packID}' AND consumer_work_id='${workID}'`)
    dbExec(dbPath, "DELETE FROM active_research_consumers WHERE pack_id=? AND consumer_work_id=?", [packID, workID])
    await refusedDispatch("g07-dispatch-missing", "missing")
    dbExec(dbPath, "INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES(?,?,?,?,?,?)", [consumerRow.pack_id, consumerRow.revision, consumerRow.consumer_work_id, consumerRow.use_role, consumerRow.required, consumerRow.accepted_at])
    // Inactive (distinguished direct corpus control): no public verb changes
    // a stored finding's status, so this control contradicts the finding and
    // restores it, exactly as the store owner's drift tests inject.
    dbExec(dbPath, "UPDATE active_research_findings SET status='contradicted' WHERE pack_id=? AND revision=1 AND finding_id=?", [packID, finding.finding_id])
    await refusedDispatch("g07-dispatch-inactive", "inactive")
    dbExec(dbPath, "UPDATE active_research_findings SET status='active' WHERE pack_id=? AND revision=1 AND finding_id=?", [packID, finding.finding_id])
    // Over-bound selection (public): a second synthetic pack carries ten
    // maximal findings, and one declaration selecting them all refuses at the
    // post-append view check with unchanged operation effects.
    const createdOverPack = await ok("concord_work_define", "research_pack_create", {
      owner_work_id: ownerID, revision: { question: "Which oversized synthetic finding overflows the shared view?", method: "source inspection" }, idempotency_key: "g07-over-pack",
    }, a)
    const overPackID = createdOverPack.changed_refs.find((ref: JSONRecord) => ref.entity_kind === "research_pack").id as string
    const overPackVersion = () => dbValue(dbPath, `SELECT expected_version FROM active_research_packs WHERE pack_id='${overPackID}'`).expected_version
    const overSelections: JSONRecord[] = []
    for (let index = 0; index < 10; index++) {
      const overFinding = { finding_id: `finding:g07-over-${index}`, kind: "inference", statement: "R".repeat(8192), confidence: "high", freshness: "current" }
      await ok("concord_work_define", "research_finding_record", { pack_id: overPackID, expected_version: overPackVersion(), idempotency_key: `g07-over-${index}`, finding: overFinding }, a)
      overSelections.push({ pack_id: overPackID, revision: 1, finding_id: overFinding.finding_id, domain_id: rootDomain, product_wide_rationale: "The oversized synthetic finding classifies to the fixture root." })
    }
    await ok("concord_work_transition", "workflow_action", { work_id: workID, expected_version: workVersion(), action_id: "bind_evidence", idempotency_key: "g07-over-pin", fields: { evidence_kind: "verification" }, research_bindings: [{ pack_id: overPackID, revision: 1, use_role: "context", required: true }] }, b)
    const beforeOverBound = snapshot()
    const overRefused = await invoke("concord_work_transition", { operation: "workflow_action", input: contextAction("g07-over-context", overSelections) }, b)
    expect(overRefused.outcome, JSON.stringify(overRefused).slice(0, 2000)).toBe("error")
    expect(overRefused.error.kind).toBe("limit_exceeded")
    expect(overRefused.error.effect_state).toBe("none")
    expect(snapshot()).toEqual(beforeOverBound)
    b.workerSessionID = "g07-implement-worker"
    const worker = await dispatchRouteTask(lane, workID, worktree, workVersion(), "g07-dispatch", "g07-task", b, invoke, (packet, agent) => host.opening(b, packet, agent))
    expect(worker.taskArgs.subagent_type).toBe("concord-implement")
    expect(worker.packet.work_id).toBe(workID)
    // The selected research rides the shared work context the packet copies
    // verbatim from the work pin; no separate research input member exists.
    // The packet's entries are the coordinator continuity view's exact copy,
    // with the unselected sibling and the newer revisions as negative
    // controls.
    expect(worker.packet.inputs.research_bindings).toBeUndefined()
    const packetWorkContext = worker.packet.inputs.work_context as JSONRecord
    const packetResearch = packetWorkContext.research_findings as JSONRecord[]
    expect(packetResearch).toEqual(continuityResearch)
    expect(packetResearch).toHaveLength(1)
    const packetEntry = packetResearch[0]
    expect(packetEntry.view_id).toMatch(/^research:\d+:0$/)
    expect(packetEntry.selection).toEqual(selectionEntry)
    expect(packetEntry.required).toBe(true)
    expect(packetEntry.state).toBe("resolved")
    expect(packetEntry.finding).toEqual(findingView)
    expect(packetEntry.sources).toEqual([sourceView])
    const packetResearchText = JSON.stringify(packetResearch)
    expect(packetResearchText).not.toContain("finding:g07-b")
    expect(packetResearchText).not.toContain("Revision two")
    expect(packetResearchText).not.toContain("Revised source title")
    expect(packetWorkContext.domain_groups).toEqual([{ domain_id: rootDomain, required_reading_ordinals: [], finding_ids: [packetEntry.view_id] }])
    expect(pins()).toEqual([{ ...binding, consumer_work_id: workID, required: 1 }])
    const subject = await runProcess(["git", "rev-parse", "HEAD"], "", worktree)
    expect(subject.exitCode, subject.stderr).toBe(0)
    const report = routeWorkerReport(lane, worker.packet, subject.stdout.trim())
    const completion = await completeRouteTask(worker, report, runner, binary)
    expect(completion.outcome, JSON.stringify(completion)).toBe("ok")
    const retained = host.retain(worker, report)
    const restartRead = async (task: RouteTaskDispatch, attemptRetained: { parent: JSONRecord; child: JSONRecord }, attemptReport: JSONRecord, reconcileKey: string) => {
      const before = snapshot()
      const counts = host.counts()
      const restarted = await runProcess([process.execPath, "-e", RECOVERY_PROCESS], JSON.stringify({ binary, dbPath, worktree, sessionID: b.sessionID, ...attemptRetained, researchRead: { operation: "research", input: readInput }, request: { operation: "worker_reconcile", input: { work_id: workID, attempt_id: task.packet.attempt_id, task_part_id: task.callID, idempotency_key: reconcileKey } } }), join(import.meta.dir, "../.."))
      expect(restarted.exitCode, restarted.stderr).toBe(0)
      const result = JSON.parse(restarted.stdout) as JSONRecord
      expect(result.envelope.outcome, JSON.stringify(result)).toBe("ok")
      expect(result.envelope.result.worker_recovery.lifecycle_state).toBe("completed")
      expect(result.envelope.result.worker_recovery.dispatch.attempt_id).toBe(task.packet.attempt_id)
      expect(result.envelope.result.worker_recovery.coordinator_session).toBe(b.sessionID)
      expect(result.research.outcome, JSON.stringify(result)).toBe("ok")
      expect(result.research.result.pack.revisions).toEqual(original)
      expect(result.retained.parent.messages.at(-1).parts[0].state.input.prompt).toBe(task.taskArgs.prompt)
      expect(result.retained.parent.messages.at(-1).parts[0].state.output).toBe(task.retainedOutput)
      expect(result.retained.child.messages.at(-1).parts[0].text).toBe(reportText(attemptReport))
      // Reconciliation replays signed evidence, not a new host Task.
      expect(result.core_commands.filter((command: string) => command === "worker-dispatch")).toHaveLength(1)
      expect(result.core_commands.filter((command: string) => command === "worker-complete")).toHaveLength(1)
      // Preparation/replay receipts publish a new read revision, not an effect.
      const { receipts: beforeReceipts, revision: beforeRevision, ...beforeState } = before
      const { receipts: afterReceipts, revision: afterRevision, ...afterState } = snapshot()
      expect(afterRevision[0].revision).toBeGreaterThanOrEqual(beforeRevision[0].revision)
      expect(afterState).toEqual(beforeState)
      // Preparation receipts record reconciliation and replay observations.
      expect(afterReceipts.filter((receipt) => receipt.operation_kind !== "worker_reconcile")).toEqual(beforeReceipts.filter((receipt) => receipt.operation_kind !== "worker_reconcile"))
      for (const receipt of afterReceipts.filter((receipt) => receipt.operation_kind === "worker_reconcile")) {
        expect(JSON.parse(receipt.authorized_scope_snapshot).durable_commit).toBe(false)
        expect(receipt.result_event_ids).toBe("null")
      }
      expect(host.counts()).toEqual(counts)
    }
    await restartRead(worker, retained, report, "g07-retained-reconcile")
    // Retry route: the coordinator rejects the completed result through the
    // real combined rejection surface. With the selection stale the
    // core-derived retry dispatch refuses inside the same action, the
    // rejection stays unwritten, and no window or attempt appears; with the
    // selection resolved again the same route mints the second attempt whose
    // packet carries the same exact selection until explicitly changed.
    const executeTransition = async (input: JSONRecord) => {
      const raw = await work_transition.execute({ request: { operation: "workflow_action", input } }, b)
      const output = typeof raw === "string" ? raw : (raw as JSONRecord).output as string
      return JSON.parse(output.slice(0, output.indexOf("\n") === -1 ? output.length : output.indexOf("\n"))) as JSONRecord
    }
    const workerEpoch = dbValue(dbPath, `SELECT json_extract(payload,'$.attempt_epoch') AS epoch FROM worker_authorizations WHERE work_id='${workID}' AND attempt_id='${worker.packet.attempt_id}'`).epoch
    expect(Number.isInteger(workerEpoch)).toBe(true)
    const rejectAction = (key: string) => ({ work_id: workID, expected_version: workVersion(), action_id: "reject_worker_result", idempotency_key: key, fields: { attempt_id: worker.packet.attempt_id, attempt_epoch: workerEpoch, diagnosis: "The synthetic result missed the selected research obligation.", strategy: "Reproduce the result against the selected research.", predicate_ids: [WORKFLOW_PREDICATE.predicate_id], evidence_refs: ["evidence:g07-retry"], retry: { lane_id: lane.id } } })
    await writePack("research_freshness_set", "g07-stale-retry", { revision: 1, freshness: "stale" })
    const beforeStaleRetry = snapshot()
    const staleRetry = await executeTransition(rejectAction("g07-reject-stale"))
    expect(staleRetry.outcome, JSON.stringify(staleRetry)).toBe("error")
    expect(staleRetry.error.kind).toBe("stale_requires_review")
    expect(staleRetry.error.message).toContain("stale")
    // The refusal rolls the whole combined action back: no rejection event,
    // no derived retry attempt, no armed window, no new host Task.
    expect(snapshot()).toEqual(beforeStaleRetry)
    expect(dispatchWindows().has(b.sessionID)).toBe(false)
    expect(host.counts().taskLaunches).toBe(1)
    await writePack("research_freshness_set", "g07-current-retry-restore", { revision: 1, freshness: "current" })
    const rejection = await executeTransition(rejectAction("g07-reject-retry"))
    expect(rejection.outcome, JSON.stringify(rejection)).toBe("ok")
    expect(rejection.dispatch_state).toBe("awaiting_worker")
    expect(typeof (rejection.result as JSONRecord).retry_attempt_id).toBe("string")
    expect(dispatchWindows().has(b.sessionID)).toBe(true)
    // The native Task consumes the retry window using the returned guidance;
    // no dispatch_worker authorization runs again.
    const retryTaskArgs = nativeTaskSelector(rejection)
    await dispatchWindows().bind(TASK_TOOL_ID, b.sessionID, retryTaskArgs, "g07-retry-task", async () => worktree, worktree)
    const retryPacket = JSON.parse(retryTaskArgs.prompt as string) as JSONRecord
    host.opening(b, retryPacket, "concord-implement")
    expect(retryTaskArgs.subagent_type).toBe("concord-implement")
    expect(retryPacket.attempt_id).toBe((rejection.result as JSONRecord).retry_attempt_id)
    expect(retryPacket.attempt_id).not.toBe(worker.packet.attempt_id)
    expect(dispatchWindows().has(b.sessionID)).toBe(false)
    const retryResearch = ((retryPacket.inputs as JSONRecord).work_context as JSONRecord).research_findings as JSONRecord[]
    // The later packet after the derived retry carries the same exact
    // selection: same view identity, descriptors, and provenance.
    expect(retryResearch).toEqual(packetResearch)
    const retryTask: RouteTaskDispatch = { dispatchResult: rejection, windows: dispatchWindows(), windowWasOpen: true, taskArgs: retryTaskArgs, packet: retryPacket, callID: "g07-retry-task", sessionID: b.sessionID, workerSessionID: b.workerSessionID }
    const retryReport = routeWorkerReport(lane, retryPacket, subject.stdout.trim())
    const retryCompletion = await completeRouteTask(retryTask, retryReport, runner, binary)
    expect(retryCompletion.outcome, JSON.stringify(retryCompletion)).toBe("ok")
    const retryRetained = host.retain(retryTask, retryReport)
    await restartRead(retryTask, retryRetained, retryReport, "g07-retained-reconcile-2")
    await writePack("research_revision_append", "g07-revision-3", { revision: { question: "Which later source owns the route?", method: "source inspection" } })
    await writePack("research_source_record", "g07-source-3", { source: { ...source, title: "Third revision source" } })
    await writePack("research_finding_record", "g07-finding-3", { finding: { ...finding, statement: "Revision three has another finding." }, source_ids: [source.source_id] })
    expect((await read()).pack.current_revision).toBe(3)
    expect((await read()).pack.revisions).toEqual(original)
    await restartRead(retryTask, retryRetained, retryReport, "g07-retained-reconcile-2")
    // Beside the newer head the view still resolves the exact selected
    // revision-one entry: same view identity and descriptors, with no
    // revision-three finding or source content.
    const laterContext = await pinnedContextOf()
    expect(laterContext.research_findings).toEqual(continuityResearch)
    const laterResearchText = JSON.stringify(laterContext.research_findings)
    expect(laterResearchText).not.toContain("Revision three")
    expect(laterResearchText).not.toContain("Third revision source")
    await ok("concord_work_transition", "lifecycle", { work_id: ownerID, expected_version: workVersion(ownerID), target: "cancelled", reason: "The synthetic research author finished.", idempotency_key: "g07-owner-finished", evidence: [{ kind: "verification", authority: "synthetic-test", locator_kind: "research_pack", locator: packID }] }, a)
    const retire = async (key: string) => {
      const beforePins = pins()
      const beforePackVersion = packVersion()
      const result = await ok("concord_work_define", "research_retire", { product_id: PRODUCT_ID, candidates: [{ pack_id: packID, expected_version: beforePackVersion }], dry_run: false, idempotency_key: key }, a)
      expect(result.candidates).toHaveLength(1)
      expect(result.candidates[0]).toMatchObject({ pack_id: packID, classification: "protected", protection_reason: "active_pin" })
      expect(packVersion()).toBe(beforePackVersion)
      expect(pins()).toEqual(beforePins)
      return result
    }
    await retire("g07-retire-required")
    await writePack("research_freshness_set", "g07-stale-pin", { revision: 1, freshness: "stale" })
    const beforeRefusal = snapshot()
    const refused = await invoke("concord_work_transition", { operation: "workflow_action", input: bindAction("g07-stale-required", true, 1) }, b)
    expect(refused.outcome, JSON.stringify(refused)).toBe("error")
    expect(refused.error.kind).toBe("stale_requires_review")
    expect(refused.error.effect_state).toBe("none")
    expect(snapshot()).toEqual(beforeRefusal)
    await ok("concord_work_transition", "workflow_action", bindAction("g07-optional-stale", false, 1), b)
    expect(pins()).toEqual([{ ...binding, consumer_work_id: workID, required: 0 }])
    // The optional unresolved selection stays visible as typed stale state
    // carrying no finding or source body; this asserts its view visibility,
    // not an optional dispatch result.
    const optionalStaleContext = await pinnedContextOf()
    const optionalStaleEntry = (optionalStaleContext.research_findings as JSONRecord[])[0]
    expect(optionalStaleEntry.required).toBe(false)
    expect(optionalStaleEntry.state).toBe("stale")
    expect(optionalStaleEntry.finding).toBeUndefined()
    expect(optionalStaleEntry.sources).toBeUndefined()
    const staleRead = await read()
    expect(staleRead.pack.revisions[0].freshness).toBe("stale")
    expect(staleRead.pack.revisions[0].findings).toEqual(original[0].findings)
    expect(staleRead.pack.revisions[0].sources).toEqual(original[0].sources)
    await retire("g07-retire-optional")
    expect(host.counts()).toEqual({ coordinatorSessions: 2, taskLaunches: 2, sessionCreates: 0 })
    const authored = calls.find((call) => call.argv[1] === "invoke" && call.input.operation === "research_finding_record")!
    expect(authored.input.call_envelope.session_ref).toBe(a.sessionID)
    const relied = calls.find((call) => call.argv[1] === "invoke" && call.input.input.idempotency_key === "g07-required-pin")!
    expect(relied.input.call_envelope.session_ref).toBe(b.sessionID)
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    if (previousConfig === undefined) delete process.env.OPENCODE_CONFIG
    else process.env.OPENCODE_CONFIG = previousConfig
  }
}, 300_000)

const RECOVERY_PROCESS = `
import { createInterface } from "node:readline";
import { Database } from "bun:sqlite";
import { configureConcordAdapter, invokeConcordOperation, work_start, work_transition, work_trace } from "./adapter/opencode/concord.ts";
import { dispatchLaneWorker } from "./adapter/opencode/lane_dispatch.ts";
import { completeDispatchedWorker } from "./adapter/opencode/lane_completion.ts";
import { configureCoreBinary } from "./adapter/opencode/dispatch.ts";
import { hostControlPlane, MOVE_SESSION_ROUTE, SESSION_ROUTE, SESSION_MESSAGES_ROUTE, SESSION_LIST_ROUTE } from "./adapter/opencode/move-session.ts";
import { dispatchWindows, TASK_TOOL_ID } from "./adapter/opencode/dispatch-window.ts";
import { clearTurnMoveBoundary } from "./adapter/opencode/turn-move-boundary.ts";
const lines = createInterface({ input: process.stdin })[Symbol.asyncIterator]();
const fixture = JSON.parse((await lines.next()).value);
let heldKey;
let dispatchTransport;
let abortedAt = null;
let abortedPhase = null;
let preparationInvoked = false;
let evidenceAcknowledged = false;
const coreCommands = [];
const observedSignals = new Set();
const observeSignal = (signal) => {
  if (observedSignals.has(signal)) return;
  observedSignals.add(signal);
  signal.addEventListener("abort", () => { abortedAt ??= performance.now(); }, { once: true });
};
const delay = (milliseconds, signal, phase) => new Promise((resolve, reject) => {
  const abort = () => { abortedAt ??= performance.now(); abortedPhase ??= phase; clearTimeout(timer); reject(signal.reason); };
  const timer = setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, milliseconds);
  signal.addEventListener("abort", abort, { once: true });
  if (signal.aborted) abort();
});
configureCoreBinary(fixture.binary);
if (dispatchWindows().inFlightAttempt(fixture.sessionID)) throw new Error("restart fixture must have no retained in-memory dispatch");
hostControlPlane().bind({
  get: async ({ url, path, signal }) => {
    observeSignal(signal);
    if (fixture.coordinator && url === SESSION_LIST_ROUTE) return { data: [fixture.parent.info], response: new Response("[]", { status: 200 }) };
    if (fixture.hostReadDelayMs && url === SESSION_MESSAGES_ROUTE && path.id === fixture.sessionID) await delay(fixture.hostReadDelayMs, signal, "host_proof_read");
    const session = path.id === fixture.sessionID ? fixture.parent : fixture.child;
    if (url === SESSION_ROUTE) return { data: session.info, response: new Response(null, { status: 200 }) };
    if (url === SESSION_MESSAGES_ROUTE) return { data: session.messages, response: new Response("[]", { status: 200 }) };
    throw new Error("unexpected host route");
  },
  patch: async ({ body }) => {
    if (!fixture.coordinator) throw new Error("recovery must not change host metadata");
    Object.assign(fixture.parent.info, body);
    return { response: new Response(null, { status: 200 }) };
  },
  post: async ({ url, body }) => {
    if (!fixture.coordinator || url !== MOVE_SESSION_ROUTE) throw new Error("recovery must not spawn a host session");
    fixture.parent.info.directory = body.destination.directory;
    return { response: new Response(null, { status: 204 }) };
  },
});
const recoveryRunner = { async run(argv, input, signal, options) {
    observeSignal(signal);
    const command = argv[1];
    coreCommands.push(command);
    if (fixture.coordinator && command === "invoke" && JSON.parse(input).input?.action_id === "dispatch_worker") dispatchTransport = { argv, input };
    if (fixture.coordinator && command === "session") return { exitCode: 0, stdout: JSON.stringify([fixture.parent.info]), stderr: "" };
    if (fixture.coordinator && heldKey !== undefined && command === "invoke" && JSON.parse(input).input?.idempotency_key === heldKey) {
      console.log(JSON.stringify({ held: { argv, input }, pid: process.pid }));
      await new Promise(() => {});
    }
    if (fixture.coordinator && fixture.dropCompletion && command === "worker-complete") return { exitCode: 1, stdout: "", stderr: "C12 lost terminal ingestion before original transport" };
    let phase;
    if (command === "project-resolve") phase = evidenceAcknowledged ? "receipt_read" : preparationInvoked ? "reconciliation_read" : "context_read";
    else if (command === "invoke") {
      const call = JSON.parse(input);
      phase = call.tool === "concord_work_browse" ? "reconciliation_read" : evidenceAcknowledged ? "receipt_read" : "mutation_transport";
    } else if (command === "worker-dispatch" || command === "worker-complete") phase = "mutation_transport";
    else if (fixture.coordinator) phase = "coordinator_transport";
    else throw new Error("unexpected recovery core command: " + command);
    if (signal.aborted) throw signal.reason;
    if (fixture.contextReadDelayMs && phase === "context_read") await delay(fixture.contextReadDelayMs, signal, phase);
    if (command === "invoke" && phase === "mutation_transport") preparationInvoked = true;
    const child = Bun.spawn([fixture.binary, ...argv.slice(1)], { cwd: options?.cwd, env: { ...process.env, CONCORD_DB_PATH: fixture.dbPath }, stdin: "pipe", stdout: "pipe", stderr: "pipe" });
    const abort = () => { abortedAt ??= performance.now(); abortedPhase ??= phase; child.kill(); };
    signal.addEventListener("abort", abort, { once: true });
    try {
      if (signal.aborted) { abort(); throw signal.reason; }
      await child.stdin.write(input); await child.stdin.end();
      const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited]);
      if (signal.aborted) throw signal.reason;
      if (command === "worker-complete" && exitCode === 0) evidenceAcknowledged = true;
      return { stdout, stderr, exitCode };
    } finally {
      signal.removeEventListener("abort", abort);
    }
  } };
configureConcordAdapter({ credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } }, runner: recoveryRunner });
const started = performance.now();
const context = { sessionID: fixture.sessionID, messageID: "recovery-message", agent: "concord-implement", directory: fixture.worktree, worktree: fixture.worktree, abort: new AbortController().signal, metadata() {}, ask: async () => {} };
if (fixture.coordinator) {
  console.log(JSON.stringify({ ready: true, pid: process.pid }));
  for (;;) {
    const next = await lines.next();
    if (next.done) break;
    const request = JSON.parse(next.value);
    if (request.host) Object.assign(fixture, request.host);
    context.directory = request.directory ?? fixture.parent.info.directory;
    context.worktree = context.directory;
    context.messageID = request.messageID ?? "c12-coordinator-message";
    let envelope;
    if (request.kind === "snapshot") {
      const db = new Database(fixture.dbPath, { readonly: true });
      try {
        envelope = { outcome: "ok", facts: {
          claims: db.query("SELECT e.* FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE c.work_id=?").all(fixture.workID),
          attempts: db.query("SELECT attempt_id,lifecycle_state FROM worker_attempts WHERE work_id=? ORDER BY attempt_id").all(fixture.workID),
          dispositions: db.query("SELECT attempt_id,action_id FROM worker_attempt_dispositions WHERE work_id=? ORDER BY seq").all(fixture.workID),
          checkpoint: db.query("SELECT checkpoint_id,attempt_epoch FROM workflow_context_checkpoints WHERE work_id=? ORDER BY checkpoint_sequence DESC LIMIT 1").get(fixture.workID),
        } };
      } finally { db.close(); }
    } else if (request.kind === "next_turn") {
      // Controlled host input: the same event sink chat.message uses.
      clearTurnMoveBoundary(fixture.sessionID);
      envelope = { outcome: "ok" };
    } else if (request.kind === "start") envelope = JSON.parse((await work_start.execute({ work_id: fixture.workID }, context)).output.split("\\n")[0]);
    else if (request.kind === "dispatch") envelope = await dispatchLaneWorker(request.input, { context, invoke: invokeConcordOperation, credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } }, windows: dispatchWindows() });
    else if (request.kind === "bind") {
      await dispatchWindows().bind(TASK_TOOL_ID, fixture.sessionID, request.taskArgs, request.callID, async () => fixture.worktree, fixture.worktree);
      envelope = { outcome: "ok", taskArgs: request.taskArgs };
    } else if (request.kind === "ingest") {
      const output = { title: "task", output: request.output, metadata: {} };
      await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: fixture.sessionID, callID: request.callID, args: request.taskArgs }, output, { windows: dispatchWindows(), runner: recoveryRunner, concordBinary: fixture.binary, credentials: { async getPrivateKey() { return new Uint8Array(32).fill(7) } } });
      envelope = { output: output.output };
    } else if (request.kind === "invoke" || request.kind === "hold") {
      if (request.kind === "hold") heldKey = request.args.input.idempotency_key;
      envelope = await invokeConcordOperation(request.tool ?? "concord_work_transition", request.args, context);
    } else envelope = JSON.parse((await work_transition.execute({ request: request.args }, context)).output.split("\\n")[0]);
    console.log(JSON.stringify({ envelope, pid: process.pid, core_commands: coreCommands, ...(request.kind === "dispatch" ? { dispatch_transport: dispatchTransport } : {}) }));
  }
} else {
const result = await work_transition.execute({ request: fixture.request }, context);
const research = fixture.researchRead ? await work_trace.execute({ request: fixture.researchRead }, context) : null;
console.log(JSON.stringify({ envelope: JSON.parse(result.output), elapsed_ms: performance.now() - started, abort_elapsed_ms: abortedAt === null ? null : abortedAt - started, aborted_phase: abortedPhase, signal_count: observedSignals.size, core_commands: coreCommands, ...(research ? { research: JSON.parse(research.output), retained: { parent: fixture.parent, child: fixture.child } } : {}) }));
}
`

// Each actor owns its adapter memory and real process identity. Only host
// readback is controlled; the core, Git, authenticated calls, and signatures
// are real. The suite's external owner contains every descendant.
async function c12Coordinator(fixture: RouteFixture, sessionID: string, retained?: JSONRecord, childEnv: Record<string, string> = {}) {
  const child = Bun.spawn([process.execPath, "-e", RECOVERY_PROCESS], {
    cwd: join(import.meta.dir, "../.."), env: { ...process.env, ...childEnv }, stdin: "pipe", stdout: "pipe", stderr: "pipe",
  })
  const stderr = new Response(child.stderr).text()
  const reader = createInterface({ input: Readable.fromWeb(child.stdout as any) })
  const lines = reader[Symbol.asyncIterator]()
  const read = async () => {
    let timer: ReturnType<typeof setTimeout> | undefined
    try {
      const next = await Promise.race([lines.next(), new Promise<never>((_, reject) => {
        timer = setTimeout(() => { child.kill("SIGKILL"); reject(new Error(`C12 actor ${sessionID} exceeded its 40s response budget`)) }, 40_000)
      })])
      if (next.done) throw new Error(`C12 actor ${sessionID} exited: ${await stderr}`)
      return JSON.parse(next.value) as JSONRecord
    } finally { if (timer) clearTimeout(timer) }
  }
  const stop = async () => {
    child.kill("SIGKILL")
    await child.exited
    reader.close()
    await stderr
  }
  try {
    await child.stdin.write(JSON.stringify({ ...fixture, coordinator: true, sessionID, dropCompletion: true,
      parent: retained?.parent ?? { info: { id: sessionID, directory: fixture.worktree, metadata: {} }, messages: [] }, child: retained?.child,
    }) + "\n")
    expect(await read()).toEqual({ ready: true, pid: child.pid })
  } catch (error) { await stop(); throw error }
  return { pid: child.pid, stop, async call(request: JSONRecord) {
    await child.stdin.write(JSON.stringify(request) + "\n")
    return read()
  } }
}

for (const started of [false, true]) {
  routeDeclaration(`C12 ${started ? "05" : "01"} composes real coordinator death same-session landing recovery acceptance and delayed progress`, async () => {
    const root = await fixtureTempRoot(`dispatch-c12-${started ? "started" : "unknown"}`)
    const previousConfig = process.env.OPENCODE_CONFIG
    const actors: Awaited<ReturnType<typeof c12Coordinator>>[] = []
    try {
      const fixture = await bootRouteFixture(root)
      const { binary, dbPath, configPath, workID, worktree, repo, lane } = fixture
      process.env.OPENCODE_CONFIG = configPath
      // The real session-prepare route needs installed identities and a host
      // config probe. Keep both synthetic and private to this owned fixture.
      const home = join(root, "home"), bin = join(root, "bin")
      await mkdir(join(home, ".config/opencode/agents"), { recursive: true })
      await mkdir(bin)
      for (const id of ["research", "implement", "design", "review", "verify"]) await Bun.write(join(home, ".config/opencode/agents", `concord-${id}.md`), `concord-${id} synthetic definition\n`)
      const probe = join(bin, "opencode")
      await Bun.write(probe, `#!/bin/sh\necho '{"agent":{"concord-implement":{"mode":"all","disable":false}}}'\n`)
      await chmod(probe, 0o755)
      const childEnv = { HOME: home, PATH: `${bin}${delimiter}${process.env.PATH ?? ""}`, OPENCODE_BIN: probe }
      const aID = "c12-coordinator-a", bID = "c12-coordinator-b"
      const calls: Array<{ argv: string[]; input: JSONRecord }> = []
      const runner = realStoreRunner(binary, dbPath, calls, worktree)
      configureConcordAdapter({ runner })
      const host = bindRouteSessions(worktree, [aID, bID])
      const a = await c12Coordinator(fixture, aID, undefined, childEnv); actors.push(a)
      const b = await c12Coordinator(fixture, bID, undefined, childEnv); actors.push(b)
      const version = () => dbValue(dbPath, "SELECT version FROM work_items WHERE id=?", workID).version
      const sql = (query: string) => dbRows(dbPath, query, workID)
      const claims = () => sql("SELECT e.* FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE c.work_id=?")
      const occupants = () => dbRows(dbPath, "SELECT * FROM worktree_occupancy ORDER BY session_ref")
      const snapshot = () => ({
        work: sql("SELECT * FROM work_items WHERE id=?"), workflow: sql("SELECT * FROM workflow_instances WHERE work_id=?"),
        checkpoints: sql("SELECT * FROM workflow_context_checkpoints WHERE work_id=? ORDER BY checkpoint_sequence"), epochs: sql("SELECT * FROM workflow_step_epochs WHERE work_id=? ORDER BY step_id"),
        jobs: sql("SELECT * FROM worker_job_revisions WHERE work_id=? ORDER BY job_id,revision"), occupants: occupants(),
        attempts: sql("SELECT * FROM worker_attempts WHERE work_id=? ORDER BY attempt_id"), authorizations: sql("SELECT * FROM worker_authorizations WHERE work_id=? ORDER BY attempt_id"),
        dispositions: sql("SELECT * FROM worker_attempt_dispositions WHERE work_id=? ORDER BY seq"), claims: claims(),
      })
      const ok = (response: JSONRecord) => { expect(response.envelope.outcome, JSON.stringify(response)).toBe("ok"); return response.envelope }
      const invoke = async (tool: string, args: any, context: any) => (await a.call({ kind: "invoke", tool, args, directory: context.directory })).envelope
      const context = contextFor(worktree, aID)
      const action = (actor: typeof a, name: string, key: string, fields: JSONRecord) => actor.call({ kind: "invoke", args: {
        operation: "workflow_action", input: { work_id: workID, expected_version: version(), action_id: name, idempotency_key: key, fields },
      } })
      const checkpoint = (id: string) => ({ checkpoint_id: id, active_unit: "synthetic route", diagnosis: id,
        hypothesis: "The original effect must settle before retry.", strategy: "Recover the exact original attempt.",
        touched_refs: ["oraclekit/oracle.go"], evidence_refs: ["evidence:c12-fixture"], pending_decisions: [], pending_questions: [],
      })
      const starts = join(root, "native-starts.jsonl")
      const marker = join(root, "external-effect.jsonl")
      const markerID = `c12-effect-${crypto.randomUUID()}`
      const count = async (path: string) => await Bun.file(path).exists() ? (await Bun.file(path).text()).trim().split("\n").length : 0
      const spawnEffect = async (packet: JSONRecord, workerID: string, coordinatorID: string) => {
        const subject = (await runProcess(["git", "rev-parse", "HEAD"], "", worktree)).stdout.trim()
        const report = routeWorkerReport(lane, packet, subject)
        // A real harness-owned child, not the assertion process, emits the
        // unique effect and its original report. This is not OpenCode startup.
        const emitted = await runProcess([process.execPath, "-e", `
          import { appendFile } from "node:fs/promises";
          const f = await new Response(Bun.stdin).json();
          await appendFile(f.starts, JSON.stringify({ attempt_id: f.attemptID, pid: process.pid }) + "\\n");
          await appendFile(f.marker, JSON.stringify({ marker: f.markerID, attempt_id: f.attemptID }) + "\\n");
          console.log(JSON.stringify(f.report));
        `], JSON.stringify({ starts, marker, markerID, attemptID: packet.attempt_id, report }), worktree)
        expect(emitted.exitCode, emitted.stderr).toBe(0)
        const original = JSON.parse(emitted.stdout)
        const workerContext = { ...contextFor(worktree, coordinatorID), workerSessionID: workerID }
        host.opening(workerContext, packet, "concord-implement")
        return original
      }
      const resumed = await Promise.all([a.call({ kind: "start" }), b.call({ kind: "start" })])
      resumed.forEach(ok)
      expect(claims()).toHaveLength(1)
      const claim = claims()
      const nativeBefore = await runProcess(["git", "worktree", "list", "--porcelain"], "", repo)
      ok(await b.call({ kind: "transition", args: { operation: "session_vacate", input: { idempotency_key: "c12-b-vacate" } } }))
      expect(occupants().map(row => row.session_ref)).toEqual([aID])
      const originalOccupancy = occupants()[0]
      expect(originalOccupancy.host_pid).toBe(a.pid)
      expect(originalOccupancy.host_pid_start).toBeGreaterThan(0)
      await driveWorkflowToContract(workID, invoke, context)
      const dispatchInput = { work_id: workID, expected_version: version(), idempotency_key: "c12-original-dispatch", lane_id: lane.id }
      const originalDispatch = await a.call({ kind: "dispatch", input: dispatchInput })
      const dispatch = ok(originalDispatch)
      const taskArgs = ok(await a.call({ kind: "bind", taskArgs: nativeTaskSelector(dispatch), callID: "c12-original-task" })).taskArgs
      const packet = JSON.parse(taskArgs.prompt)
      const attemptID = packet.attempt_id
      const epoch = dbValue(dbPath, "SELECT json_extract(payload,'$.attempt_epoch') AS n FROM worker_authorizations WHERE work_id=? AND attempt_id=?", workID, attemptID).n
      await Bun.write(join(root, "original-authorization.json"), JSON.stringify({ attempt_id: attemptID, attempt_epoch: epoch, packet, claim, transport: originalDispatch.dispatch_transport }))
      let retained: JSONRecord | undefined
      const retainedPath = join(root, "original-host-task.json")
      if (started) {
        const original = await spawnEffect(packet, "c12-original-worker", aID)
        const task: RouteTaskDispatch = { dispatchResult: dispatch, windows: new DispatchWindows(), windowWasOpen: true, taskArgs, packet, callID: "c12-original-task", sessionID: aID, workerSessionID: "c12-original-worker", retainedOutput: taskResult(original, "c12-original-worker") }
        retained = host.retain(task, original)
        await Bun.write(retainedPath, JSON.stringify(retained))
        const ingestion = await a.call({ kind: "ingest", taskArgs, callID: task.callID, output: task.retainedOutput, host: retained })
        expect(ingestion.envelope.output).toContain("C12 lost terminal ingestion before original transport")
        expect(workerEffects(dbPath, workID).map(row => row.kind)).toEqual(["worker.dispatched"])
        expect(JSON.parse(await Bun.file(retainedPath).text())).toEqual(retained)
      }
      const delayed = { operation: "workflow_action", input: { work_id: workID, expected_version: version(), action_id: "checkpoint_context", idempotency_key: "c12-original-delayed-progress", fields: checkpoint("checkpoint:c12-delayed-a") } }
      const held = await a.call({ kind: "hold", args: delayed })
      await Bun.write(join(root, "original-progress-envelope.json"), JSON.stringify({ ...held, attempt_id: attemptID, attempt_epoch: epoch }))
      expect(JSON.parse(held.held.input).call_envelope.session_ref).toBe(aID)
      expect(JSON.parse(held.held.input).input).toEqual(delayed.input)
      await Bun.write(join(worktree, "c12-dirty.txt"), "preserve this dirty file across coordinator death\n")
      await a.stop()
      if (started) retained = JSON.parse(await Bun.file(retainedPath).text())
      const a2 = await c12Coordinator(fixture, aID, retained, childEnv); actors.push(a2)
      expect(a2.pid).not.toBe(a.pid)
      ok(await a2.call({ kind: "start" }))
      expect(dbValue(dbPath, "SELECT host_pid,has_process_identity FROM worktree_occupancy WHERE session_ref=?", aID)).toEqual({ host_pid: a2.pid, has_process_identity: 1 })
      expect(dbValue(dbPath, "SELECT host_pid_start FROM worktree_occupancy WHERE session_ref=?", aID).host_pid_start).toBeGreaterThan(originalOccupancy.host_pid_start)
      expect(await Bun.file(join(worktree, "c12-dirty.txt")).text()).toBe("preserve this dirty file across coordinator death\n")
      // Preservation was observed across the real restart. Remove only this
      // fixture-owned file so the clean-subject gate cannot mask attempt admission.
      await unlink(join(worktree, "c12-dirty.txt"))
      expect((await runProcess(["git", "status", "--porcelain"], "", worktree)).stdout).toBe("")
      expect(claims()).toEqual(claim)
      expect((await runProcess(["git", "worktree", "list", "--porcelain"], "", repo)).stdout).toBe(nativeBefore.stdout)
      const unresolved = snapshot()
      expect(dbValue(dbPath, "SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?", attemptID).lifecycle_state).toBe(started ? "dispatched" : "in_flight")
      // The byte-identical core envelope replays its standing authorization;
      // it does not arm a Task in the replacement adapter's empty memory.
      const originalTransport = originalDispatch.dispatch_transport
      const exactReplay = await runner.run(originalTransport.argv, originalTransport.input, AbortSignal.timeout(10_000))
      const replayedAuthorization = JSON.parse(exactReplay.stdout)
      expect(replayedAuthorization.outcome, exactReplay.stdout).toBe("ok")
      expect(replayedAuthorization.replayed).toBe(true)
      expect(snapshot()).toEqual(unresolved)
      const replay = await a2.call({ kind: "dispatch", input: dispatchInput })
      expect(replay.envelope.outcome, JSON.stringify(replay)).toBe("error")
      expect(snapshot()).toEqual(unresolved)
      const replacement = await a2.call({ kind: "dispatch", input: { ...dispatchInput, expected_version: version(), idempotency_key: "c12-replacement-dispatch" } })
      console.log(JSON.stringify({ case: started ? "C12-05" : "C12-01", boundary: "same-session-replacement", original_attempt: attemptID,
        original_state: dbValue(dbPath, "SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?", attemptID),
        outcome: replacement.envelope.outcome, error: replacement.envelope.error, authorizations: sql("SELECT attempt_id FROM worker_authorizations WHERE work_id=?"), native_starts: await count(starts) }))
      // A restarted coordinator cannot replace its unresolved assignment.
      expect(replacement.envelope.outcome, `same-session restart guard admitted ${replacement.envelope.dispatch_state}`).toBe("error")
      expect(replacement.envelope.error.details?.core_error?.kind, JSON.stringify(replacement.envelope.error)).toBe("operation_conflict")
      expect(snapshot()).toEqual(unresolved)
      expect(await count(starts)).toBe(started ? 1 : 0)
      if (started) {
        const recovery = { binary, dbPath, worktree, sessionID: aID, ...retained, request: { operation: "worker_reconcile", input: { work_id: workID, attempt_id: attemptID, task_part_id: "c12-original-task", idempotency_key: "c12-recover-original" } } }
        for (const [name, originalHost] of [["missing", { ...retained, parent: { ...retained!.parent, messages: [] } }], ["foreign", { ...retained, child: { ...retained!.child, info: { ...retained!.child.info, parentID: bID } } }]] as const) {
          const refused = await runProcess([process.execPath, "-e", RECOVERY_PROCESS], JSON.stringify({ ...recovery, ...originalHost, request: { ...recovery.request, input: { ...recovery.request.input, idempotency_key: `c12-recovery-${name}` } } }), join(import.meta.dir, "../.."))
          expect(refused.exitCode, refused.stderr).toBe(0)
          expect(JSON.parse(refused.stdout).envelope.outcome).toBe("error")
          expect(snapshot()).toEqual(unresolved)
          expect(await count(starts)).toBe(1)
          expect(await count(marker)).toBe(1)
        }
        for (let repeat = 0; repeat < 2; repeat++) {
          const recovered = await runProcess([process.execPath, "-e", RECOVERY_PROCESS], JSON.stringify(recovery), join(import.meta.dir, "../.."))
          expect(recovered.exitCode, recovered.stderr).toBe(0)
          const receipt = ok(JSON.parse(recovered.stdout))
          expect(receipt.result.worker_recovery.dispatch.attempt_id).toBe(attemptID)
          expect(receipt.result.worker_recovery.lifecycle_state).toBe("completed")
        }
      } else {
        const abandon = { operation: "worker_abandon", input: { work_id: workID, attempt_id: attemptID, lane_id: lane.id, detail: "The harness killed its original coordinator before native startup.", idempotency_key: "c12-live-abandon" } }
        // B observes A2 as a foreign live occupant. The own-row adapter route
        // would automatically vacate/retry/re-land, hiding the raw refusal.
        const refused = await b.call({ kind: "transition", args: abandon })
        expect(refused.envelope.outcome, JSON.stringify(refused)).toBe("error")
        expect(refused.envelope.error.message).toContain(`session ${aID} still holds the worker attempt worktree`)
        expect(snapshot()).toEqual(unresolved)
        ok(await a2.call({ kind: "transition", args: { operation: "session_vacate", input: { idempotency_key: "c12-a2-vacate" } } }))
        expect(dbValue(dbPath, "SELECT destination_directory,landed FROM session_vacate_requests WHERE session_ref=?", aID)).toEqual({ destination_directory: repo, landed: 1 })
        expect(occupants()).toHaveLength(0)
        const closed = await b.call({ kind: "transition", directory: repo, args: abandon })
        expect(closed.envelope.error?.message, JSON.stringify(closed)).toBe("the worker attempt was closed, but the durable replay receipt was not confirmed")
        expect(workerEffects(dbPath, workID).map(row => row.kind)).toEqual(["worker.failed"])
        expect(JSON.parse(workerEffects(dbPath, workID)[0].payload).failure_attribution.observation).toMatchObject({ cause: "unknown", basis: "unknown" })
        ok(await b.call({ kind: "start", directory: worktree }))
        ok(await b.call({ kind: "invoke", args: abandon }))
      }
      // Both branches must finish acceptance and delayed-progress checks;
      // refusal-only prefixes are never successful composite evidence.
      ok(await b.call({ kind: "start", directory: worktree }))
      let acceptedAttempt = attemptID, acceptedEpoch = epoch
      if (!started) {
        const successorInput = { ...dispatchInput, expected_version: version(), idempotency_key: "c12-successor-dispatch" }
        const sameTurn = await b.call({ kind: "dispatch", input: successorInput })
        expect(sameTurn.envelope.error?.details?.boundary, JSON.stringify(sameTurn)).toBe("turn_move")
        ok(await b.call({ kind: "next_turn", messageID: "c12-next-operator-turn" }))
        const successor = ok(await b.call({ kind: "dispatch", input: successorInput }))
        const successorArgs = ok(await b.call({ kind: "bind", taskArgs: nativeTaskSelector(successor), callID: "c12-successor-task" })).taskArgs
        const successorPacket = JSON.parse(successorArgs.prompt)
        acceptedAttempt = successorPacket.attempt_id
        acceptedEpoch = dbValue(dbPath, "SELECT json_extract(payload,'$.attempt_epoch') AS n FROM worker_authorizations WHERE work_id=? AND attempt_id=?", workID, acceptedAttempt).n
        expect(acceptedEpoch).toBeGreaterThan(epoch)
        const report = await spawnEffect(successorPacket, "c12-successor-worker", bID)
        const task: RouteTaskDispatch = { dispatchResult: successor, windows: new DispatchWindows(), windowWasOpen: true, taskArgs: successorArgs, packet: successorPacket, callID: "c12-successor-task", sessionID: bID, workerSessionID: "c12-successor-worker", retainedOutput: taskResult(report, "c12-successor-worker") }
        // U has no lost response. Its original Task completes through the
        // ordinary signed hook; only T needs abandonment in this branch.
        const successorHost = host.retain(task, report)
        const completed = await b.call({ kind: "ingest", taskArgs: successorArgs, callID: task.callID, output: task.retainedOutput, host: { ...successorHost, dropCompletion: false } })
        expect(JSON.parse(completed.envelope.output.split("<concord_attempt>\n")[1].split("\n</concord_attempt>")[0]).outcome, JSON.stringify(completed)).toBe("ok")
      }
      if (!started) {
        // After acceptance, the disposed-result gate precedes epoch checking.
        // Exercise the epoch fence independently while U is still decidable.
        const beforeEpoch = snapshot()
        const directEpoch = await action(b, "accept_worker_result", "c12-direct-old-epoch", { attempt_id: acceptedAttempt, attempt_epoch: epoch })
        expect(directEpoch.envelope.outcome, JSON.stringify(directEpoch)).toBe("error")
        expect(directEpoch.envelope.error.message).toBe("worker attempt epoch does not match the latest workflow action start")
        expect(snapshot()).toEqual(beforeEpoch)
      }
      ok(await action(b, "accept_worker_result", "c12-b-accept", { attempt_id: acceptedAttempt, attempt_epoch: acceptedEpoch }))
      ok(await action(b, "checkpoint_context", "c12-b-checkpoint", checkpoint("checkpoint:c12-current-b")))
      const accepted = snapshot()
      // Context checkpoints bind the durable-operation epoch, not the lane's
      // dispatch-start epoch. Preserve the actual checkpoint pair on restart.
      const acceptedCheckpoint = dbValue(dbPath, "SELECT checkpoint_id,attempt_epoch FROM workflow_context_checkpoints WHERE work_id=? ORDER BY checkpoint_sequence DESC LIMIT 1", workID)
      expect(acceptedCheckpoint).toEqual({ checkpoint_id: "checkpoint:c12-current-b", attempt_epoch: dbValue(dbPath, "SELECT COALESCE(MAX(attempt_epoch),1) AS n FROM durable_operations WHERE work_id=?", workID).n })
      for (let replay = 0; replay < 2; replay++) {
        const late = await runner.run(held.held.argv, held.held.input, AbortSignal.timeout(10_000))
        const envelope = JSON.parse(late.stdout)
        expect(envelope.outcome).toBe("error")
        expect(envelope.error.kind).toBe("version_conflict")
        expect(snapshot()).toEqual(accepted)
      }
      expect(await count(starts)).toBe(1)
      expect(await count(marker)).toBe(1)
      expect(JSON.parse((await Bun.file(marker).text()).trim())).toEqual({ marker: markerID, attempt_id: acceptedAttempt })
      if (!started) {
        ok(await a2.call({ kind: "start", directory: worktree }))
        const beforeOldEpoch = snapshot()
        const stale = await action(a2, "accept_worker_result", "c12-a-old-epoch", { attempt_id: acceptedAttempt, attempt_epoch: epoch })
        expect(stale.envelope.outcome, JSON.stringify(stale)).toBe("error")
        expect(stale.envelope.error.message).toBe("result acceptance requires an undispositioned completed worker result")
        expect(snapshot()).toEqual(beforeOldEpoch)
      }
      const freshReader = await c12Coordinator(fixture, bID, undefined, childEnv); actors.push(freshReader)
      const read = await freshReader.call({ kind: "invoke", tool: "concord_work_trace", args: { operation: "continuity", input: { work_id: workID } } })
      expect(JSON.stringify(ok(read))).toContain("checkpoint:c12-current-b")
      const finalFacts = ok(await freshReader.call({ kind: "snapshot" })).facts
      expect(finalFacts.claims).toEqual(claim)
      expect(finalFacts.attempts).toEqual(sql("SELECT attempt_id,lifecycle_state FROM worker_attempts WHERE work_id=? ORDER BY attempt_id"))
      expect(finalFacts.dispositions).toEqual(sql("SELECT attempt_id,action_id FROM worker_attempt_dispositions WHERE work_id=? ORDER BY seq"))
      expect(finalFacts.checkpoint).toEqual(acceptedCheckpoint)
      expect(sql("SELECT attempt_id,action_id FROM worker_attempt_dispositions WHERE work_id=? AND action_id='accept_worker_result'")).toEqual([{ attempt_id: acceptedAttempt, action_id: "accept_worker_result" }])
      expect(claims()).toEqual(claim)
    } finally {
      await Promise.all(actors.map(actor => actor.stop()))
      configureConcordAdapter({ reset: true })
      hostControlPlane().bind(undefined)
      if (previousConfig === undefined) delete process.env.OPENCODE_CONFIG
      else process.env.OPENCODE_CONFIG = previousConfig
    }
  }, 300_000)
}

for (const blockedVerb of ["worker-dispatch", "worker-complete"]) {
  routeDeclaration(`recovers a lost ${blockedVerb} acknowledgement with a pinned reader after adapter process restart`, async () => {
    const root = await fixtureTempRoot("dispatch-recovery")
    const previousConfig = process.env.OPENCODE_CONFIG
    let reader: Database | undefined
    try {
      const { binary, dbPath, configPath, workID, worktree, lane } = await bootRouteFixture(root)
      process.env.OPENCODE_CONFIG = configPath
      const context = contextFor(worktree)
      let metadata: JSONRecord = {}
      let packet: JSONRecord | null = null
      hostControlPlane().bind({
        get: async ({ url, path }) => {
          if (url === SESSION_MESSAGES_ROUTE) return { data: JSON.parse(exportedSession(packet)).messages, response: new Response("[]", { status: 200 }) }
          return { data: { id: path?.id, directory: worktree, metadata, ...(path?.id === "worker-session" ? { parentID: SESSION_ID } : {}) }, response: new Response(null, { status: 200 }) }
        },
        patch: async () => { metadata = { [MANAGED_TASK_SCOPE_KEY]: "managed" }; return { response: new Response(null, { status: 200 }) } },
        post: async () => { throw new Error("dispatch performs zero subtask writes") },
      })
      const calls: Array<{ argv: string[]; input: JSONRecord }> = []
      const baseRunner = realStoreRunner(binary, dbPath, calls, worktree)
      let blocked = false
      const runner: DispatchRunner = { async run(argv, input, signal) {
        const loseAcknowledgement = !blocked && argv[1] === blockedVerb
        if (loseAcknowledgement) {
          blocked = true
          reader = new Database(dbPath, { readonly: true })
          reader.exec("BEGIN")
          reader.query("SELECT revision FROM store_revision").get()
        }
        const result = await baseRunner.run(argv, input, signal)
        if (loseAcknowledgement) {
          expect(result.exitCode, result.stderr).toBe(0)
          // The real FULL commit succeeded. Only its host acknowledgement is
          // lost; this fixture does not claim to inject a filesystem sync fault.
          return { exitCode: 1, stdout: "", stderr: "synthetic lost worker-evidence acknowledgement" }
        }
        return result
      } }
      configureConcordAdapter({ runner })
      const invoke = (toolName: string, args: any, callContext: any) => invokeConcordOperation(toolName, args, callContext)
      const dispatchVersion = await driveWorkflowToContract(workID, invoke, context)
      const windows = new DispatchWindows()
      const credentials = { async getPrivateKey() { return PRIVATE_SEED } }
      const dispatch = await dispatchLaneWorker({ work_id: workID, expected_version: dispatchVersion, idempotency_key: "recovery-dispatch", lane_id: lane.id }, { context, invoke, credentials, windows })
      expect(dispatch.outcome, JSON.stringify(dispatch)).toBe("ok")
      const taskArgs = nativeTaskSelector(dispatch)
      let nativeTaskCalls = 0
      await windows.bind(TASK_TOOL_ID, SESSION_ID, taskArgs, "recovery-task-call", async () => worktree, worktree)
      nativeTaskCalls++
      packet = JSON.parse(taskArgs.prompt)
      const packetFailures: string[] = []
      if (!validateAgentLanePacket(packet, packetFailures)) throw new Error(`retained recovery packet failed the closed packet schema: ${packetFailures.join("; ")}`)
      // The synthetic original completed report claims exactly the binding
      // the authorized packet carries (CD-0205): the packet's schema version
      // and its dispatched worker-job revision, never invented job metadata.
      const report = { schema_version: packet.schema_version, readback_model: READBACK_MODEL, status: "completed", worker_job: reportWorkerJob(packet), evidence: lane.evidence_obligations.map((obligation, index) => ({ obligation, detail: `original ${obligation}`, ...(index === 0 ? { predicate_ids: [WORKFLOW_PREDICATE.predicate_id] } : {}) })) }
      const output = { title: "task", output: taskResult(report), metadata: {} }
      await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION_ID, callID: "recovery-task-call", args: taskArgs }, output, { windows, credentials, runner, concordBinary: binary })
      const pending = JSON.parse(output.output.split("<concord_attempt>\n")[1].split("\n</concord_attempt>")[0])
      expect(pending.outcome).toBe("error")
      expect(pending.error.details.effect_state).toBe("unknown")
      expect(windows.inFlightAttempt(SESSION_ID)).not.toBeNull()
      const before = workerEffects(dbPath, workID)
      expect(before.map((event) => event.kind)).toEqual(blockedVerb === "worker-dispatch" ? ["worker.dispatched"] : ["worker.dispatched", "worker.completed"])
      const child = JSON.parse(exportedSession(packet))
      child.info = { id: "worker-session", parentID: SESSION_ID, directory: worktree }
      child.messages[child.messages.length - 1].parts = [{ type: "text", text: reportText(report) }]
      const fixture = { binary, dbPath, worktree, sessionID: SESSION_ID, parent: { info: { id: SESSION_ID, directory: worktree, metadata }, messages: [{ info: { id: MESSAGE_ID, sessionID: SESSION_ID, role: "assistant" }, parts: [{ id: "retained-task-part", sessionID: SESSION_ID, type: "tool", tool: TASK_TOOL_ID, state: { status: "completed", input: taskArgs, output: output.output } }] }] }, child, request: { operation: "worker_reconcile", input: { work_id: workID, attempt_id: packet.attempt_id, task_part_id: "retained-task-part", idempotency_key: "recover-original-report" } } }
      const recover = async (body: JSONRecord = fixture) => {
        const result = await runProcess([process.execPath, "-e", RECOVERY_PROCESS], JSON.stringify(body), join(import.meta.dir, "..", ".."))
        expect(result.exitCode, result.stderr).toBe(0)
        return JSON.parse(result.stdout)
      }
      const declaration = contractOperations.find((operation) => operation.id === "concord_work_transition.worker_reconcile")!
      const { envelope: refusedBudget } = await recover({ ...fixture, request: { ...fixture.request, input: { ...fixture.request.input, idempotency_key: "recover-original-report-over-budget", requested_budget_seconds: declaration.supported_budget_seconds + 1 } } })
      expect(refusedBudget.outcome, JSON.stringify(refusedBudget)).toBe("error")
      expect(refusedBudget.error.kind, JSON.stringify(refusedBudget)).toBe("budget_refused")
      expect(refusedBudget.error.effect_state).toBe("none")
      expect(workerEffects(dbPath, workID)).toEqual(before)
      const contextDeadline = await recover({
        ...fixture,
        contextReadDelayMs: 2_000,
        request: { ...fixture.request, input: { ...fixture.request.input, idempotency_key: "recover-context-deadline", requested_budget_seconds: 1 } },
      })
      expect(contextDeadline.envelope.error.kind, JSON.stringify(contextDeadline)).toBe("timeout")
      expect(contextDeadline.envelope.error.effect_state).toBe("none")
      expect(contextDeadline.envelope.error.recovery_action.kind).toBe("retry_same_request")
      expect(contextDeadline.aborted_phase).toBe("context_read")
      expect(contextDeadline.core_commands).toEqual(["project-resolve"])
      expect(contextDeadline.signal_count).toBe(1)
      expect(contextDeadline.abort_elapsed_ms).not.toBeNull()
      expect(contextDeadline.abort_elapsed_ms).toBeLessThan(1_600)
      expect(workerEffects(dbPath, workID)).toEqual(before)
      const { envelope: expired, abort_elapsed_ms: elapsed, aborted_phase: phase, signal_count: signalCount } = await recover({
        ...fixture,
        hostReadDelayMs: 2_000,
        request: {
          ...fixture.request,
          input: { ...fixture.request.input, idempotency_key: "recover-original-report-deadline", requested_budget_seconds: 1 },
        },
      })
      expect(expired.outcome, JSON.stringify(expired)).toBe("error")
      expect(["context_read", "host_proof_read", "mutation_transport", "reconciliation_read", "receipt_read"]).toContain(phase)
      if (phase === "context_read") {
        expect(expired.error.kind, JSON.stringify(expired)).toBe("timeout")
        expect(expired.error.effect_state).toBe("none")
      } else {
        expect(expired.error.kind, JSON.stringify(expired)).toBe("operation_conflict")
        expect(expired.error.effect_state).toBe("possible")
        expect(expired.error.recovery_action.kind).toBe("reconcile_operation")
      }
      expect(signalCount).toBe(1)
      expect(elapsed).not.toBeNull()
      expect(elapsed).toBeLessThan(1_600)
      expect(workerEffects(dbPath, workID)).toEqual(before)
      await Bun.write(configPath, JSON.stringify({ instructions: ["https://example.invalid/changed-after-worker"] }))
      for (let repeat = 0; repeat < 3; repeat++) {
        const { envelope: receipt } = await recover()
        expect(receipt.outcome, JSON.stringify(receipt)).toBe("ok")
        expect(receipt.result.worker_recovery.lifecycle_state).toBe("completed")
      }
      const after = workerEffects(dbPath, workID)
      expect(after.map((event) => event.kind)).toEqual(["worker.dispatched", "worker.completed"])
      expect(after.slice(0, before.length)).toEqual(before)
      expect(nativeTaskCalls).toBe(1)
      expect(dbValue(dbPath, "SELECT count(*) AS n FROM worker_attempts").n).toBe(1)
      expect(dbValue(dbPath, "SELECT count(*) AS n FROM worker_authorizations WHERE work_id=?", workID).n).toBe(1)
    } finally {
      if (reader) { reader.exec("ROLLBACK"); reader.close() }
      configureConcordAdapter({ reset: true })
      hostControlPlane().bind(undefined)
      if (previousConfig === undefined) delete process.env.OPENCODE_CONFIG
      else process.env.OPENCODE_CONFIG = previousConfig
    }
  }, 120_000)
}

// Full public dispatch capacity test: an admitted maximum premise —
// 4096 UTF-8 bytes of ASCII, the full approval limit — approved through the
// real store with eight synthetic predicates must dispatch through the public
// route with the objective byte-for-byte in inputs.task. Exercising the public
// route covers the core attempt, authorization window, and native Task binding;
// a packet-builder-only probe cannot establish those effects.
const MAX_PREMISE = "o".repeat(4_096)
const EIGHT_ROUTE_PREDICATES: JSONRecord[] = Array.from({ length: 8 }, (_, ordinal) => ({
  predicate_id: `predicate:route-e2e-eight-${ordinal}`,
  ordinal,
  outcome_kind: "check",
  outcome_payload: {
    kind: "check",
    check_ref: `check:bun-test/adapter/opencode/dispatch_route_end_to_end.test.ts/eight-${ordinal}`,
    expected_result: "pass",
    immutable_subject_ref: "commit:f076cef390c13944b831b9024334b291a435588b",
  },
}))

routeDeclaration("dispatches an admitted maximum premise with eight synthetic predicates through the real route", async () => {
  const root = await fixtureTempRoot("dispatch-maxpremise")
  const previousConfig = process.env.OPENCODE_CONFIG
  try {
    const { binary, dbPath, configPath, workID, worktree, lane } = await bootRouteFixture(root)
    process.env.OPENCODE_CONFIG = configPath
    const context = contextFor(worktree)
    let boundPacket: JSONRecord | null = null
    let sessionMetadata: Record<string, unknown> = {}
    hostControlPlane().bind({
      get: async ({ url, path }) => {
        if (url === SESSION_MESSAGES_ROUTE) {
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
      post: async () => { throw new Error("dispatch performs zero subtask writes") },
    })
    const realCalls: Array<{ argv: string[]; input: JSONRecord }> = []
    const realRunner = realStoreRunner(binary, dbPath, realCalls, worktree)
    configureConcordAdapter({ runner: realRunner })

    const invoke = (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) =>
      invokeConcordOperation(toolName, args as any, callContext, sessionDirectory)
    await driveWorkflowToContract(workID, invoke, context, MAX_PREMISE, EIGHT_ROUTE_PREDICATES)

    const routed = laneDispatchRequest({ operation: "workflow_action", input: { work_id: workID, expected_version: 12, action_id: "dispatch_worker", idempotency_key: "e2e-max-premise-dispatch", fields: { lane_id: "implement" } } })
    const windows = new DispatchWindows()
    const dispatchResult = await dispatchLaneWorker(routed as any, {
      context, invoke,
      credentials: { async getPrivateKey() { return PRIVATE_SEED } } satisfies CredentialStore,
      windows,
    })
    // The whole dispatch must land: the core authorizes the attempt and the
    // window opens.
    expect(dispatchResult.outcome, JSON.stringify(dispatchResult)).toBe("ok")
    expect(dispatchResult.dispatch_state).toBe("awaiting_worker")
    expect(windows.has(SESSION_ID)).toBe(true)

    const taskArgs = nativeTaskSelector(dispatchResult)
    await windows.bind(TASK_TOOL_ID, SESSION_ID, taskArgs, undefined, async () => worktree, worktree)
    const packet = JSON.parse(taskArgs.prompt as string) as JSONRecord
    boundPacket = packet
    // The objective reaches the worker byte-for-byte: 4096 UTF-8 bytes of
    // ASCII, no header, no trailer, no duplicate, with the typed binding
    // beside it and all eight predicates in ordinal order.
    expect(packet.inputs.task).toBe(MAX_PREMISE)
    expect(Buffer.byteLength(packet.inputs.task as string, "utf8")).toBe(4_096)
    expect(packet.inputs.task).not.toContain(workID)
    expect(packet.inputs.binding).toEqual({ objective_source: "contract_premise", work_version: 12, contract_version: 1, assigned_result: "files_touched" })
    const predicates = packet.inputs.outcome_predicates as JSONRecord[]
    expect(predicates).toHaveLength(8)
    predicates.forEach((predicate, ordinal) => {
      expect(predicate.predicate_id).toBe(`predicate:route-e2e-eight-${ordinal}`)
      expect(predicate.ordinal).toBe(ordinal)
      expect(predicate.outcome_payload).toEqual(EIGHT_ROUTE_PREDICATES[ordinal].outcome_payload)
    })

    // The core recorded the authorized attempt against all eight predicates.
    const dispatchEvent = dbRows(dbPath, `SELECT payload FROM worker_authorizations WHERE work_id='${workID}' AND attempt_id='${packet.attempt_id}'`)
    expect(JSON.parse(dispatchEvent[0].payload as string).worker_packet_predicate_ids).toEqual(EIGHT_ROUTE_PREDICATES.map((predicate) => predicate.predicate_id))

    // The authorized packet round-trips completion: the worker session opens
    // with it, the report discharges the assigned result, and the store
    // completes the attempt.
    const report = {
      schema_version: packet.schema_version,
      readback_model: READBACK_MODEL,
      status: "completed",
      worker_job: reportWorkerJob(packet as JSONRecord),
      evidence: lane.evidence_obligations.map((obligation: string, index: number) => ({
        obligation,
        detail: `discharged ${obligation}`,
        ...(index === 0 ? { predicate_ids: ["predicate:route-e2e-eight-0"] } : {}),
      })),
    }
    const completionOutput = { title: "task", output: taskResult(report), metadata: {} }
    await completeDispatchedWorker({ tool: TASK_TOOL_ID, sessionID: SESSION_ID, callID: "e2e-maxpremise-call", args: taskArgs }, completionOutput, { windows, credentials: { async getPrivateKey() { return PRIVATE_SEED } }, runner: realRunner, concordBinary: binary })
    expect(completionOutput.output).toContain('<concord_attempt>')
    const attempt = dbValue(dbPath, `SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id='${packet.attempt_id}'`)
    expect(attempt.lifecycle_state).toBe("completed")
    expect(attempt.readback_model).toBe(READBACK_MODEL)
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    if (previousConfig === undefined) delete process.env.OPENCODE_CONFIG
    else process.env.OPENCODE_CONFIG = previousConfig
  }
}, 120_000)

// The oversized-session completion is verified against the real core and
// store: a transcript past the fixed 8 MiB export bound the readback once
// crossed must complete through the bounded message pages, and the evidence
// verbs must land on the real CLI and its worker_attempts row.
routeDeclaration("records an oversized worker session through the real CLI and store", async () => {
  const root = await fixtureTempRoot("dispatch-oversized")
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
      post: async () => { throw new Error("dispatch performs zero subtask writes") },
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
    const taskArgs = nativeTaskSelector(dispatchResult)
    await windows.bind(TASK_TOOL_ID, SESSION_ID, taskArgs, undefined, async () => worktree, worktree)
    const packet = JSON.parse(taskArgs.prompt as string) as JSONRecord
    boundPacket = packet
    // The transcript the readback walks crosses the fixed 8 MiB export bound
    // that born the failure under repair.
    expect(Buffer.byteLength(exportedSession(packet, BULK_TEXT_BYTES))).toBeGreaterThan(8_388_608)
    const report = {
      schema_version: packet.schema_version,
      readback_model: READBACK_MODEL,
      status: "completed",
      worker_job: reportWorkerJob(packet as JSONRecord),
      evidence: lane.evidence_obligations.map((obligation: string, index: number) => ({
        obligation,
        detail: `discharged ${obligation}`,
        ...(index === 0 ? { predicate_ids: [WORKFLOW_PREDICATE.predicate_id] } : {}),
      })),
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
  }
}, 120_000)

// The born-failed recording is verified against the real core and store: a
// refused session read must leave one durable failed attempt row written by
// the real CLI, with the refusal diagnosable from the store alone — the exact
// surface that once answered 'worker evidence assertion timestamp invalid'
// and left no attempt row.
routeDeclaration("records a refused readback as a durable failed attempt through the real CLI and store", async () => {
  const root = await fixtureTempRoot("dispatch-refused")
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
      post: async () => { throw new Error("dispatch performs zero subtask writes") },
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
    const taskArgs = nativeTaskSelector(dispatchResult)
    await windows.bind(TASK_TOOL_ID, SESSION_ID, taskArgs, undefined, async () => worktree, worktree)
    const packet = JSON.parse(taskArgs.prompt as string) as JSONRecord
    refuseWorkerSessionRead = true
    const report = {
      schema_version: packet.schema_version,
      readback_model: READBACK_MODEL,
      status: "completed",
      worker_job: reportWorkerJob(packet as JSONRecord),
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
  }
}, 120_000)
