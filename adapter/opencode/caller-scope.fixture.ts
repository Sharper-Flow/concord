// callerScopeFields authors the direct-caller allowance (CD-0227 D1 as
// amended by CD-0242 D6) an implementation job carries on a caller-capable
// pin: a minimal caller allowance inside the oracle owner's Domain. The
// allowance names the job's recorded project_scope, the work's primary
// Project, which may differ from the oracle owner's Project.
export function callerScopeFields(owner: { domain_id: string; mechanism: { project_id: string } }, inventory: string, project = owner.mechanism.project_id) {
  return {
    direct_caller_allowance: {
      project_id: project,
      mechanisms: [{ project_id: project, path: inventory, entry_point: "recorded job" }],
      domain_ids: [owner.domain_id],
      law_bindings: [],
    },
  }
}

// headCommit reads the delivered tree the terminal observation seals.
export function headCommit(directory: string): string {
  const result = Bun.spawnSync(["git", "-C", directory, "rev-parse", "HEAD"])
  if (result.exitCode !== 0) throw new Error(`read HEAD of ${directory}: ${result.stderr.toString()}`)
  return result.stdout.toString().trim()
}
