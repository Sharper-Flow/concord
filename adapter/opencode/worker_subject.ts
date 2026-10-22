import { createHash } from "node:crypto"
import type { AgentLanePacket, DispatchRunner, WorkerFailureObservation } from "./dispatch"
import type { AgentLane } from "./generated-agent-lanes"

export type ReviewSubjectFailure = { detail: string; observation: WorkerFailureObservation }

export function repairingReview(lane: AgentLane, packet: AgentLanePacket): boolean {
  return lane.digest === packet.lane_digest && (lane.capabilities as readonly string[]).includes("edit_scoped_files") && (lane.capabilities as readonly string[]).includes("inspect_diff")
}

export async function reviewSubjectFailure(lane: AgentLane, packet: AgentLanePacket, directory: string, signal: AbortSignal, runner: DispatchRunner, boundary: "entry" | "terminal" = "entry"): Promise<ReviewSubjectFailure | null> {
  const subject = packet.inputs.work_context?.subject_commit
  if (!lane.capabilities.some((capability) => capability === "inspect_diff") || subject === undefined) return null
  const failure = (detail: string, observed = false): ReviewSubjectFailure => ({
    detail,
    observation: {
      observer: "host_adapter", phase: "readback", cause: observed ? "identity_provenance" : "unknown", basis: observed ? "hypothesis" : "unknown",
      sources: [{ kind: "validator", message: detail, digest: `sha256:${createHash("sha256").update(detail).digest("hex")}` }],
    },
  })
  const oid = /^[0-9a-f]{40}([0-9a-f]{24})?$/
  if (!oid.test(subject)) return failure("review subject is not a raw commit OID")
  const boundedSignal = AbortSignal.any([signal, AbortSignal.timeout(5_000)])
  try {
    boundedSignal.throwIfAborted()
    const git = ["git", "--no-optional-locks", "-c", "core.fsmonitor=false", "-C", directory]
    const head = await runner.run([...git, "rev-parse", "--verify", "HEAD^{commit}"], "", boundedSignal)
    boundedSignal.throwIfAborted()
    const status = await runner.run([...git, "status", "--porcelain=v1", "-z", "--untracked-files=all"], "", boundedSignal)
    if (boundedSignal.aborted || head.exitCode !== 0 || status.exitCode !== 0 || !oid.test(head.stdout.trim())) {
      return failure("review subject observation unavailable: Git did not return a complete HEAD and tree observation")
    }
    const output = head.stdout.trim()
    if (status.stdout !== "" || (output !== subject && (boundary === "entry" || !repairingReview(lane, packet)))) {
      return failure(`review subject mismatch: expected HEAD ${subject}; observed HEAD ${head.stdout.trim()}; tree_clean=${status.stdout === ""}`, true)
    }
    if (output !== subject) {
      const ancestry = await runner.run([...git, "merge-base", "--is-ancestor", subject, output], "", boundedSignal)
      boundedSignal.throwIfAborted()
      if (ancestry.exitCode !== 0) return failure("review subject mismatch: output is not a proved descendant of its input candidate", true)
    }
    return null
  } catch {
    return failure("review subject observation unavailable: Git probe failed")
  }
}
