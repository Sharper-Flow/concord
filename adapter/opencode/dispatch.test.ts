import { test, expect } from "bun:test"
import { agentLanes, routingPolicies, routingPolicyManifestDigest, routingPolicyVersion } from "./generated-agent-lanes"
import { dispatchWorker, preferredModelForLane, readExportSessionMetadata, readRunSessionMetadata, resetRoutingPolicyForTesting, validateAgentLanePacket, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import type { CredentialStore } from "./credentials"

// The adapter signs worker evidence with its registered client key (CD-0044).
// Tests supply a deterministic seed so dispatch does not reach the host
// credential service.
const testCredentials: CredentialStore = { async getPrivateKey() { return new Uint8Array(32).fill(7) } }

const lane = agentLanes[0]
const packet = (): AgentLanePacket => ({
  schema_version: "1.0",
  attempt_id: "attempt-1",
  lane_id: lane.id,
  lane_version: lane.version,
  lane_digest: lane.digest,
  work_id: "work-1",
  step_id: "step-1",
  inputs: { task: "Run the bounded worker fixture." },
})

const runOutput = (extra = "") => [
  JSON.stringify({ type: "step_start", timestamp: 1, sessionID: "session-1", part: { type: "step-start" } }),
  JSON.stringify({ type: "step_finish", timestamp: 2, sessionID: "session-1", part: { type: "step-finish", reason: "stop" } }),
  extra,
].filter(Boolean).join("\n")

const availableModels = () => routingPolicies.flatMap(policy => policy.resolution_set).join("\n")

const exportedSession = (model = preferredModelForLane(lane)) => JSON.stringify({
  info: { id: "session-1" },
  messages: [{ info: { id: "message-1", sessionID: "session-1", role: "assistant", providerID: model.split("/")[0], modelID: model.split("/").slice(1).join("/"), time: { created: 1 } }, parts: [] }],
})

const workerRunner = (model = preferredModelForLane(lane), output = runOutput()): DispatchRunner => ({
  async run(argv) {
    if (argv[1] === "models") return { exitCode: 0, stdout: availableModels(), stderr: "" }
    if (argv[1] === "run") return { exitCode: 0, stdout: output, stderr: "" }
    if (argv[1] === "export") return { exitCode: 0, stdout: exportedSession(model), stderr: "" }
    return { exitCode: 0, stdout: "", stderr: "" }
  },
})

test("generated adapter routing policy digest and preferred cross-validation are deterministic", async () => {
  const digest = (await Bun.file(new URL("../../contracts/routing-policy.digest", import.meta.url)).text()).trim()
  expect(digest).toBe(routingPolicyManifestDigest)
  expect(routingPolicyVersion).toBe("routing-v1")
  for (const policy of routingPolicies) {
    expect(policy.resolution_set[0]).toBe(policy.preferred_model)
    expect(policy.resolution_set.length).toBeGreaterThanOrEqual(1)
  }
})

function hostPolicy(preferred = "host/preferred", fallback = "host/fallback") {
  return { schema_version: "1.0", registry: "routing_policy", version: "routing-v1", policies: routingPolicies.map(policy => ({ capability_class: policy.capability_class, preferred_model: preferred, resolution_set: [preferred, fallback] })) }
}

test("host routing policy takes precedence and supplies the dispatch model", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "routing-policy-"))
  const policyPath = `${dir}/routing-policy.json`
  await Bun.write(policyPath, JSON.stringify(hostPolicy()))
  process.env.CONCORD_ROUTING_POLICY = policyPath
  resetRoutingPolicyForTesting()
  let argv: string[] = []
  const runner: DispatchRunner = { async run(args) {
    if (args[1] === "models") return { exitCode: 0, stdout: "host/preferred\nhost/fallback", stderr: "" }
    if (args[1] === "run") { argv = args; return { exitCode: 0, stdout: runOutput(), stderr: "" } }
    return { exitCode: 0, stdout: exportedSession("host/preferred"), stderr: "" }
  } }
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner })
  expect(result.outcome).toBe("ok")
  expect(argv).toContain("host/preferred")
  expect(result.routing_policy_digest).not.toBe(routingPolicyManifestDigest)
  delete process.env.CONCORD_ROUTING_POLICY
  resetRoutingPolicyForTesting()
})

test("host policy names every unavailable model and its source", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "routing-policy-missing-"))
  const policyPath = `${dir}/routing-policy.json`
  await Bun.write(policyPath, JSON.stringify(hostPolicy("host/missing", "host/fallback")))
  process.env.CONCORD_ROUTING_POLICY = policyPath
  resetRoutingPolicyForTesting()
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: { async run(args) {
    if (args[1] === "models") return { exitCode: 0, stdout: "host/other", stderr: "" }
    throw new Error("must not dispatch")
  } } })
  expect(result.error?.kind).toBe("routing_policy_model_unavailable")
  expect(result.error?.message).toContain(policyPath)
  expect(result.error?.message).toContain("host/missing")
  expect(result.error?.message).toContain("host/fallback")
  delete process.env.CONCORD_ROUTING_POLICY
  resetRoutingPolicyForTesting()
})

test("policy preferred model must be the first resolution member", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "routing-policy-anchor-"))
  const policyPath = `${dir}/routing-policy.json`
  const policy = hostPolicy()
  policy.policies[0].resolution_set = ["host/fallback", "host/preferred"]
  await Bun.write(policyPath, JSON.stringify(policy))
  process.env.CONCORD_ROUTING_POLICY = policyPath
  resetRoutingPolicyForTesting()
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: { async run() { throw new Error("must not dispatch") } } })
  expect(result.error?.kind).toBe("routing_policy_invalid")
  expect(result.error?.message).toContain("preferred_model")
  delete process.env.CONCORD_ROUTING_POLICY
  resetRoutingPolicyForTesting()
})

test("packet validation is closed before any runner call", async () => {
  let calls = 0
  const invalid = { ...packet(), inputs: { task: "" } }
  expect(validateAgentLanePacket(invalid)).toBe(false)
  const result = await dispatchWorker(invalid, { credentials: testCredentials, runner: { async run() { calls++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } } })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(calls).toBe(0)
})

test("unknown lane identity fails closed before spawn", async () => {
  const unknown = { ...packet(), lane_id: "unknown" }
  const result = await dispatchWorker(unknown, { credentials: testCredentials, runner: { async run() { throw new Error("must not spawn") } } })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
})

test("spawn failure is a typed blocked outcome with bounded diagnostic", async () => {
  let argv: string[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials, binary: "opencode-test", runner: { async run(args) { if (args[1] === "models") return { exitCode: 0, stdout: availableModels(), stderr: "" }; argv = args; return { exitCode: 1, stdout: "", stderr: "provider unavailable", fallbackExhausted: true } } } })
  expect(result.outcome).toBe("blocked")
  expect(result.error?.kind).toBe("blocked")
  expect(argv).toEqual(["opencode-test", "run", "--agent", "concord-research", "--model", preferredModelForLane(lane), "--format", "json", JSON.stringify(packet())])
})

test("spawn failure without exhaustion evidence is not mislabeled blocked", async () => {
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: { async run(args) { if (args[1] === "models") return { exitCode: 0, stdout: availableModels(), stderr: "" }; return { exitCode: 1, stdout: "", stderr: "provider unavailable" } } } })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
})

test("recorded session metadata proves the executing model and exposes fallback", async () => {
  const fallback = "zai-coding-plan/glm-5.3"
  const fallbackOutput = runOutput(JSON.stringify({ type: "message.updated", properties: { sessionId: "session-1", status: { action: { reason: "account_rate_limit" } } } }))
  expect(readRunSessionMetadata(fallbackOutput)).toEqual({ session_id: "session-1", fallback_reason: "rate_limit" })
  expect(readExportSessionMetadata(exportedSession(fallback), "session-1")).toEqual({ readback_model: fallback, session_id: "session-1" })
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: workerRunner(fallback, fallbackOutput) })
  expect(result.outcome).toBe("fallback")
  expect(result.readback_model).toBe(fallback)
  expect(result.error?.kind).toBe("fallback")
  expect(result.resolution_role).toBe("fallback")
  expect(result.fallback_reason).toBe("rate_limit")
})

test("matching recorded session metadata returns bounded ok envelope", async () => {
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: workerRunner() })
  expect(result.outcome).toBe("ok")
  expect(result.agent).toBe("concord-research")
  expect(result.resolved_model).toBe(preferredModelForLane(lane))
  expect(result.resolution_role).toBe("preferred")
  expect(result.fallback_reason).toBe("")
  expect(result.readback_model).toBe(preferredModelForLane(lane))
  expect(result.session_id).toBe("session-1")
})

test("dispatch obtains readback from a sanitized session export", async () => {
  const calls: string[][] = []
  const base = workerRunner()
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: { async run(argv, input, signal) { calls.push(argv); return base.run(argv, input, signal) } },
    evidenceRunner: { async run() { return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("ok")
  expect(calls.map((argv) => argv.slice(0, 2))).toEqual([["opencode", "models"], ["opencode", "run"], ["opencode", "export"]])
  expect(calls[2]).toEqual(["opencode", "export", "session-1", "--sanitize"])
})

test("undeclared readback is rejected instead of becoming an implicit fallback", async () => {
  const result = await dispatchWorker(packet(), { credentials: testCredentials, runner: workerRunner("openai/not-declared") })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("model_identity_mismatch")
})

test("a successful run records dispatch evidence before completion evidence", async () => {
  const calls: { argv: string[]; input: string }[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    concordBinary: "concord-test",
    runner: workerRunner(),
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("ok")
  expect(calls.map((call) => call.argv)).toEqual([["concord-test", "worker-dispatch"], ["concord-test", "worker-complete"]])

  const dispatched = JSON.parse(calls[0].input)
  expect(dispatched.work_id).toBe("work-1")
  expect(dispatched.attempt_id).toBe("attempt-1")
  expect(dispatched.lane_id).toBe(lane.id)
  expect(dispatched.lane_version).toBe(lane.version)
  expect(dispatched.lane_digest).toBe(lane.digest)
  expect(dispatched.routing_policy_version).toBe(routingPolicyVersion)
  expect(dispatched.routing_policy_digest).toBe(routingPolicyManifestDigest)
  expect(dispatched.resolved_model).toBe(preferredModelForLane(lane))
  expect(dispatched.resolution_role).toBe("preferred")
  expect(dispatched.fallback_reason).toBe("")
  expect(dispatched.packet_schema_version).toBe("1.0")
  expect(dispatched.report_schema_version).toBe("1.0")
  expect(typeof dispatched.event_id).toBe("string")

  const completed = JSON.parse(calls[1].input)
  expect(completed.work_id).toBe("work-1")
  expect(completed.attempt_id).toBe("attempt-1")
  expect(completed.readback_model).toBe(preferredModelForLane(lane))
  expect(completed.report_schema_version).toBe("1.0")
  expect(completed.event_id).not.toBe(dispatched.event_id)
})

test("a declared fallback is recorded as fallback evidence, not as a failure", async () => {
  const fallbackOutput = runOutput(JSON.stringify({ type: "message.updated", properties: { sessionId: "session-1", status: { action: { reason: "account_rate_limit" } } } }))
  const calls: { argv: string[]; input: string }[] = []
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: workerRunner("zai-coding-plan/glm-5.3", fallbackOutput),
    evidenceRunner: { async run(argv, input) { calls.push({ argv, input }); return { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(result.outcome).toBe("fallback")
  expect(calls).toHaveLength(2)
  const dispatched = JSON.parse(calls[0].input)
  const completed = JSON.parse(calls[1].input)
  expect(dispatched.resolved_model).toBe("zai-coding-plan/glm-5.3")
  expect(dispatched.resolution_role).toBe("fallback")
  expect(dispatched.fallback_reason).toBe("rate_limit")
  expect(completed.readback_model).toBe(dispatched.resolved_model)
})

test("a run whose evidence cannot be recorded is not reported as a success", async () => {
  const refuse = async (argv: string[]) => argv[1] === "worker-dispatch" ? { exitCode: 1, stdout: "", stderr: "resolved model is not a declared routing-policy member" } : { exitCode: 0, stdout: "", stderr: "" }
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: workerRunner(),
    evidenceRunner: { async run(argv) { return refuse(argv) } },
  })
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("error")
  expect(result.error?.recovery_action).toBe("reconcile_operation")
  expect(result.error?.message).toBe("resolved model is not a declared routing-policy member")
})

test("a completion that cannot be recorded is not reported as a success", async () => {
  let recorded = 0
  const result = await dispatchWorker(packet(), { credentials: testCredentials,
    runner: workerRunner(),
    evidenceRunner: { async run(argv) { recorded++; return argv[1] === "worker-complete" ? { exitCode: 1, stdout: "", stderr: "worker attempt belongs to a different work item" } : { exitCode: 0, stdout: "", stderr: "" } } },
  })
  expect(recorded).toBe(2)
  expect(result.outcome).toBe("error")
  expect(result.error?.message).toBe("worker attempt belongs to a different work item")
})

test("generic host agents are not dispatchable and never spawn or record", async () => {
  for (const generic of ["general", "explore", "build", "plan"]) {
    let spawned = 0
    let recorded = 0
    const result = await dispatchWorker({ ...packet(), lane_id: generic }, { credentials: testCredentials,
      runner: { async run() { spawned++; return { exitCode: 0, stdout: runOutput(), stderr: "" } } },
      evidenceRunner: { async run() { recorded++; return { exitCode: 0, stdout: "", stderr: "" } } },
    })
    expect(result.outcome).toBe("error")
    expect(result.error?.kind).toBe("invalid_input")
    expect(spawned).toBe(0)
    expect(recorded).toBe(0)
  }
})

test("the registered lane set is closed and every agent name is Concord-owned", () => {
  expect(agentLanes.map((entry) => entry.id).sort()).toEqual(["implement", "research", "review", "verify"])
  for (const entry of agentLanes) expect(`concord-${entry.id}`).toMatch(/^concord-[a-z]+$/)
})

// CD-0032 / issue #103: provenance is deterministic for the same inputs and
// changes when an enumerated source changes.
import { computeHostPromptProvenance } from "./dispatch"
import { mkdtemp } from "node:fs/promises"
import * as path from "node:path"
import * as os from "node:os"

test("host prompt provenance is deterministic and content-bound", async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), "provenance-"))
  await Bun.write(`${dir}/AGENTS.md`, "# instructions v1\n")
  const first = await computeHostPromptProvenance("research", dir)
  const second = await computeHostPromptProvenance("research", dir)
  expect(first.digest).toBe(second.digest)
  expect(first.digest).toMatch(/^sha256:[0-9a-f]{64}$/)
  const agentsMd = first.sources.find((s) => s.kind === "agents_md")
  expect(agentsMd?.path).toBe(`${dir}/AGENTS.md`)
  expect(agentsMd?.sha256).toMatch(/^sha256:/)
  expect(first.sources.filter((s) => s.kind === "unenumerated").length).toBeGreaterThan(0)
  await Bun.write(`${dir}/AGENTS.md`, "# instructions v2 — silently changed\n")
  const changed = await computeHostPromptProvenance("research", dir)
  expect(changed.digest).not.toBe(first.digest)
})
