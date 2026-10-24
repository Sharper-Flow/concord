import { expect, test } from "bun:test"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { agentLanes, laneLegacyDigests } from "./generated-agent-lanes"
import { defaultRunner, type AgentLanePacket, type DispatchRunner } from "./dispatch"
import { reviewSubjectFailure } from "./worker_subject"

const lane = agentLanes.find((entry) => entry.id === "review")!
const subject = "a".repeat(40)
const packet: AgentLanePacket = {
  schema_version: "1.0", attempt_id: "subject-attempt", work_id: "subject-work", step_id: "review",
  lane_id: lane.id, lane_version: lane.version, lane_digest: lane.digest,
  inputs: { task: "Review the pinned subject", binding: { objective_source: "contract_premise", work_version: 1, contract_version: 1, assigned_result: "contract_findings" },
    work_context: { source_event_frontier: 1, required_reading: [], findings: [], domain_groups: [], subject_commit: subject } },
}

test("the review backstop admits only the clean exact pinned subject", async () => {
  for (const [head, status, exitCode, expected] of [[subject, "", 0, null], ["b".repeat(40), "", 0, "mismatch"], [subject, "?? private-name\0", 0, "mismatch"], ["not-an-oid", "", 0, "unavailable"], [subject, "", 1, "unavailable"]] as const) {
    const runner: DispatchRunner = { run: async (argv) => ({ stdout: argv.includes("rev-parse") ? head + "\n" : status, stderr: "private diagnostic", exitCode }) }
    const result = await reviewSubjectFailure(lane, packet, "/synthetic/worktree", new AbortController().signal, runner)
    if (expected === null) expect(result).toBeNull()
    else {
      expect(result?.detail).toContain(expected)
      expect(result?.detail).not.toContain("private-name")
      expect(result?.detail).not.toContain("private diagnostic")
      expect(result?.observation.sources[0].kind).toBe("validator")
      expect(result?.observation.basis).not.toBe("supported")
    }
  }
})

test("missing subjects and non-review capabilities run no metadata probes", async () => {
  const runner: DispatchRunner = { run: async () => { throw new Error("must not run") } }
  expect(await reviewSubjectFailure(lane, { ...packet, inputs: { ...packet.inputs, work_context: undefined } }, "/synthetic/worktree", new AbortController().signal, runner)).toBeNull()
  expect(await reviewSubjectFailure(agentLanes.find((entry) => entry.id === "implement")!, packet, "/synthetic/worktree", new AbortController().signal, runner)).toBeNull()
})

test("a cancelled review probe starts no process and an unavailable probe grants no review", async () => {
  let calls = 0
  const runner: DispatchRunner = { run: async () => { calls++; throw new Error("unavailable") } }
  const cancelled = AbortSignal.abort()
  expect((await reviewSubjectFailure(lane, packet, "/synthetic/worktree", cancelled, runner))?.detail).toContain("unavailable")
  expect(calls).toBe(0)
  expect((await reviewSubjectFailure(lane, packet, "/synthetic/worktree", new AbortController().signal, runner))?.observation.cause).toBe("unknown")
  expect(calls).toBe(1)
})

test("real Git observes the pinned clean subject and refuses untracked files or a later commit", async () => {
  const root = mkdtempSync(join(tmpdir(), "concord-review-subject-"))
  const signal = AbortSignal.timeout(10_000)
  const git = async (...args: string[]) => {
    const result = await defaultRunner.run(["git", "-C", root, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", ...args], "", signal)
    expect(result.exitCode, result.stderr).toBe(0)
    return result.stdout.trim()
  }
  try {
    await git("init", "--quiet", "--initial-branch=main")
    await git("commit", "--quiet", "--allow-empty", "-m", "fixture")
    const head = await git("rev-parse", "--verify", "HEAD^{commit}")
    const pinned = { ...packet, inputs: { ...packet.inputs, work_context: { ...packet.inputs.work_context!, subject_commit: head } } }
    expect(await reviewSubjectFailure(lane, pinned, root, signal, defaultRunner)).toBeNull()
    const untracked = join(root, "untracked.txt")
    await Bun.write(untracked, "synthetic\n")
    expect((await reviewSubjectFailure(lane, pinned, root, signal, defaultRunner))?.detail).toContain("tree_clean=false")
    rmSync(untracked)
    await git("commit", "--quiet", "--allow-empty", "-m", "next fixture")
    expect((await reviewSubjectFailure(lane, pinned, root, signal, defaultRunner))?.detail).toContain("review subject mismatch")
    expect(await reviewSubjectFailure(lane, pinned, root, signal, defaultRunner, "terminal")).toBeNull()
    const readOnly = { ...pinned, lane_digest: laneLegacyDigests["review:1"].at(-1)! }
    expect((await reviewSubjectFailure(lane, readOnly, root, signal, defaultRunner, "terminal"))?.detail).toContain("review subject mismatch")
    await git("checkout", "--orphan", "unrelated")
    await git("commit", "--quiet", "--allow-empty", "-m", "unrelated")
    expect((await reviewSubjectFailure(lane, pinned, root, signal, defaultRunner, "terminal"))?.detail).toContain("not a proved descendant")
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
