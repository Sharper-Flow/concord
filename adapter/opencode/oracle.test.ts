import { test, expect } from "bun:test"
import { agentLanes } from "./generated-agent-lanes"
import { configureCoreBinary, dispatchWorker, resolveWorkerReportFromText, validateAgentLanePacket, validateAgentLaneReport, type AgentLanePacket, type CanonicalLaneReport, type DispatchRunner, type HostProvenance } from "./dispatch"
import { verifyPacketOracleReadiness } from "./oracle-readiness"

// CON-890 slice A, adapter side. Three focused groups:
//
// 1. Predispatch readiness: the pinned harness sources of a packet's
//    acceptance oracle must resolve as exact Git objects and paths in the
//    repository the dispatch can reach, the retained readiness evidence must
//    be present per control, and the check never executes the oracle's own
//    argv and never calls the core.
// 2. Dispatch wiring: the readiness refusal fires before the authorization
//    call, so no authorized attempt persists for an unverifiable oracle.
// 3. Terminal report admission: typed oracle findings, resolutions, and
//    receipts ride the canonical report, while the dispatch-owned candidate
//    subject is stripped from worker echo and injected only from the packet.

// Fake-runner suite: bind any worker-evidence CLI call to a nominal core path
// instead of the unstamped repository placeholder (CD-0111 D1).
configureCoreBinary("concord-test")

const ORACLE_RECIPE_SOURCE = { kind: "repository_file", project_id: "project-1", path: "internal/store/worker_oracle_harness_test.go", commit_oid: `a1${"0".repeat(38)}` }
const OTHER_RECIPE_SOURCE = { kind: "repository_file", project_id: "project-1", path: "scripts/harness_second_test.go", commit_oid: `b2${"1".repeat(38)}` }
const ORACLE = {
  owners: [{
    owner_id: "owner:acceptance-graph",
    domain_id: "workflow-engine",
    mechanism: { project_id: "project-1", path: "internal/store/worker_jobs.go", entry_point: "DeriveWorkerJobDigest" },
    obligation: "The job digest covers every recorded content field, the oracle included.",
    predicate_ids: ["predicate:primary"],
    law_bindings: [{ source: { kind: "knowledge", source_id: "records", law_id: "CD-0205", content_hash: `sha256:${"b".repeat(64)}` }, clause: "D1 lines 39-53" }],
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
    argv: ["bin/oc-test", "targeted", "--", "go", "test", "./internal/store", "-run", "TestOwnerOracleGraph"],
    cwd: ".",
    expected_result: "pass",
    required_evidence_role: "reported",
    readiness_evidence_refs: ["run:harness-selftest"],
  }, {
    control_id: "control:harness-second",
    owner_id: "owner:acceptance-graph",
    predicate_ids: ["predicate:primary"],
    case_ids: ["case:digest-covers-oracle", "case:second-entry-path"],
    recipe_source: OTHER_RECIPE_SOURCE,
    argv: ["go", "test", "./internal/store", "-run", "TestOwnerOracleSecond"],
    cwd: ".",
    expected_result: "pass",
    required_evidence_role: "independently_executed",
    readiness_evidence_refs: ["run:harness-selftest", "run:harness-selector-proof"],
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
    work_context: { source_event_frontier: 0, required_reading: [], findings: [], domain_groups: [], candidate_subject: `commit:${"a".repeat(40)}` },
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

// The scripted runner answers git object probes by shape and records every
// argv it saw, so the tests can prove the oracle's own argv never executes.
function gitRunner(outcomes: { [commit: string]: { commitExists: boolean; paths?: { [path: string]: boolean } } } = {}): { runner: DispatchRunner; seen: string[][] } {
  const seen: string[][] = []
  const runner: DispatchRunner = {
    async run(argv) {
      seen.push([...argv])
      const rest = argv[0] === "git" && argv[1] === "-C" ? argv.slice(3) : argv
      if (argv[0] !== "git") return { exitCode: 0, stdout: "", stderr: "" }
      if (rest[0] === "rev-parse") return { exitCode: 0, stdout: `${"a".repeat(40)}\n`, stderr: "" }
      if (rest[0] === "cat-file") {
        const oid = rest[2]?.replace(/\^\{commit\}$/, "") ?? ""
        const outcome = outcomes[oid]
        return outcome && outcome.commitExists ? { exitCode: 0, stdout: "", stderr: "" } : { exitCode: 1, stdout: "", stderr: `fatal: not a valid object name ${oid}` }
      }
      if (rest[0] === "ls-tree") {
        const oid = rest[2] ?? ""
        const pathSpec = rest[rest.indexOf("--") + 1] ?? ""
        const outcome = outcomes[oid]
        const present = outcome?.commitExists && outcome.paths?.[pathSpec] === true
        return { exitCode: 0, stdout: present ? `100644 blob ${"d".repeat(40)}\t${pathSpec}\n` : "", stderr: "" }
      }
      return { exitCode: 0, stdout: "", stderr: "" }
    },
  }
  return { runner, seen }
}

const RESOLVED_SOURCES = {
  [ORACLE_RECIPE_SOURCE.commit_oid]: { commitExists: true, paths: { [ORACLE_RECIPE_SOURCE.path]: true } },
  [OTHER_RECIPE_SOURCE.commit_oid]: { commitExists: true, paths: { [OTHER_RECIPE_SOURCE.path]: true } },
}

const SIGNAL = new AbortController().signal

test("the oracle packet fixture passes the closed packet schema", () => {
  const failures: string[] = []
  const valid = validateAgentLanePacket(oraclePacket(), failures)
  expect(failures).toEqual([])
  expect(valid).toBe(true)
})

test("an oracle packet passes readiness when every pinned source resolves as an exact Git object and path", async () => {
  const { runner, seen } = gitRunner(RESOLVED_SOURCES)
  const refusal = await verifyPacketOracleReadiness(oraclePacket(), { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).toBeNull()
  // One probe pair per unique pinned source, never one per control.
  expect(seen.length).toBe(6)
})

test("an oracle-free packet performs no git probes and refuses nothing", async () => {
  const { runner, seen } = gitRunner(RESOLVED_SOURCES)
  const legacy: AgentLanePacket = { ...oraclePacket(), inputs: { ...oraclePacket().inputs, worker_job: undefined } }
  const refusal = await verifyPacketOracleReadiness(legacy, { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).toBeNull()
  expect(seen.length).toBe(0)
})

test("an absent pinned commit object refuses readiness and names the control and commit", async () => {
  const { runner } = gitRunner({ [ORACLE_RECIPE_SOURCE.commit_oid]: { commitExists: false, paths: {} }, [OTHER_RECIPE_SOURCE.commit_oid]: RESOLVED_SOURCES[OTHER_RECIPE_SOURCE.commit_oid] })
  const refusal = await verifyPacketOracleReadiness(oraclePacket(), { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("control:harness-digest")
  expect(refusal!.message).toContain(ORACLE_RECIPE_SOURCE.commit_oid)
})

test("a pinned path absent from the commit tree refuses readiness", async () => {
  const { runner } = gitRunner({
    [ORACLE_RECIPE_SOURCE.commit_oid]: { commitExists: true, paths: {} },
    [OTHER_RECIPE_SOURCE.commit_oid]: RESOLVED_SOURCES[OTHER_RECIPE_SOURCE.commit_oid],
  })
  const refusal = await verifyPacketOracleReadiness(oraclePacket(), { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("internal/store/worker_oracle_harness_test.go")
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
  const { runner } = gitRunner(RESOLVED_SOURCES)
  const refusal = await verifyPacketOracleReadiness(packet, { runner, directory: "/claimed/worktree", signal: SIGNAL })
  expect(refusal).not.toBeNull()
  expect(refusal!.message).toContain("control:harness-digest")
  expect(refusal!.message).toContain("readiness evidence")
})

test("readiness never executes the oracle argv and never calls the core", async () => {
  const { runner, seen } = gitRunner(RESOLVED_SOURCES)
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
  const { runner, seen } = gitRunner({}) // no pinned source resolves
  const result = await dispatchWorker(oraclePacket(), {
    runner,
    workerDirectory: process.cwd(),
    sessionID: "session-parent",
    provenance: FIXED_PROVENANCE,
    authorize: async () => { authorizations++; return { outcome: "ok" } },
  })
  expect(authorizations).toBe(0)
  expect(seen.length).toBeGreaterThan(0)
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
      candidate_subject: "model-claimed-subject-echo",
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
  delete packet.inputs.work_context!.candidate_subject
  const admitted = resolveWorkerReportFromText(oracleReportText(), packet)
  if (!("report" in admitted)) throw new Error(admitted.detail)
  const receipt = admitted.report.evidence.find((entry) => entry.oracle_receipt)?.oracle_receipt
  expect(receipt?.candidate_subject).toBeUndefined()
})

test("the observed candidate subject from the packet's work context is injected when present", () => {
  const packet = reviewPacket() as AgentLanePacket
  ;(packet.inputs as { work_context?: unknown }).work_context = {
    source_event_frontier: 7,
    required_reading: [],
    findings: [],
    domain_groups: [],
    candidate_subject: "git:commit:observed-subject",
  }
  const admitted = resolveWorkerReportFromText(oracleReportText(), packet)
  if (!("report" in admitted)) throw new Error(admitted.detail)
  const receipt = admitted.report.evidence.find((entry) => entry.oracle_receipt)?.oracle_receipt
  expect(receipt?.candidate_subject).toBe("git:commit:observed-subject")
})

test("an executed receipt without a run locator is refused by the closed report schema", () => {
  const withoutRunRef = JSON.parse(oracleReportText())
  withoutRunRef.evidence[1].oracle_receipt.run_ref = ""
  const failures: string[] = []
  expect(validateAgentLaneReport(withoutRunRef, failures)).toBe(false)
})

test("a not_run receipt carries the empty run locator and no exit code", () => {
  const notRun = JSON.parse(oracleReportText())
  notRun.evidence[1].oracle_receipt.result = "not_run"
  delete notRun.evidence[1].oracle_receipt.exit_code
  notRun.evidence[1].oracle_receipt.run_ref = ""
  const failures: string[] = []
  expect(validateAgentLaneReport(notRun, failures)).toBe(true)
})
