// CD-0198 D5: the combined rejection's host launch. A reject_worker_result
// whose core result carries the derived retry dispatch routes through the one
// post-commit launch owner: the adapter arms the same single-use window the
// dispatch_worker route arms, from the core's own admitted packet bytes, and
// never calls dispatch_worker again. The coordinator then issues the one
// native foreground Task named by the directive, and the plugin's
// tool.execute.before body — windows.bind — validates that exact selector,
// overwrites the caller's arguments with the recorded packet, and consumes the
// window once, so every test here ends at the exact hook that arms the worker.
import { test, expect, mock, beforeEach, afterEach } from "bun:test"
import { mkdtemp, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { manifestDigest } from "./generated-contracts"
import { configureCoreBinary } from "./dispatch"
import { dispatchWindows, serializeLanePacket, TASK_TOOL_ID } from "./dispatch-window"
import { resetClaimedWorktrees } from "./claimed-worktree"
import { hostControlPlane } from "./move-session"
import { abandonWorkerAttempt, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"
import { agentLanes, workerScopeAssignedResult } from "./generated-agent-lanes"
import { fakeTool } from "./plugin-tool.fixture"

// The tests run against a fake runner, so bind the transport to a nominal
// core path instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord")

mock.module("@opencode-ai/plugin", () => ({ tool: fakeTool }))

const adapter = await import("./concord")

const lane = agentLanes[0]
const SESSION = "session-retry"
const REJECT_OPERATION_ID = "reject-op-1"
const RETRY_ATTEMPT = `${REJECT_OPERATION_ID}:retry`
const RETRY_DIGEST = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }

const retryPacket = () => ({
  schema_version: "2.0",
  attempt_id: RETRY_ATTEMPT,
  lane_id: lane.id,
  lane_digest: lane.digest,
  work_id: "work-retry",
  step_id: "execution",
  inputs: { task: "Retry the rejected bounded change.", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: workerScopeAssignedResult(lane.id) } },
})

let workspace = "/reject-retry-unset"
let transcriptWrites = 0

const contextFor = (directory = workspace): any => ({ sessionID: SESSION, messageID: "message-1", agent: "agent-1", worktree: directory, directory, abort: new AbortController().signal, ask: async () => { throw new Error("the adapter must not raise a permission ask") } })

const rejectCall = () => ({ request: { operation: "workflow_action", input: { work_id: "work-retry", expected_version: 8, action_id: "reject_worker_result", idempotency_key: "reject-retry-1", fields: { attempt_id: "attempt-1", diagnosis: "the bounded change failed its check", strategy: "repair the failing check", retry: { lane_id: lane.id } } } } })

const coreEnvelope = (fields: Record<string, unknown>) => ({
  schema_version: "1.0", manifest_digest: manifestDigest, request_id: `${SESSION}-message-1`, origin: "core", tool: "concord_work_transition", operation: "workflow_action", outcome: "ok", resolved_scope: null, authority: "authoritative", freshness: null, source_version_watermark: [], ordering_keys: [], next_cursor: null, omissions: [], warnings: [], evidence_refs: [], replayed: false, ...fields,
})

const okMembers = { changed_refs: [{ entity_kind: "work_item", id: "work-retry", version: "10" }], next_valid_intents: [] }

const combinedOkEnvelope = () => coreEnvelope({
  ...okMembers,
  result: {
    changed_refs: [{ entity_kind: "work_item", id: "work-retry", version: 10 }],
    next_valid_intents: [],
    operation_id: REJECT_OPERATION_ID,
    worker_packet_digest: RETRY_DIGEST,
    retry_attempt_id: RETRY_ATTEMPT,
    retry_worker_packet: retryPacket(),
    worker_worktree: workspace,
  },
})

const wallErrorEnvelope = () => coreEnvelope({
  outcome: "error",
  error: { kind: "missing_evidence", retry_safe: false, recovery_action: { kind: "provide_evidence" }, effect_state: "none", message: "record_worker_job for the rejected attempt reaches the three-attempt limit; the combined retry refuses and rejection-only stays admitted" },
})

type RunnerScript = Array<(argv: string[], stdin: string) => { exitCode: number; stdout: string; stderr: string }>
const contextAnswer = () => ({ exitCode: 0, stdout: JSON.stringify({ project_id: "project-1", product_ids: ["product-1"], scope_version: "1", main_worktree: true }), stderr: "" })

async function runTransition(script: RunnerScript, directory = workspace): Promise<{ calls: string[]; result: any }> {
  const calls: string[] = []
  let step = 0
  // The launch probes the credential it would sign worker evidence with, so
  // the adapter's configured store must be the test's: the default store
  // shells the OS Secret Service, which a test host does not answer.
  adapter.configureConcordAdapter({ runner: { async run(argv: string[], stdin: string) {
    calls.push(argv[1] ?? "context")
    const answer = step === 0 ? contextAnswer() : script[Math.min(step - 1, script.length - 1)](argv, stdin)
    step++
    return answer
  } }, credentials: testCredentials })
  const raw = await adapter.work_transition.execute(rejectCall(), contextFor(directory))
  const output = typeof raw === "string" ? raw : raw.output
  return { calls, result: JSON.parse(output.slice(0, output.indexOf("\n") === -1 ? output.length : output.indexOf("\n"))) }
}

// The invoke calls the core's `invoke` command once per operation; the ambient
// context reads around it are the adapter's own CLI plumbing. The count of
// `invoke` commands is the count of core mutations this route made.
const invokeCalls = (calls: string[]) => calls.filter(command => command === "invoke" || command === undefined || command === "")

beforeEach(async () => {
  workspace = await mkdtemp(join(tmpdir(), "reject-retry-launch-"))
  transcriptWrites = 0
  hostControlPlane().bind({
    get: async () => ({ data: { id: SESSION, directory: workspace }, response: new Response(null, { status: 200 }) }),
    // The launch queues no subtask (CD-0102 D1): every transcript write fails
    // this run, and the counter proves the route stayed at zero writes.
    post: async () => { transcriptWrites++; throw new Error("the adapter must not write a native subtask") },
  })
  resetClaimedWorktrees()
  dispatchWindows().close(SESSION)
})
afterEach(async () => {
  dispatchWindows().takeInFlight(SESSION)
  dispatchWindows().close(SESSION)
  resetClaimedWorktrees()
  adapter.configureConcordAdapter({ reset: true })
  await rm(workspace, { recursive: true, force: true })
})

// The directive's exact native foreground Task selector for the armed retry
// window: the registered lane, the recorded attempt reference, nothing else.
const exactSelector = () => ({ subagent_type: `concord-${lane.id}`, prompt: RETRY_ATTEMPT, description: "Run authorized worker" })

const bindTaskArgs = () => {
  const args: Record<string, unknown> = exactSelector()
  return { args, bind: () => dispatchWindows().bind(TASK_TOOL_ID, SESSION, args, "call-1", async () => workspace, workspace) }
}

test("a combined rejection arms one window, writes no subtask, and the exact native selector consumes the core's own packet", async () => {
  const { calls, result } = await runTransition([() => ({ exitCode: 0, stdout: JSON.stringify(combinedOkEnvelope()), stderr: "" })])
  // The rejection rode the one workflow_action call; no dispatch_worker
  // authorization ran again.
  expect(invokeCalls(calls)).toHaveLength(1)
  expect(result.outcome).toBe("ok")
  expect(result.dispatch_state).toBe("awaiting_worker")
  expect(transcriptWrites).toBe(0)
  // The serialized directive is authorization, not launch: it instructs the
  // coordinator to issue one native foreground Task with the exact selector.
  const selector = exactSelector()
  expect(result.output).toContain("Authorized only")
  expect(result.output).toContain("launches no worker")
  expect(result.output).toContain("one native foreground Task")
  expect(result.output).toContain(`exactly ${JSON.stringify(selector)}`)
  expect(JSON.parse(result.output.slice(result.output.indexOf("{"), result.output.lastIndexOf("}") + 1))).toEqual(selector)
  // The receipt preserves the committed rejection's mutation receipt beside
  // the Task directive: the rejection's changed_refs and next intents under
  // their core names, and the core result receipt with the retry identity,
  // digest, and claimed worktree. The packet itself stays off the receipt:
  // the launch consumed those exact bytes into the armed window.
  expect(result.changed_refs).toEqual(okMembers.changed_refs)
  expect(result.next_valid_intents).toEqual([])
  expect(result.result?.operation_id).toBe(REJECT_OPERATION_ID)
  expect(result.result?.retry_attempt_id).toBe(RETRY_ATTEMPT)
  expect(result.result?.worker_packet_digest).toBe(RETRY_DIGEST)
  expect(result.result?.worker_worktree).toBe(workspace)
  expect(result.result?.retry_worker_packet).toBeUndefined()
  expect(dispatchWindows().has(SESSION)).toBe(true)
  // The coordinator issues the exact selector from the directive; the window
  // binds the Task call to the retry packet, overwrites the caller's
  // arguments, and consumes itself once.
  const { args, bind } = bindTaskArgs()
  await bind()
  expect(args.subagent_type).toBe(`concord-${lane.id}`)
  expect(args.prompt).toBe(serializeLanePacket(retryPacket() as any))
  expect(args.description).toContain(RETRY_ATTEMPT)
  expect(dispatchWindows().has(SESSION)).toBe(false)
  await expect(bindTaskArgs().bind()).rejects.toThrow("no authorized dispatch window")
})

test("a replayed combined response never launches twice and names the exact original identity", async () => {
  const answer = () => ({ exitCode: 0, stdout: JSON.stringify(combinedOkEnvelope()), stderr: "" })
  const first = await runTransition([answer])
  expect(first.result.dispatch_state).toBe("awaiting_worker")
  expect(transcriptWrites).toBe(0)
  expect(dispatchWindows().has(SESSION)).toBe(true)
  // The first response was lost after the window armed; the coordinator
  // reconciles and the combined response replays. The open window must not
  // be duplicated and the committed attempt identity must surface.
  const replay = await runTransition([() => ({ exitCode: 0, stdout: JSON.stringify({ ...combinedOkEnvelope(), replayed: true }), stderr: "" })])
  expect(replay.result.outcome).toBe("error")
  expect(transcriptWrites).toBe(0)
  expect(replay.result.error?.message).toContain(RETRY_ATTEMPT)
  expect(dispatchWindows().has(SESSION)).toBe(true)
  // The window the first response armed is still the original one: the next
  // Task call consumes the original packet, not a rebuilt one.
  const { args, bind } = bindTaskArgs()
  await bind()
  expect(args.prompt).toBe(serializeLanePacket(retryPacket() as any))
  // Once the host is running the worker, a further replay names the in-flight
  // attempt and cannot launch a second worker.
  const third = await runTransition([() => ({ exitCode: 0, stdout: JSON.stringify({ ...combinedOkEnvelope(), replayed: true }), stderr: "" })])
  expect(third.result.outcome).toBe("error")
  expect(transcriptWrites).toBe(0)
  expect(third.result.error?.message).toContain(RETRY_ATTEMPT)
  expect(dispatchWindows().has(SESSION)).toBe(false)
})

test.each(["open", "in-flight"] as const)("a replay cannot recreate a lost %s retry authorization", async state => {
  const first = await runTransition([() => ({ exitCode: 0, stdout: JSON.stringify(combinedOkEnvelope()), stderr: "" })])
  expect(first.result.dispatch_state).toBe("awaiting_worker")
  if (state === "in-flight") await bindTaskArgs().bind()
  dispatchWindows().close(SESSION)
  dispatchWindows().takeInFlight(SESSION)
  const replay = await runTransition([() => ({ exitCode: 0, stdout: JSON.stringify({ ...combinedOkEnvelope(), replayed: true }), stderr: "" })])
  expect(replay.result.outcome).toBe("error")
  expect(replay.result.error?.kind).toBe("unauthorized_dispatch")
  expect(replay.result.error?.retry_safe).toBe(false)
  expect(replay.result.error?.message).toContain(RETRY_ATTEMPT)
  expect(replay.result.dispatch_state).toBeUndefined()
  expect(replay.result.output).toBeUndefined()
  expect(replay.result.changed_refs).toEqual(okMembers.changed_refs)
  expect(replay.result.result?.retry_attempt_id).toBe(RETRY_ATTEMPT)
  expect(replay.result.result?.retry_worker_packet).toBeUndefined()
  expect(dispatchWindows().has(SESSION)).toBe(false)
  expect(dispatchWindows().inFlightAttempt(SESSION)).toBeNull()
  await expect(bindTaskArgs().bind()).rejects.toThrow("no authorized dispatch window")
})

test("stale, full-packet, wrong-lane, and execution-control seeds leave the armed retry window usable; the matching selector binds the core's packet once", async () => {
  const { result } = await runTransition([() => ({ exitCode: 0, stdout: JSON.stringify(combinedOkEnvelope()), stderr: "" })])
  expect(result.outcome).toBe("ok")
  expect(transcriptWrites).toBe(0)
  const otherLane = agentLanes.find((candidate) => candidate.id !== lane.id)!
  const seeds: Array<[string, Record<string, unknown>]> = [
    ["stale attempt reference", { ...exactSelector(), prompt: "attempt-stale" }],
    ["full queued packet as prompt", { ...exactSelector(), prompt: serializeLanePacket(retryPacket() as any) }],
    ["wrong lane", { ...exactSelector(), subagent_type: `concord-${otherLane.id}` }],
    ["resume control field", { ...exactSelector(), task_id: "resumed-session" }],
    ["background control field", { ...exactSelector(), background: true }],
    ["command control field", { ...exactSelector(), command: "opencode run" }],
  ]
  for (const [name, seed] of seeds) {
    await expect(dispatchWindows().bind(TASK_TOOL_ID, SESSION, { ...seed }, "seed-call", async () => workspace, workspace), name).rejects.toThrow("requires the exact authorized lane and attempt reference")
    expect(dispatchWindows().has(SESSION), name).toBe(true)
  }
  const { args, bind } = bindTaskArgs()
  await bind()
  expect(args.prompt).toBe(serializeLanePacket(retryPacket() as any))
  expect(args.subagent_type).toBe(`concord-${lane.id}`)
  expect(dispatchWindows().has(SESSION)).toBe(false)
})

test("a native denial after binding retains the consumed attempt for exactly one abandonment", async () => {
  const { result } = await runTransition([() => ({ exitCode: 0, stdout: JSON.stringify(combinedOkEnvelope()), stderr: "" })])
  expect(result.outcome).toBe("ok")
  // Native permission denial lands after the binding hook but before the
  // child exists (CD-0102 D5): the selector consumed the window into a
  // retained in-flight record and no completion hook will run for it.
  const { bind } = bindTaskArgs()
  await bind()
  const retained = dispatchWindows().inFlightAttempt(SESSION)
  expect(retained?.packet.attempt_id).toBe(RETRY_ATTEMPT)
  expect(retained?.packetDigest).toBe(RETRY_DIGEST)
  // The existing no-report recovery closes exactly the retained identity and
  // releases it once; the release requires the matching attempt and lane.
  const abandonments: Array<{ command: string; attempt_id: string }> = []
  const evidenceRunner: DispatchRunner = { async run(argv, stdin) {
    if (argv[1] === "worker-abandon") {
      const body = JSON.parse(stdin)
      abandonments.push({ command: argv[1], attempt_id: body.attempt_id })
    }
    return { exitCode: 0, stdout: "", stderr: "" }
  } }
  const abandonment = await abandonWorkerAttempt(lane, { work_id: "work-retry", attempt_id: RETRY_ATTEMPT }, "the native Task was denied before its child started; no worker report exists", { credentials: testCredentials, evidenceRunner }, new AbortController().signal, () => { expect(dispatchWindows().releaseRetained(SESSION, RETRY_ATTEMPT, lane.id)).toBe(true) })
  expect(abandonment.envelope.outcome).toBe("error")
  expect(abandonment.committed).toBe(true)
  expect(abandonments).toEqual([{ command: "worker-abandon", attempt_id: RETRY_ATTEMPT }])
  expect(dispatchWindows().inFlightAttempt(SESSION)).toBeNull()
  // The released attempt cannot authorize another Task, and a mismatched
  // release identity never touches a fresh record.
  expect(dispatchWindows().releaseRetained(SESSION, RETRY_ATTEMPT, lane.id)).toBe(false)
})

test("a failed launch keeps the committed attempt truthful for one abandonment", async () => {
  // The calling tool context sits outside the claimed worktree the core
  // named, so the launch gate refuses after the core committed.
  const outside = await mkdtemp(join(tmpdir(), "reject-retry-outside-"))
  try {
    const { result } = await runTransition([() => ({ exitCode: 0, stdout: JSON.stringify(combinedOkEnvelope()), stderr: "" })], outside)
    expect(result.outcome).toBe("error")
    expect(result.error?.details?.retry_attempt_id).toBe(RETRY_ATTEMPT)
    expect(result.error?.details?.effect_state).toBe("possible")
    expect(result.error?.message).toContain(RETRY_ATTEMPT)
    expect(result.error?.message).toMatch(/worker.abandon|abandon/)
    // The post-commit refusal preserves the rejection receipt too: the
    // rejection committed even though the window never armed.
    expect(result.changed_refs).toEqual(okMembers.changed_refs)
    expect(result.result?.retry_attempt_id).toBe(RETRY_ATTEMPT)
    expect(dispatchWindows().has(SESSION)).toBe(false)
    // The coordinator abandons exactly the committed identity; the terminal
    // seam records one abandonment and the receipt names the same attempt.
    const abandonments: Array<{ command: string; attempt_id: string }> = []
    const evidenceRunner: DispatchRunner = { async run(argv, stdin) {
      if (argv[1] === "worker-abandon") {
        const body = JSON.parse(stdin)
        abandonments.push({ command: argv[1], attempt_id: body.attempt_id })
      }
      return { exitCode: 0, stdout: "", stderr: "" }
    } }
    const abandonment = await abandonWorkerAttempt(lane, { work_id: "work-retry", attempt_id: RETRY_ATTEMPT }, "the launch window never opened; the committed retry attempt closes with no report", { credentials: testCredentials, evidenceRunner }, new AbortController().signal)
    expect(abandonment.envelope.outcome).toBe("error")
    expect(abandonments).toHaveLength(1)
    expect(abandonments[0].attempt_id).toBe(RETRY_ATTEMPT)
  } finally {
    await rm(outside, { recursive: true, force: true })
  }
})

test("a wall refusal flows through untouched with no window and no host Task", async () => {
  const { calls, result } = await runTransition([() => ({ exitCode: 0, stdout: JSON.stringify(wallErrorEnvelope()), stderr: "" })])
  expect(invokeCalls(calls)).toHaveLength(1)
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("missing_evidence")
  expect(result.error?.message).toContain("three-attempt limit")
  expect(result.origin).toBe("core")
  // No launch: the session holds no window, so the next Task call cannot
  // spawn a worker.
  expect(dispatchWindows().has(SESSION)).toBe(false)
  const { bind } = bindTaskArgs()
  await expect(bind()).rejects.toThrow(/no authorized dispatch window/)
})

test("a rejection-only response stays the core's own envelope and opens nothing", async () => {
  const rejectionOnly = coreEnvelope({
    ...okMembers,
    result: {
      changed_refs: [{ entity_kind: "work_item", id: "work-retry", version: 10 }],
      next_valid_intents: [],
      operation_id: REJECT_OPERATION_ID,
    },
  })
  const { calls, result } = await runTransition([() => ({ exitCode: 0, stdout: JSON.stringify(rejectionOnly), stderr: "" })])
  expect(invokeCalls(calls)).toHaveLength(1)
  expect(result.outcome).toBe("ok")
  expect(result.result?.operation_id).toBe(REJECT_OPERATION_ID)
  expect(result.dispatch_state).toBeUndefined()
  expect(dispatchWindows().has(SESSION)).toBe(false)
})
