import type { AgentLanePacket, DispatchRunner } from "./dispatch"

// CON-890 slice A: predispatch oracle readiness. The owner-level acceptance
// oracle of a packet's worker job pins each control's harness to one exact
// Git object (repository file at an exact commit). The core validated the
// closed graph and its authority joins at author time; this module is the
// adapter-side dispatch preparation the design assigns to the pre-spawn
// boundary: the pinned Git input must resolve, and the retained readiness
// evidence must be present, before the dispatch_worker authorization call
// persists an authorized attempt.
//
// Three boundaries this module never crosses:
// - It never executes the oracle's own argv. A control's argument vector is
//   an execution instruction for the worker inside declared lane
//   permissions, not an admission probe; running it here would spend worker
//   budget and side effects before any authorization existed.
// - It never calls the core. Readiness is answered from the repository the
//   dispatch can already reach (the claimed worktree's Git directory) and
//   the packet's own retained content.
// - It never infers selector nonemptiness from prose. The author's retained
//   readiness evidence is the proof that the harness resolves and the
//   selector is nonempty; a keyword or command-string heuristic over argv
//   would manufacture that proof from nothing.

export interface OracleReadinessDeps {
  runner: DispatchRunner
  /** The repository working tree the dispatch can reach: the claimed worktree of the dispatching Project. */
  directory: string
  signal: AbortSignal
}

export type OracleReadinessRefusal = { message: string }

interface PinnedRecipe {
  controlID: string
  projectID: string
  path: string
  commitOID: string
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : []
}

// pinnedRecipeSources flattens the oracle's controls into one probe list,
// keeping the first control identity that named each unique pinned source so
// a refusal names a concrete control, never an abstract object id.
function pinnedRecipeSources(oracle: Record<string, unknown>): PinnedRecipe[] {
  const seen = new Set<string>()
  const pinned: PinnedRecipe[] = []
  for (const control of Array.isArray(oracle.controls) ? oracle.controls : []) {
    if (!isRecord(control)) continue
    const source = isRecord(control.recipe_source) ? control.recipe_source : null
    if (!source) continue
    const projectID = typeof source.project_id === "string" ? source.project_id : ""
    const path = typeof source.path === "string" ? source.path : ""
    const commitOID = typeof source.commit_oid === "string" ? source.commit_oid : ""
    if (projectID.length === 0 || path.length === 0 || commitOID.length === 0) continue
    const key = `${projectID}\u0000${commitOID}\u0000${path}`
    if (seen.has(key)) continue
    seen.add(key)
    pinned.push({ controlID: typeof control.control_id === "string" ? control.control_id : "control:?", projectID, path, commitOID })
  }
  return pinned
}

// verifyPacketOracleReadiness refuses a dispatch whose oracle is not
// dispatch-ready. A packet without an oracle (every revision a pre-oracle
// definition recorded) passes untouched and performs no probes.
export async function verifyPacketOracleReadiness(packet: AgentLanePacket, deps: OracleReadinessDeps): Promise<OracleReadinessRefusal | null> {
  const oracle = packet.inputs.worker_job?.acceptance_oracle as unknown
  if (!isRecord(oracle)) return null

  // Retained readiness evidence, as the author declared it: every control
  // carries at least one readiness reference. The closed packet schema
  // enforces the same bound; this defensive re-check keeps a future schema
  // drift from silently dispatching a control whose selector proof vanished.
  for (const control of Array.isArray(oracle.controls) ? oracle.controls : []) {
    if (!isRecord(control)) continue
    const controlID = typeof control.control_id === "string" ? control.control_id : "control:?"
    const readiness = stringArray(control.readiness_evidence_refs)
    const argv = stringArray(control.argv)
    if (readiness.length === 0 || argv.length === 0) {
      return { message: `acceptance oracle control ${controlID} carries no retained readiness evidence proving its harness resolves and its test selector is nonempty; record the readiness evidence through record_worker_job before dispatching` }
    }
  }

  // Exact repo source availability: each pinned source must resolve as a
  // real commit object in the repository the dispatch can reach, and the
  // pinned path must exist in that commit's tree. One probe pair per unique
  // pinned source, answered by read-only git plumbing. A probe that itself
  // throws (no git, unreadable repository) refuses as an unresolved source,
  // never as an exception through admission.
  const probe = async (argv: string[]): Promise<{ exitCode: number; stdout: string }> => {
    try {
      const outcome = await deps.runner.run(argv, "", deps.signal)
      return { exitCode: outcome.exitCode, stdout: outcome.stdout }
    } catch (error) {
      return { exitCode: -1, stdout: `probe threw: ${error instanceof Error ? error.message : String(error)}` }
    }
  }
  // The packet carries the core's existing verify receipt subject. Neither
  // the harness commit nor a worker echo can replace it. Observe the actual
  // clean HEAD here before dispatchWorker calls its authorizer.
  const subject = packet.inputs.work_context?.candidate_subject
  const recovery = "run concord_work_transition worktree_verify with work_id, command, and a new idempotency_key on the clean candidate, then rebuild the packet before dispatching"
  if (typeof subject !== "string" || !/^commit:[0-9a-f]{40}([0-9a-f]{24})?$/.test(subject)) {
    return { message: `acceptance oracle has no core-qualified candidate subject; ${recovery}` }
  }
  const head = await probe(["git", "-C", deps.directory, "rev-parse", "--verify", "HEAD^{commit}"])
  if (head.exitCode !== 0 || `commit:${head.stdout.trim()}` !== subject) {
    return { message: `acceptance oracle candidate subject ${subject} does not match the observed HEAD (git rev-parse exited ${head.exitCode}); ${recovery}` }
  }
  const status = await probe(["git", "-C", deps.directory, "status", "--porcelain=v1", "-z", "--untracked-files=all"])
  if (status.exitCode !== 0 || status.stdout.length !== 0) {
    return { message: `acceptance oracle requires the observed candidate HEAD to be clean, including the index and untracked files (git status exited ${status.exitCode}); ${recovery}` }
  }
  for (const pinned of pinnedRecipeSources(oracle)) {
    const commitProbe = await probe(["git", "-C", deps.directory, "cat-file", "-e", `${pinned.commitOID}^{commit}`])
    if (commitProbe.exitCode !== 0) {
      return {
        message: `acceptance oracle control ${pinned.controlID} pins harness commit ${pinned.commitOID} (project ${pinned.projectID}) that does not resolve as a commit object in the repository at ${deps.directory}; prepare the harness at the pinned commit or re-record the job under a reachable pin before dispatching (git cat-file exited ${commitProbe.exitCode})`,
      }
    }
    const treeProbe = await probe(["git", "-C", deps.directory, "ls-tree", "-r", pinned.commitOID, "--", pinned.path])
    if (treeProbe.exitCode !== 0) {
      return { message: `acceptance oracle readiness could not resolve the pinned harness tree: git ls-tree exited ${treeProbe.exitCode} for control ${pinned.controlID} at commit ${pinned.commitOID}` }
    }
    if (treeProbe.stdout.trim().length === 0) {
      return { message: `acceptance oracle control ${pinned.controlID} pins harness path ${pinned.path} at commit ${pinned.commitOID}, but that path is absent from the commit's tree; prepare the harness at the pinned commit or re-record the job under a reachable pin before dispatching` }
    }
  }
  return null
}
