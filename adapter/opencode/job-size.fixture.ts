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
