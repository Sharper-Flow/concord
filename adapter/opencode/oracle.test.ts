import { test, expect } from "bun:test"
import { createHash } from "node:crypto"
import { agentLanes } from "./generated-agent-lanes"
import { configureCoreBinary, dispatchWorker, resolveWorkerReportFromText, validateAgentLanePacket, validateAgentLaneReport, type AgentLanePacket, type CanonicalLaneReport, type DispatchRunner, type HostProvenance, type NativeOraclePreparation } from "./dispatch"
import { verifyPacketOracleReadiness } from "./oracle-readiness"

// Readiness consumes native metadata and observes clean HEAD. Report
// admission composes the raw subject OID from the authorized typed packet.

// Fake-runner suite: bind any worker-evidence CLI call to a nominal core path
// instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord-test")

const ORACLE_RECIPE_SOURCE = { kind: "repository_file" as const, project_id: "project-1", path: "testdata/oracle_recipe.json", commit_oid: `a1${"0".repeat(38)}` }
const OTHER_RECIPE_SOURCE = { kind: "repository_file" as const, project_id: "project-1", path: "testdata/second_recipe.json", commit_oid: `b2${"1".repeat(38)}` }
const ORACLE = {
  owners: [{
    owner_id: "owner:acceptance-graph",
    domain_id: "workflow-engine",
    mechanism: { project_id: "project-1", path: "internal/store/worker_jobs.go", entry_point: "DeriveWorkerJobDigest" },
    obligation: "The job digest covers every recorded content field, the oracle included.",
    predicate_ids: ["predicate:primary"],
    law_bindings: [{ source: { kind: "knowledge" as const, source_id: "records", law_id: "CD-0205", content_hash: `sha256:${"b".repeat(64)}` }, clause: "D1 lines 39-53" }],
  }],
  cases: [{
    case_id: "case:digest-covers-oracle",
    owner_id: "owner:acceptance-graph",
    entry_point: "record_worker_job",
    input_class: "one oracle-bearing revision",
    expected_state: "the digest changes when the oracle changes",
    control_ids: ["control:harness-digest", "control:harness-second"],
  }, {
    case_id: "case:second-entry-path",
    owner_id: "owner:acceptance-graph",
    entry_point: "record_worker_job (revision supersession)",
    input_class: "a second revision of the same job",
    expected_state: "the earlier obligation survives unchanged",
    control_ids: ["control:harness-second"],
  }],
  controls: [{
    control_id: "control:harness-digest",
    owner_id: "owner:acceptance-graph",
    predicate_ids: ["predicate:primary"],
    case_ids: ["case:digest-covers-oracle"],
    recipe_source: ORACLE_RECIPE_SOURCE,
    argv: ["go", "test", "-count=1", "-run", "^(TestOwnerOracleGraph)$", "."],
    cwd: ".",
    expected_result: "pass",
    required_evidence_role: "reported",
    readiness_evidence_refs: ["worktree_verify:harness-graph-prepare"],
  }, {
    control_id: "control:harness-second",
    owner_id: "owner:acceptance-graph",
    predicate_ids: ["predicate:primary"],
    case_ids: ["case:digest-covers-oracle", "case:second-entry-path"],
    recipe_source: OTHER_RECIPE_SOURCE,
    argv: ["go", "test", "-count=1", "-run", "^(TestOwnerOracleSecond)$", "."],
    cwd: ".",
    expected_result: "pass",
    required_evidence_role: "independently_executed",
    readiness_evidence_refs: ["worktree_verify:harness-second-prepare"],
  }],
}

const JOB = {
  job_id: "job:oracle-repair",
  revision: 1,
  digest: `sha256:${"c".repeat(64)}`,
  objective: "Apply the bounded repair under the owner oracle.",
  stopping_condition: "Every declared control passes on the current subject.",
  project_scope: "project-1",
  path_scope: ["internal/store"],
  predicate_ids: ["predicate:primary"],
  checks: ["go test ./internal/store -run TestOwnerOracle"],
  prerequisites: [],
  unresolved_refs: [],
  reserved_integration: "",
  acceptance_oracle: ORACLE,
}

const reviewLane = agentLanes.find((candidate) => candidate.id === "review")!
const implementLane = agentLanes.find((candidate) => candidate.id === "implement")!

function preparation(control: typeof ORACLE.controls[number]): NativeOraclePreparation {
  const digest = `sha256:${"d".repeat(64)}`
  const names = control.control_id === "control:harness-digest" ? ["TestOwnerOracleGraph"] : ["TestOwnerOracleSecond"]
  return {
    protocol: "native_oracle_v2", phase: "prepare", qualification: "ready", run_ref: control.readiness_evidence_refs[0],
    work_id: "work-oracle-1", project_id: JOB.project_scope, contract_version: 1, subject_commit: "a".repeat(40),
    bundle_digest: digest, logical_argv_digest: `sha256:${createHash("sha256").update(JSON.stringify(control.argv)).digest("hex")}`,
    logical_cwd: control.cwd, recipe_source: control.recipe_source, manifest_blob: "d".repeat(40), manifest_digest: digest,
    files: [{ path: "harness_test.go", blob_oid: "d".repeat(40), sha256: digest }],
    toolchain_identity: "go1.26.7", build_environment_digest: digest, selected_test_names: names,
    case_to_test_map: Object.fromEntries(control.case_ids.map((id) => [id, names])), selected_distinct_count: 1,
    native_plan_sha256: digest, streams_complete: true,
    stdout: { stream: "stdout", length: 0, sha256: digest, complete: true, ref: "output:stdout" },
    stderr: { stream: "stderr", length: 0, sha256: digest, complete: true, ref: "output:stderr" },
  }
}

const oraclePacket = (): AgentLanePacket => ({
  schema_version: "1.1",
  attempt_id: "attempt-oracle-1",
  lane_id: implementLane.id,
  lane_version: implementLane.version,
  lane_digest: implementLane.digest,
  work_id: "work-oracle-1",
  step_id: "implement",
  inputs: {
    task: "Deliver the bounded store change under the recorded job.",
    binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "files_touched" },
    worker_job: JOB as AgentLanePacket["inputs"]["worker_job"],
    work_context: { source_event_frontier: 0, required_reading: [], findings: [], domain_groups: [], subject_commit: "a".repeat(40), oracle_preparations: ORACLE.controls.map(preparation) },
  },
})

function reviewPacket(): AgentLanePacket {
  return {
    ...oraclePacket(),
    lane_id: reviewLane.id,
    lane_version: reviewLane.version,
    lane_digest: reviewLane.digest,
    step_id: "review",
    inputs: {
      ...oraclePacket().inputs,
      binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "contract_findings" },
    },
  } as AgentLanePacket
}

// The runner observes HEAD/status and records every argv. Any other native
// command is an error, not a fabricated preparation or worker outcome.
function gitRunner(): { runner: DispatchRunner; seen: string[][] } {
  const seen: string[][] = []
  const runner: DispatchRunner = {
    async run(argv) {
      seen.push([...argv])
      const rest = argv[0] === "git" && argv[1] === "-C" ? argv.slice(3) : argv
      if (rest[0] === "rev-parse") return { exitCode: 0, stdout: `${"a".repeat(40)}\n`, stderr: "" }
      if (argv[0] === "git" && rest[0] === "status") return { exitCode: 0, stdout: "", stderr: "" }
      throw new Error(`unexpected preauthorization command: ${argv.join(" ")}`)
    },
  }
  return { runner, seen }
}

const SIGNAL = new AbortController().signal

test("the oracle packet fixture passes the closed packet schema", () => {
  const failures: string[] = []
  const valid = validateAgentLanePacket(oraclePacket(), failures)
  expect(failures).toEqual([])
  expect(valid).toBe(true)
})

test("native case witness maps admit only bounded case keys and typed witness arrays", () => {
  for (const cases of [{}, { "not-a-case": ["TestOwnerOracleGraph"] },
    { [`case:${"x".repeat(124)}`]: ["TestOwnerOracleGraph"] }, { "case:one": ["not-a-test"] },
    { "case:one": "TestOwnerOracleGraph" },
    Object.fromEntries(Array.from({ length: 65 }, (_, index) => [`case:${index}`, ["TestOwnerOracleGraph"]]))]) {
    const packet = oraclePacket()
    packet.inputs.work_context!.oracle_preparations![0].case_to_test_map = cases as Record<string, string[]>
    const failures: string[] = []
    expect(validateAgentLanePacket(packet, failures)).toBe(false)
    expect(failures.join(" ")).toContain("oracle_preparations[0].case_to_test_map")
  }
})

test("an oracle packet consumes matching native preparations and rechecks clean HEAD", async () => {
  const { runner, seen } = gitRunner()
  const refusal = await verifyPacketOracleReadiness(oraclePacket(), { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).toBeNull()
  expect(seen.length).toBe(2)
})

test("an oracle-free packet performs no git probes and refuses nothing", async () => {
  const { runner, seen } = gitRunner()
  const legacy: AgentLanePacket = { ...oraclePacket(), inputs: { ...oraclePacket().inputs, worker_job: undefined } }
  const refusal = await verifyPacketOracleReadiness(legacy, { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).toBeNull()
  expect(seen.length).toBe(0)
})

test("a mismatched native recipe commit refuses readiness and names its control", async () => {
  const { runner } = gitRunner()
  const candidate = oraclePacket()
  candidate.inputs.work_context!.oracle_preparations![0].recipe_source = { ...ORACLE_RECIPE_SOURCE, commit_oid: "f".repeat(40) }
  const refusal = await verifyPacketOracleReadiness(candidate, { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("control:harness-digest")
  expect(refusal!.message).toContain("qualified native preparation")
})

test("a mismatched native recipe path refuses readiness", async () => {
  const { runner } = gitRunner()
  const candidate = oraclePacket()
  candidate.inputs.work_context!.oracle_preparations![0].recipe_source = { ...ORACLE_RECIPE_SOURCE, path: "wrong.json" }
  const refusal = await verifyPacketOracleReadiness(candidate, { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("control:harness-digest")
})

test("a git probe that itself fails refuses readiness with the probe outcome", async () => {
  const runner: DispatchRunner = { async run() { return { exitCode: 128, stdout: "", stderr: "fatal: not a git repository" } } }
  const refusal = await verifyPacketOracleReadiness(oraclePacket(), { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("does not match")
  expect(refusal!.message).toContain("git rev-parse exited 128")
})

test("a control without retained readiness evidence refuses readiness structurally", async () => {
  const evidenceless = { ...ORACLE, controls: [{ ...ORACLE.controls[0], readiness_evidence_refs: [] }, ORACLE.controls[1]] }
  const packet = { ...oraclePacket(), inputs: { ...oraclePacket().inputs, worker_job: { ...JOB, acceptance_oracle: evidenceless } } } as AgentLanePacket
  const { runner } = gitRunner()
  const refusal = await verifyPacketOracleReadiness(packet, { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("control:harness-digest")
  expect(refusal!.message).toContain("readiness evidence")
})

test("readiness never executes the oracle argv and never calls the core", async () => {
  const { runner, seen } = gitRunner()
  await verifyPacketOracleReadiness(oraclePacket(), { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(seen.length).toBeGreaterThan(0)
  for (const argv of seen) {
    expect(argv[0]).toBe("git")
    expect(argv.join(" ")).not.toContain("bin/oc-test")
    expect(argv.join(" ")).not.toContain("TestOwnerOracle")
  }
})

const FIXED_PROVENANCE: HostProvenance = { sources: [{ kind: "agent_definition", path: ".opencode/agents/concord-implement.md", sha256: `sha256:${"e".repeat(64)}` }], digest: `sha256:${"e".repeat(64)}` }

test("dispatch refuses an unverifiable oracle before the authorization call", async () => {
  let authorizations = 0
  const { runner, seen } = gitRunner()
  const candidate = oraclePacket()
  candidate.inputs.work_context!.oracle_preparations = []
  const result = await dispatchWorker(candidate, {
    runner,
    workerDirectory: process.cwd(),
    sessionID: "session-parent",
    provenance: FIXED_PROVENANCE,
    authorize: async () => { authorizations++; return { outcome: "ok" } },
  })
  expect(authorizations).toBe(0)
  expect(seen.length).toBe(0)
  expect(result.outcome).toBe("error")
  expect(result.error?.kind).toBe("invalid_input")
  expect(result.error?.message).toContain("acceptance oracle")
})

// The typed review block with oracle classification, resolutions, and one
// evidence entry carrying an executed receipt. The worker echo below must be
// stripped by admission: the candidate subject is dispatch-owned identity.
const oracleReportText = () => JSON.stringify({
  schema_version: "1.1",
  readback_model: "openai/gpt-5.6-luna",
  status: "completed",
  evidence: [
    { obligation: "contract_findings", detail: "the uncovered second entry path is reported as a typed finding" },
    { obligation: "verification_commands", detail: "ran both declared controls; exit 0", oracle_receipt: {
      control_ids: ["control:harness-digest", "control:harness-second"],
      case_ids: ["case:digest-covers-oracle", "case:second-entry-path"],
      subject_commit: "model-claimed-subject-echo",
      recipe_source: ORACLE_RECIPE_SOURCE,
      result: "pass",
      exit_code: 0,
      run_ref: "run:harness-dispatch-7",
      evidence_refs: ["run:harness-dispatch-7"],
    } },
  ],
  review: {
    verdict: "no_ship",
    findings: [
      { severity: "P1", confidence: "high", detail: "The second entry path still fails its control.", oracle: {
        classification: "uncovered_case",
        owner_id: "owner:acceptance-graph",
        failure_family: "owner-variant",
        predicate_ids: ["predicate:primary"],
        case_ids: ["case:second-entry-path"],
        control_ids: ["control:harness-second"],
        evidence_refs: ["run:harness-dispatch-7"],
        entry_point: "record_worker_job (revision supersession)",
      } },
    ],
    resolved_findings: [{ finding_id: "finding:19:0", evidence_refs: ["run:harness-dispatch-7"] }],
  },
  worker_job: { job_id: JOB.job_id, revision: JOB.revision, digest: JOB.digest },
})

const admittedReport = (): CanonicalLaneReport => {
  const admitted = resolveWorkerReportFromText(oracleReportText(), reviewPacket())
  if (!("report" in admitted)) throw new Error(admitted.detail)
  return admitted.report
}

test("typed oracle findings, resolutions, and receipts ride the canonical report", () => {
  const report = admittedReport()
  expect(report.review?.findings[0]?.oracle?.classification).toBe("uncovered_case")
  expect(report.review?.resolved_findings).toEqual([{ finding_id: "finding:19:0", evidence_refs: ["run:harness-dispatch-7"] }])
  const receipt = report.evidence.find((entry) => entry.oracle_receipt)?.oracle_receipt
  expect(receipt?.control_ids).toEqual(["control:harness-digest", "control:harness-second"])
  expect(receipt?.result).toBe("pass")
  expect(receipt?.exit_code).toBe(0)
})

test("the worker-echoed candidate subject is stripped from an oracle receipt", () => {
  const packet = reviewPacket()
  delete packet.inputs.work_context!.subject_commit
  const admitted = resolveWorkerReportFromText(oracleReportText(), packet)
  if (!("report" in admitted)) throw new Error(admitted.detail)
  const receipt = admitted.report.evidence.find((entry) => entry.oracle_receipt)?.oracle_receipt
  expect(receipt?.subject_commit).toBeUndefined()
})

test("the observed candidate subject from the packet's work context is injected when present", () => {
  const packet = reviewPacket() as AgentLanePacket
  ;(packet.inputs as { work_context?: unknown }).work_context = {
    source_event_frontier: 7,
    required_reading: [],
    findings: [],
    domain_groups: [],
    subject_commit: "f".repeat(40),
  }
  const admitted = resolveWorkerReportFromText(oracleReportText(), packet)
  if (!("report" in admitted)) throw new Error(admitted.detail)
  const receipt = admitted.report.evidence.find((entry) => entry.oracle_receipt)?.oracle_receipt
  expect(receipt?.subject_commit).toBe("f".repeat(40))
})

test("an executed receipt without a run locator is refused by the closed report schema", () => {
  const withoutRunRef = JSON.parse(oracleReportText())
  delete withoutRunRef.evidence[1].oracle_receipt.subject_commit
  withoutRunRef.evidence[1].oracle_receipt.run_ref = ""
  const failures: string[] = []
  expect(validateAgentLaneReport(withoutRunRef, failures)).toBe(false)
})

test("a not_run receipt carries the empty run locator and no exit code", () => {
  const notRun = JSON.parse(oracleReportText())
  delete notRun.evidence[1].oracle_receipt.subject_commit
  notRun.evidence[1].oracle_receipt.result = "not_run"
  delete notRun.evidence[1].oracle_receipt.exit_code
  notRun.evidence[1].oracle_receipt.run_ref = ""
  const failures: string[] = []
  expect(validateAgentLaneReport(notRun, failures)).toBe(true)
})
