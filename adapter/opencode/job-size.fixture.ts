import { agentLanes } from "./generated-agent-lanes"

// jobSizeFields authors the CD-0225 size members an implementation job
// carries on a size-capable pin: a bounded cap, a measurement basis that
// inventories one path inside the job's path_scope at the admitted clean
// subject, and a minimal caller allowance inside the oracle owner's Domain.
// The allowance names the job's recorded project_scope, the work's primary
// Project, which may differ from the oracle owner's Project.
export function jobSizeFields(owner: { domain_id: string; mechanism: { project_id: string } }, inventory: string, baseCommit: string, project = owner.mechanism.project_id) {
  return {
    production_size_max: 400,
    measurement_basis: { base_commit: baseCommit, paths: [{ path: inventory, class: "production" }] },
    direct_caller_allowance: {
      project_id: project,
      mechanisms: [{ project_id: project, path: inventory, entry_point: "recorded job" }],
      domain_ids: [owner.domain_id],
      law_bindings: [],
    },
  }
}

// reportedProductionSize is the production_size member a compliant
// implementation worker reports for a capped job: the base its packet
// carries and the delivered tree. A packet without a measurement basis or
// for another lane reports no member.
export function reportedProductionSize(packet: { lane_id?: unknown; inputs?: unknown }, tree: string): { production_size?: { base: string; tree: string; count: number } } {
  const job = (packet.inputs as { worker_job?: { measurement_basis?: { base_commit: string } } } | undefined)?.worker_job
  const lane = agentLanes.find((candidate) => candidate.id === packet.lane_id)
  if (!job?.measurement_basis || lane?.capability_class !== "implementation") return {}
  return { production_size: { base: job.measurement_basis.base_commit, tree, count: 0 } }
}

// headCommit reads the delivered tree the terminal observation seals.
export function headCommit(directory: string): string {
  const result = Bun.spawnSync(["git", "-C", directory, "rev-parse", "HEAD"])
  if (result.exitCode !== 0) throw new Error(`read HEAD of ${directory}: ${result.stderr.toString()}`)
  return result.stdout.toString().trim()
}
