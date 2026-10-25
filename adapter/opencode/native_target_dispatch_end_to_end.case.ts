// The synthetic non-Go native dispatch end-to-end case (design section 9.9).
// The real production OpenCode adapter drives the real core binary against a
// fresh synthetic SQLite store whose target Project fixture carries no Go
// file and no Go module: the closed target_runner_v1 recipe with materializer
// `none` and runner `check` pins one actual bash script (literal --expected 7)
// that reads the project's production value 7. The route records real native
// refs and never invents readiness: the qualified preparation run reference
// comes from the core's own prepare lease, the ready job pins that reference,
// the adapter consumes the core's work-context projection into the canonical
// packet, the persisted host dispatch authorizes, and the execute lease grades
// the host-observed check exit status against the exact selected witness. No
// model runs: the Task host readback is the controlled mock the dispatch-route
// case established, and every core effect stays on the real binary.
import { expect } from "bun:test"
import { createHash, createPublicKey, verify as verifySignature } from "node:crypto"
import { mkdir } from "node:fs/promises"
import { delimiter, join } from "node:path"
import { fixtureTempRoot, ownedFixtureRunRoot, requireOwnedFixtureRun, runFixtureProcess } from "./fixture-temp-root"
import { configureConcordAdapter, executeBoundWorkerControl, invokeConcordOperation, laneDispatchRequest } from "./concord"
import { configureCoreBinary, type DispatchRunner } from "./dispatch"
import { completeDispatchedWorker } from "./lane_completion"
import { dispatchLaneWorker } from "./lane_dispatch"
import { DispatchWindows, TASK_TOOL_ID } from "./dispatch-window"
import type { CredentialStore } from "./credentials"
import { agentLanes } from "./generated-agent-lanes"
import { hostControlPlane, MANAGED_TASK_SCOPE_KEY, SESSION_MESSAGES_ROUTE, SESSION_ROUTE } from "./move-session"
import { dbRows, dbValue, fixturePrivateKey, fixturePublicKeyBase64, git, nativeTaskSelector, reportText, reportWorkerJob, requiredFields, routeDeclaration, runJSON, runProcess, type JSONRecord } from "./end-to-end.fixture"

import { createWorkerCapabilityGuard } from "./lane_capabilities"
import { canonicalWorkerControl, workerCheckTool } from "./worker_control"

// The route test drives the real core through its own runner, so argv[0] is
// replaced there. Bind the nominal path the transport resolves instead of the
// unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")
requireOwnedFixtureRun()

const PRODUCT_ID = "product-e2e"
const PROJECT_ID = "project-e2e"
const CLIENT_REF = "opencode"
const SESSION_ID = "e2e-session"
const MESSAGE_ID = "e2e-message"
const READBACK_MODEL = "openai/gpt-5.6-luna"
const PRIVATE_SEED = new Uint8Array(32).fill(7)
// The closed target_runner_v1 recipe identities of this fixture. The root is
// the nested contained directory `checkkit`: the shared work-context
// repository-path validator refuses a bare "." root, and the parent owns that
// gap report, so this case pins every declared path under the contained root.
const RECIPE_PATH = "test-oracle/manifest.json"
const TARGET_ROOT = "checkkit"
const HARNESS_SCRIPT = "checkkit/check.sh"
const WITNESS_ID = "witness:production-value-seven"
const CASE_ID = "case:native-e2e"
const CONTROL_ID = "control:native-e2e"
const OWNER_ID = "owner:native-e2e"
const JOB_ID = "job:native-e2e-check"
const APPROVED_OBJECTIVE = "Check the synthetic checkkit production value with the pinned target_runner_v1 check."
let sharedCoreBinary: Promise<string> | undefined

function logicalArgvDigest(argv: string[]): string {
  return "sha256:" + createHash("sha256").update(JSON.stringify(argv)).digest("hex")
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

function taskResult(report: JSONRecord, workerSessionID = "worker-session"): string {
  return [
    `<task id="${workerSessionID}" state="completed">`,
    "<task_result>",
    reportText(report),
    "</task_result>",
    "</task>",
  ].join("\n")
}

// CD-0102: an authorized worker session opens with the dispatch packet as its
// first user message, so the host readback mock serves the bound packet once
// the test captures it from the rewritten Task call.
function exportedSession(opening: JSONRecord | null = null, workerAgent = "concord-implement"): string {
  return JSON.stringify({
    info: { id: "worker-session" },
    messages: [
      ...(opening ? [{ info: { id: "worker-message-open", sessionID: "worker-session", role: "user", agent: workerAgent, time: { created: 0 } }, parts: [{ type: "text", text: JSON.stringify(opening) }] }] : []),
      { info: { id: "worker-message", sessionID: "worker-session", role: "assistant", agent: workerAgent, providerID: "openai", modelID: "gpt-5.6-luna", time: { created: 1 } }, parts: [] },
    ],
  })
}

interface NativeTargetFixture {
  binary: string
  repo: string
  dbPath: string
  configPath: string
  workID: string
  worktree: string
  lane: (typeof agentLanes)[number]
  bashPin: string
}

// bootNativeTargetFixture builds the real store and the non-Go repository
// fixture: one built core binary, one real git repository with no Go file and
// no Go module anywhere, the synthetic knowledge home, one bootstrapped work
// item, and one registered client carrying the worker evidence and dispatch
// capabilities. The pinned bash check script reads production.txt and compares
// it with the literal --expected value, so the graded witness exercises the
// project's own production bytes.
async function bootNativeTargetFixture(root: string): Promise<NativeTargetFixture> {
  // The CLI's native producer uses the installed local toolchain, not Go's
  // module-triggered launcher switch. Pin the toolchain that builds this test.
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
  // The exact bash version pin comes from the installed interpreter, computed
  // the way the core's own lead-version reader computes it: first line of
  // `bash --version`, first `version <digits and dots>` match.
  const bashVersion = await runProcess(["bash", "--version"])
  expect(bashVersion.exitCode, bashVersion.stderr).toBe(0)
  const bashPin = bashVersion.stdout.split("\n")[0].trim().match(/version ([0-9][0-9.]*)/)?.[1]
  if (!bashPin) throw new Error(`unparseable bash version: ${bashVersion.stdout}`)
  expect(bashPin, `unparseable bash version: ${bashVersion.stdout}`).toMatch(/^[0-9]+(\.[0-9]+)*$/)

  const repo = join(root, "repo")
  const dbPath = join(root, "concord.db")
  const configPath = join(repo, "opencode.jsonc")
  const lane = agentLanes.find((candidate) => candidate.id === "verify")
  if (!lane) throw new Error("verify lane is not registered")

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
  await Bun.write(join(repo, "README.md"), "synthetic non-Go dispatch fixture\n")
  await mkdir(join(repo, "checkkit"))
  await mkdir(join(repo, "test-oracle"))
  // The pinned harness script: a real bash check with the literal
  // `--expected` flag. It grades the project's production value file, so the
  // execute lease observes the candidate's own bytes, never a canned result.
  const checkScript = [
    "set -eu",
    'expected=""',
    'while [ "$#" -gt 0 ]; do',
    '  case "$1" in',
    "    --expected) expected=\"$2\"; shift 2 ;;",
    '    *) printf \'unknown argument: %s\\n\' "$1" >&2; exit 2 ;;',
    "  esac",
    "done",
    'if [ -z "$expected" ]; then',
    "  printf 'no expected value supplied\\n' >&2",
    "  exit 2",
    "fi",
    "actual=$(tr -d '[:space:]' < production.txt)",
    'if [ "$actual" != "$expected" ]; then',
    "  printf 'production value %s does not match expected %s\\n' \"$actual\" \"$expected\" >&2",
    "  exit 1",
    "fi",
    "printf 'check ok: production %s = expected %s\\n' \"$actual\" \"$expected\"",
    "",
  ].join("\n")
  await Bun.write(join(repo, "checkkit/check.sh"), checkScript)
  await Bun.write(join(repo, "checkkit/production.txt"), "7\n")
  await Bun.write(join(repo, RECIPE_PATH), JSON.stringify({
    kind: "target_runner_v1",
    materializer: "none",
    runner: "check",
    root: TARGET_ROOT,
    harness_files: [HARNESS_SCRIPT],
    support_files: [],
    cases: { [CASE_ID]: [WITNESS_ID] },
    checks: [{ witness: WITNESS_ID, argv: ["bash", "check.sh", "--expected", "7"] }],
    pin: { shell: bashPin },
  }, null, 2))
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
    title: "Synthetic non-Go native dispatch route",
    value_statement: "The route completes one real non-Go native attempt.",
    kind: "bug",
    task: "Exercise the non-Go native dispatch route.",
    idempotency_key: "native-target-dispatch-e2e",
    defect_intake: { failure_shape: "native-target-dispatch-e2e", reproduction: "run the synthetic non-Go dispatch route", searched: "the synthetic store", related_defect_ids: [] },
    priority: 1,
    urgency: "standard",
    tags: [],
    workflow_type_ref: "",
    external_ref: "CON-942",
    governing_requirements: [],
    ref: "HEAD",
  }, repo)
  await runJSON(binary, dbPath, "client-register", {
    client_ref: CLIENT_REF,
    key_id: "native-e2e-key",
    principal_ref: "operator-1",
    public_key: fixturePublicKeyBase64(PRIVATE_SEED),
    capabilities: ["product_read", "work_define", "work_transition", "work_relate", "work_compact", "research", "worker_evidence", "worker_dispatch"],
    product_scope: [PRODUCT_ID],
    project_scope: [PROJECT_ID],
    agent_scope: ["concord-implement", "concord-review", "concord-verify"],
  })
  return { binary, repo, dbPath, configPath, workID: bootstrap.work_id as string, worktree: bootstrap.worktree.path as string, lane, bashPin }
}

// realStoreRunner spawns the real core binary against the real store and
// records the evidence verbs the route observes. This is the transport the
// dispatch-route case established: no accepting stub stands between the
// adapter and the core on the evidence path. The `session` leg stays a
// host-shaped stub because it names the `opencode` CLI, not the core, and the
// host control plane answers the host session routes when it is bound.
function realStoreRunner(binary: string, dbPath: string, realCalls: Array<{ argv: string[]; input: JSONRecord }>, worktree: string): DispatchRunner {
  return {
    async run(argv, input, signal) {
      if (argv[1] === "session") return { exitCode: 0, stdout: JSON.stringify([{ id: "worker-session", directory: worktree, parentID: SESSION_ID }, { id: SESSION_ID, directory: worktree }]), stderr: "" }
      if (argv[1] === "worker-dispatch" || argv[1] === "worker-complete" || argv[1] === "worker-fail" || argv[1] === "worker-control" || argv[1] === "invoke") {
        realCalls.push({ argv, input: JSON.parse(input) as JSONRecord })
      }
      return runFixtureProcess([binary, ...argv.slice(1)], input, { env: { ...process.env, CONCORD_DB_PATH: dbPath }, signal })
    },
  }
}

// driveToReadyJob walks the real workflow from capture to the approved
// contract state, prepares the pinned target_runner_v1 oracle through the
// core's own native operation, and records the ready worker job against the
// actual qualified preparation reference. The premise and predicate are
// parameters so the subject commit stays the fixture's real HEAD. It returns
// the dispatch-ready work version and the actual preparation reference.
async function driveToReadyJob(
  workID: string,
  invoke: (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) => Promise<any>,
  context: any,
  subjectCommit: string,
  bashPin: string,
): Promise<{ dispatchVersion: number; preparationRef: string; control: JSONRecord }> {
  const witness = WITNESS_ID
  const predicate: JSONRecord = {
    predicate_id: "predicate:native-target-e2e-passes",
    ordinal: 0,
    outcome_kind: "check",
    outcome_payload: {
      kind: "check",
      check_ref: "check:bun-test/adapter/opencode/native_target_dispatch_end_to_end.test.ts",
      expected_result: "pass",
      immutable_subject_ref: `commit:${subjectCommit}`,
    },
  }
  const owner = {
    owner_id: OWNER_ID,
    domain_id: `product-root:${PRODUCT_ID}`,
    mechanism: { project_id: PROJECT_ID, path: HARNESS_SCRIPT, entry_point: "check" },
    obligation: "The synthetic production value equals the pinned expected value.",
    predicate_ids: [predicate.predicate_id],
    law_bindings: [],
  }
  const oracleCase = {
    case_id: CASE_ID,
    owner_id: OWNER_ID,
    entry_point: "check",
    input_class: "synthetic non-Go check fixture",
    expected_state: "The production value equals the pinned expected value.",
    control_ids: [CONTROL_ID],
  }
  // The closed check selector is the logical `check` identity followed by the
  // exact selected witness strings; the core refuses any other argv.
  const control: JSONRecord = {
    control_id: CONTROL_ID,
    owner_id: OWNER_ID,
    predicate_ids: [predicate.predicate_id],
    case_ids: [CASE_ID],
    recipe_source: { kind: "repository_file", project_id: PROJECT_ID, path: RECIPE_PATH, commit_oid: subjectCommit },
    argv: ["check", witness],
    cwd: TARGET_ROOT,
    expected_result: "pass",
    required_evidence_role: "reported",
  }
  const transition = (expected: number, actionID: string, idempotencyKey: string, fields: JSONRecord) => invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: expected, action_id: actionID, idempotency_key: idempotencyKey, fields } }, context)
  // CD-0198 D2: one typed record_diagnosis carries the reproduction,
  // alignment-search, and root-cause sections the route case records.
  let response = await transition(5, "record_diagnosis", "native-e2e-diagnosis", {
    reproduction: "The defect reproduces from a clean store with the seeded non-Go project.",
    searched: "Searched the backlog for duplicate defect work.",
    outcome: "none_found",
    root_cause: "The fixture recorded no production-value check for the synthetic route.",
  })
  expect(response.outcome, JSON.stringify(response)).toBe("ok")
  const domainList = await invoke("concord_domain", { operation: "list", input: { product_id: PRODUCT_ID, page: { cursor: null, limit: 10 } } }, context)
  expect(domainList.outcome).toBe("ok")
  const registryHash = ((domainList.result as JSONRecord).registry as JSONRecord).content_hash as string
  response = await transition(8, "approve_contract", "native-e2e-approve-contract", {
    premise: APPROVED_OBJECTIVE,
    outcome_predicates: [predicate],
    required_evidence: ["verification", "review"],
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
  const verify = (idempotencyKey: string, input: JSONRecord) => invoke("concord_work_transition", {
    operation: "worktree_verify", input: { work_id: workID, idempotency_key: idempotencyKey, requested_budget_seconds: 300, ...input },
  }, context)
  // The core subject: one green verify on the clean candidate pins the raw
  // commit the preparation and the packet's work context must join to.
  response = await verify("native-e2e-oracle-subject", { command: ["git", "status", "--porcelain"] })
  expect(response.outcome, JSON.stringify(response.error ?? null)).toBe("ok")
  expect(response.result.exit_code).toBe(0)
  expect(response.result.tracked_files_changed).toBe(false)
  // Preparation through the existing native operation: the recipe commit is
  // materialized, the pinned harness is overlaid, and the check-input stage
  // proves the script resolves without executing the check.
  response = await verify("native-e2e-oracle-prepare", { oracle: {
    phase: "prepare", expected_contract_version: 1,
    control_bundle: { owner, cases: [oracleCase], control },
  } })
  expect(response.outcome, JSON.stringify(response.error ?? null)).toBe("ok")
  const prepared = response.result.oracle as JSONRecord
  expect(prepared.qualification).toBe("ready")
  expect(prepared.subject_commit).toBe(subjectCommit)
  const preparationRef = prepared.run_ref as string
  expect(preparationRef).toMatch(/^worktree_verify:/)
  // The real core target metadata: the closed family identity and witness
  // grammar, never fabricated Go test names.
  expect(prepared.protocol).toBe("native_oracle_v2")
  expect(prepared.recipe_kind).toBe("target_runner_v1")
  expect(prepared.target_materializer).toBe("none")
  expect(prepared.target_runner).toBe("check")
  expect(prepared.selected_witness_ids).toEqual([witness])
  expect(prepared.case_to_witness_map).toEqual({ [CASE_ID]: [witness] })
  expect(prepared.selected_test_names).toBeUndefined()
  expect(prepared.case_to_test_map).toBeUndefined()
  expect(prepared.selected_distinct_count).toBe(1)
  expect(prepared.toolchain_identity).toBe(`bash ${bashPin}`)
  expect(prepared.logical_cwd).toBe(TARGET_ROOT)
  expect(prepared.logical_argv_digest).toBe(logicalArgvDigest(control.argv as string[]))
  expect(prepared.streams_complete).toBe(true)
  expect(prepared.stdout.complete).toBe(true)
  expect(prepared.stderr.complete).toBe(true)
  expect((prepared.files as JSONRecord[]).map((file) => file.path)).toEqual([HARNESS_SCRIPT])
  // The canonical prepare lease command is the check-input stage.
  expect(response.result.command).toEqual(["bash", "-n", "check.sh"])
  // The job-capable break-fix definition dispatches the verification lane only
  // under a recorded ready worker-job revision (CD-0205) that pins the actual
  // preparation reference.
  response = await transition(10, "record_worker_job", "native-e2e-worker-job", {
    job_id: JOB_ID,
    predicate_ids: owner.predicate_ids,
    objective: "Check the synthetic non-Go production value with the pinned target check.",
    stopping_condition: "The pinned check exits zero on the current subject.",
    path_scope: [TARGET_ROOT],
    checks: ["bash checkkit/check.sh --expected 7"],
    ready: true,
    readiness_evidence: [preparationRef],
    acceptance_oracle: { owners: [owner], cases: [oracleCase], controls: [{ ...control, readiness_evidence_refs: [preparationRef] }] },
  })
  expect(response.outcome, JSON.stringify(response.error ?? null)).toBe("ok")
  const jobRead = (await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID, page: { cursor: null, limit: 1 } } }, context)).result as JSONRecord
  const ready = (jobRead.pinned as JSONRecord).ready_worker_jobs as JSONRecord[]
  expect(ready.map((job) => [job.job_id, job.revision])).toEqual([[JOB_ID, 1]])
  const dispatchVersion = ((response.result as JSONRecord).changed_refs as JSONRecord[]).find((changed) => changed.entity_kind === "work_item")!.version as number
  return { dispatchVersion, preparationRef, control }
}

function nativeWorkerReport(lane: (typeof agentLanes)[number], packet: JSONRecord, subjectCommit: string, predicateID: string): JSONRecord {
  return {
    schema_version: packet.schema_version,
    readback_model: READBACK_MODEL,
    status: "completed",
    worker_job: reportWorkerJob(packet),
    evidence: lane.evidence_obligations.map((obligation: string, index: number) => ({
      obligation,
      detail: `verified ${obligation} on commit:${subjectCommit}`,
      ...(index === 0 ? { predicate_ids: [predicateID] } : {}),
    })),
  }
}

interface NativeTaskDispatch {
  dispatchResult: JSONRecord
  windows: DispatchWindows
  taskArgs: JSONRecord
  packet: JSONRecord
  callID: string
  sessionID: string
  workerSessionID: string
  retainedOutput?: string
}

async function dispatchNativeTask(
  lane: (typeof agentLanes)[number],
  workID: string,
  worktree: string,
  expectedVersion: number,
  idempotencyKey: string,
  callID: string,
  context: any,
  invoke: (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) => Promise<any>,
  setReadback: (packet: JSONRecord, agent: string) => void,
): Promise<NativeTaskDispatch> {
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
  const taskArgs = nativeTaskSelector(dispatchResult)
  await windows.bind(TASK_TOOL_ID, context.sessionID, taskArgs, callID, async () => worktree, worktree)
  const packet = JSON.parse(taskArgs.prompt as string) as JSONRecord
  setReadback(packet, `concord-${lane.id}`)
  expect(taskArgs.subagent_type).toBe(`concord-${lane.id}`)
  return { dispatchResult, windows, taskArgs, packet, callID, sessionID: context.sessionID, workerSessionID: context.workerSessionID ?? "worker-session" }
}

async function completeNativeTask(
  task: NativeTaskDispatch,
  report: JSONRecord,
  runner: DispatchRunner,
  binary: string,
): Promise<JSONRecord> {
  const completionOutput = { title: "task", output: taskResult(report, task.workerSessionID), metadata: {} }
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

routeDeclaration("N01 drives one synthetic non-Go target check through native preparation dispatch authorization and host observed execution", async () => {
  const root = await fixtureTempRoot("native-e2e")
  const previousConfig = process.env.OPENCODE_CONFIG
  try {
    const { binary, repo, dbPath, configPath, workID, worktree, lane, bashPin } = await bootNativeTargetFixture(root)
    process.env.OPENCODE_CONFIG = configPath
    const context = contextFor(worktree)
    const subjectCommit = (await runProcess(["git", "rev-parse", "HEAD"], "", worktree)).stdout.trim()
    expect(subjectCommit).toMatch(/^[0-9a-f]{40}$/)
    // The host control plane is the controlled Task readback mock: the worker
    // session serves the bound packet as its opening message, and the
    // coordinator session takes the managed task scope. No model runs.
    let boundPacket: JSONRecord | null = null
    let activeWorkerAgent = "concord-implement"
    let sessionMetadata: Record<string, unknown> = {}
    hostControlPlane().bind({
      get: async ({ url, path }) => {
        if (url === SESSION_MESSAGES_ROUTE) {
          expect(path?.id).toBe("worker-session")
          const parsed = JSON.parse(exportedSession(boundPacket, activeWorkerAgent)) as { messages: unknown[] }
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
    const calls: Array<{ argv: string[]; input: JSONRecord }> = []
    const runner = realStoreRunner(binary, dbPath, calls, worktree)
    configureConcordAdapter({ runner, credentials: { async getPrivateKey() { return PRIVATE_SEED } } })
    const invoke = (toolName: string, args: { operation: string; input: Record<string, unknown> }, callContext: any, sessionDirectory?: string) => invokeConcordOperation(toolName, args as any, callContext, sessionDirectory) as Promise<JSONRecord>
    const version = () => dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version
    const transition = (actionID: string, idempotencyKey: string, fields: JSONRecord) => invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: version(), action_id: actionID, idempotency_key: idempotencyKey, fields } }, context)

    const { dispatchVersion, preparationRef, control } = await driveToReadyJob(workID, invoke, context, subjectCommit, bashPin)
    // The core's work-context projection carries the real selected
    // preparation: the store joined the ready job's readiness reference to its
    // retained receipt before the adapter ever reads it.
    const preDispatch = (await invoke("concord_work_trace", { operation: "continuity", input: { work_id: workID, page: { cursor: null, limit: 1 } } }, context)).result as JSONRecord
    const workContext = ((preDispatch.pinned as JSONRecord).work_pin as JSONRecord).work_context as JSONRecord | undefined
    expect(workContext, JSON.stringify(preDispatch.pinned)).toBeDefined()
    expect(workContext!.subject_commit).toBe(subjectCommit)
    const projected = workContext!.oracle_preparations as JSONRecord[]
    expect(projected).toHaveLength(1)
    expect(projected[0].run_ref).toBe(preparationRef)
    expect(projected[0].recipe_kind).toBe("target_runner_v1")
    expect(projected[0].target_materializer).toBe("none")
    expect(projected[0].target_runner).toBe("check")
    expect(projected[0].selected_witness_ids).toEqual([WITNESS_ID])
    expect(projected[0].selected_test_names).toBeUndefined()
    expect(projected[0].case_to_test_map).toBeUndefined()
    expect(projected[0].work_id).toBe(workID)
    expect(projected[0].project_id).toBe(PROJECT_ID)
    expect(projected[0].contract_version).toBe(1)

    // The production adapter dispatch: packet build from the core projection,
    // readiness authorization, persisted dispatch, and the single-use window.
    // The worker child keeps the route's default "worker-session" identity,
    // the same identity the readback mock serves.
    const task = await dispatchNativeTask(lane, workID, worktree, dispatchVersion, "native-e2e-dispatch", "native-e2e-task-call", context, invoke, (packet, agent) => { boundPacket = packet; activeWorkerAgent = agent })
    const packet = task.packet
    expect(packet.work_id).toBe(workID)
    expect(packet.step_id).toBe("repair")
    expect(packet.inputs.task).toBe(APPROVED_OBJECTIVE)
    expect(lane.capability_class).toBe("verification")
    expect(packet.inputs.binding).toEqual({ objective_source: "contract_premise", work_version: dispatchVersion, contract_version: 1, assigned_result: "exit_codes" })
    expect(packet.inputs.work_record).toEqual({ value_statement: "The route completes one real non-Go native attempt.", task: "Exercise the non-Go native dispatch route." })
    expect(packet.inputs.law_context).toEqual({ laws: [], domains: [{ domain_id: `product-root:${PRODUCT_ID}`, name: "Synthetic root", purpose: "Synthetic test domain" }], registry_path: join(repo, ".concord/docs/knowledge/domain-registry.json") })
    expect(packet.inputs.outcome_predicates).toEqual([{ ...packet.inputs.outcome_predicates[0] }])
    expect(packet.inputs.outcome_predicates[0].predicate_id).toBe("predicate:native-target-e2e-passes")
    expect(packet.inputs.outcome_predicates[0].outcome_payload.immutable_subject_ref).toBe(`commit:${subjectCommit}`)
    const job = packet.inputs.worker_job as JSONRecord
    expect(job.job_id).toBe(JOB_ID)
    expect((job.acceptance_oracle as JSONRecord).controls[0].readiness_evidence_refs).toEqual([preparationRef])
    // The canonical packet carries the typed target preparation verbatim from
    // the core projection: closed axes, real witness identities, exact joins.
    const packetPreparations = (packet.inputs.work_context as JSONRecord).oracle_preparations as JSONRecord[]
    expect(packetPreparations).toHaveLength(1)
    const preparation = packetPreparations[0]
    expect(preparation.recipe_kind).toBe("target_runner_v1")
    expect(preparation.target_materializer).toBe("none")
    expect(preparation.target_runner).toBe("check")
    expect(preparation.selected_witness_ids).toEqual([WITNESS_ID])
    expect(preparation.case_to_witness_map).toEqual({ [CASE_ID]: [WITNESS_ID] })
    expect(preparation.selected_test_names).toBeUndefined()
    expect(preparation.case_to_test_map).toBeUndefined()
    expect(preparation.qualification).toBe("ready")
    expect(preparation.phase).toBe("prepare")
    expect(preparation.protocol).toBe("native_oracle_v2")
    expect(preparation.run_ref).toBe(preparationRef)
    expect(preparation.work_id).toBe(workID)
    expect(preparation.project_id).toBe(PROJECT_ID)
    expect(preparation.contract_version).toBe(1)
    expect(preparation.subject_commit).toBe(subjectCommit)
    expect(preparation.logical_cwd).toBe(TARGET_ROOT)
    expect(preparation.logical_argv_digest).toBe(logicalArgvDigest(control.argv as string[]))
    expect((preparation.recipe_source as JSONRecord)).toEqual(control.recipe_source)
    expect((preparation.files as JSONRecord[]).map((file) => file.path)).toEqual([HARNESS_SCRIPT])
    const packetDigest = task.dispatchResult.result?.worker_packet_digest as string
    expect(packetDigest).toMatch(/^sha256:[0-9a-f]{64}$/)

    // Every execute selector comes from coordinator-visible output, never the
    // private dispatch window, Task prompt, or database.
    const dispatchPin = (task.dispatchResult.work_pins as JSONRecord[]).find((pin) => pin.work_id === workID)!
    expect(dispatchPin.attempt.id).toBe(packet.attempt_id)
    const epoch = dispatchPin.attempt.epoch as number
    expect(Number.isSafeInteger(epoch) && epoch > 0).toBe(true)
    const visibleJob = ((preDispatch.pinned as JSONRecord).ready_worker_jobs as JSONRecord[])[0]
    const visibleControl = visibleJob.acceptance_oracle.controls[0] as JSONRecord
    expect(visibleControl.readiness_evidence_refs).toEqual([preparationRef])
    const executeOracle = {
      phase: "execute", attempt_id: dispatchPin.attempt.id, attempt_epoch: epoch,
      worker_packet_digest: packetDigest,
      worker_job_binding: { job_id: visibleJob.job_id, revision: visibleJob.revision, digest: visibleJob.digest },
      control_id: visibleControl.control_id, preparation_run_ref: visibleControl.readiness_evidence_refs[0],
    }
    const forgedDigest = "sha256:" + "0".repeat(64)
    expect(forgedDigest).not.toBe(packetDigest)
    const leasesBeforeForgery = dbValue(dbPath, "SELECT count(*) AS count FROM worktree_verify_leases").count
    const forged = await invoke("concord_work_transition", { operation: "worktree_verify", input: {
      work_id: dispatchPin.work_id, idempotency_key: "native-e2e-oracle-forged-digest", requested_budget_seconds: 300,
      oracle: { ...executeOracle, worker_packet_digest: forgedDigest },
    } }, context)
    expect(forged.outcome).toBe("error")
    expect(forged.error.kind).toBe("unauthorized")
    expect(forged.error.effect_state).toBe("none")
    expect(forged.error.message).toContain("native oracle execution lacks the exact live dispatch and candidate binding")
    expect(dbValue(dbPath, "SELECT count(*) AS count FROM worktree_verify_leases").count).toBe(leasesBeforeForgery)

    // The same public selectors with the recorded digest admit the execute
    // lease and grade the host-observed invocation against the selected witness.
    const execution = await invoke("concord_work_transition", { operation: "worktree_verify", input: {
      work_id: dispatchPin.work_id, idempotency_key: "native-e2e-oracle-execute", requested_budget_seconds: 300,
      oracle: executeOracle,
    } }, context)
    expect(execution.outcome, JSON.stringify(execution.error ?? null)).toBe("ok")
    const executed = execution.result.oracle as JSONRecord
    expect(executed.qualification).toBe("pass")
    expect(executed.subject_commit).toBe(subjectCommit)
    expect(executed.observed_witness_ids).toEqual([WITNESS_ID])
    expect(executed.target_materializer).toBe("none")
    expect(executed.target_runner).toBe("check")
    expect(execution.result.command).toEqual(["check", WITNESS_ID])
    expect(execution.result.exit_code).toBe(0)
    let executionRef = execution.result.operation_ref as string
    expect(executionRef).toMatch(/^worktree_verify:/)
    const executedStages = executed.stages as JSONRecord[]
    expect(executedStages).toHaveLength(1)
    expect(executedStages[0].name).toBe("test")
    expect(executedStages[0].argv).toEqual(["bash", "check.sh", "--expected", "7"])
    expect(executedStages[0].exit_code).toBe(0)

    const childContext = { ...contextFor(worktree, task.workerSessionID), agent: `concord-${lane.id}` }
    const childCheck = workerCheckTool(createWorkerCapabilityGuard({ windows: task.windows }), executeBoundWorkerControl)
    expect(calls.filter((call) => call.argv[1] === "worker-dispatch")).toHaveLength(0)
    expect(dbValue(dbPath, `SELECT lifecycle_state FROM worker_attempts WHERE attempt_id='${packet.attempt_id}'`).lifecycle_state).toBe("in_flight")
    const childExecution = JSON.parse(await childCheck.execute({ control_id: CONTROL_ID }, childContext)) as JSONRecord
    expect(childExecution.oracle.qualification).toBe("pass")
    expect(childExecution.oracle.subject_commit).toBe(subjectCommit)
    expect(childExecution.oracle.control_id).toBe(CONTROL_ID)
    expect(childExecution.command).toEqual(["check", WITNESS_ID])
    expect(childExecution.exit_code).toBe(0)
    expect(childExecution.operation_ref).not.toBe(executionRef)
    executionRef = childExecution.operation_ref
    const controlCalls = calls.filter((call) => call.argv[1] === "worker-control")
    expect(controlCalls).toHaveLength(1)
    const assertion = controlCalls[0].input.assertion
    expect(assertion.agent_ref).toBe(childContext.agent)
    expect(assertion.session_ref).toBe(task.workerSessionID)
    expect(assertion.session_ref).not.toBe(context.sessionID)
    expect(assertion.packet_digest).toBe(packetDigest)
    expect(assertion.control_id).toBe(CONTROL_ID)
    expect(verifySignature(null, Buffer.from(canonicalWorkerControl(assertion)), createPublicKey(fixturePrivateKey(PRIVATE_SEED)), Buffer.from(assertion.signature, "base64"))).toBe(true)
    expect(calls.filter((call) => call.argv[1] === "worker-dispatch")).toHaveLength(0)
    expect(dbValue(dbPath, `SELECT lifecycle_state FROM worker_attempts WHERE attempt_id='${packet.attempt_id}'`).lifecycle_state).toBe("in_flight")

    // The worker reports the typed receipt bound to the actual retained run
    // reference. The core's completion join proves the receipt shares the
    // core's own execute joins: work, control, project, command, bundle
    // digest, subject, and exit status. A fabricated reference refuses the
    // completion, so a passing completion is the store-validated readback.
    const report = nativeWorkerReport(lane, packet, subjectCommit, "predicate:native-target-e2e-passes")
    report.evidence[0].oracle_receipt = {
      control_ids: [CONTROL_ID], case_ids: [CASE_ID], recipe_source: control.recipe_source,
      result: "pass", exit_code: execution.result.exit_code, run_ref: executionRef, evidence_refs: [],
    }
    const receipt = await completeNativeTask(task, report, runner, binary)
    expect(receipt.outcome, JSON.stringify(receipt)).toBe("ok")
    const attempt = dbValue(dbPath, `SELECT lifecycle_state,readback_model FROM worker_attempts WHERE attempt_id='${packet.attempt_id}'`)
    expect(attempt.lifecycle_state).toBe("completed")
    expect(attempt.readback_model).toBe(READBACK_MODEL)
    const completedEvent = dbRows(dbPath, `SELECT payload FROM domain_events WHERE subject_id='${workID}' AND kind='worker.completed' AND json_extract(payload,'$.attempt_id')='${packet.attempt_id}'`)
    expect(completedEvent).toHaveLength(1)
    const completedPayload = JSON.parse(completedEvent[0].payload as string)
    expect(completedPayload.worker_job).toEqual(reportWorkerJob(packet))
    expect(completedPayload.evidence[0].oracle_receipt).toMatchObject({
      control_ids: [CONTROL_ID], case_ids: [CASE_ID], result: "pass", exit_code: 0, run_ref: executionRef, subject_commit: subjectCommit,
    })
    expect(completedPayload.evidence[0].oracle_receipt.recipe_source).toEqual(control.recipe_source)
    // The persisted host dispatch carried the same packet, its own signature
    // assertion, and the digest the core recorded at authorization.
    const workerDispatch = calls.find((call) => call.argv[1] === "worker-dispatch")?.input
    const workerComplete = calls.find((call) => call.argv[1] === "worker-complete")?.input
    requiredFields(workerDispatch ?? {}, ["event_id", "work_id", "attempt_id", "lane_id", "lane_digest", "packet_schema_version", "report_schema_version", "packet_digest", "host_provenance", "assertion"])
    requiredFields(workerComplete ?? {}, ["event_id", "work_id", "attempt_id", "readback_model", "report_schema_version", "evidence_origin", "evidence", "assertion"])
    expect(workerDispatch!.attempt_id).toBe(packet.attempt_id)
    expect(workerDispatch!.packet_digest).toBe(packetDigest)
    const persistedContext = ((workerDispatch!.authorized_packet as JSONRecord).inputs as JSONRecord).work_context as JSONRecord
    expect((persistedContext.oracle_preparations as JSONRecord[])[0].recipe_kind).toBe("target_runner_v1")
    expect(calls.filter((call) => call.argv[1] === "worker-dispatch")).toHaveLength(1)
    expect(calls.filter((call) => call.argv[1] === "worker-complete")).toHaveLength(1)
    expect(calls.filter((call) => call.argv[1] === "worker-fail")).toHaveLength(0)

    // This check-only job verifies the existing subject, not an editing result.
    expect((await runProcess(["git", "rev-parse", "HEAD"], "", worktree)).stdout.trim()).toBe(subjectCommit)
    // Local acceptance closes the route: the verification bind then the
    // job-bound accept satisfy the recorded job and hold repair.
    await transition("bind_evidence", "native-e2e-bind", { evidence_kind: "verification" })
    const acceptVersion = version()
    const accepted = await invoke("concord_work_transition", { operation: "workflow_action", input: { work_id: workID, expected_version: acceptVersion, action_id: "accept_worker_result", idempotency_key: "native-e2e-accept", fields: { attempt_id: packet.attempt_id, attempt_epoch: epoch } } }, context)
    expect(accepted.outcome, JSON.stringify(accepted)).toBe("ok")
    expect(dbValue(dbPath, `SELECT current_step FROM workflow_instances WHERE work_id='${workID}'`).current_step).toBe("repair")
    expect(dbValue(dbPath, `SELECT state FROM worker_job_revisions WHERE work_id='${workID}' AND job_id='${JOB_ID}'`).state).toBe("satisfied")
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    if (previousConfig === undefined) delete process.env.OPENCODE_CONFIG
    else process.env.OPENCODE_CONFIG = previousConfig
  }
}, 300_000)
