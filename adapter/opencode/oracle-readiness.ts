import { createHash } from "node:crypto"
import type { AgentLanePacket, DispatchRunner, NativeOraclePreparation } from "./dispatch"

// Readiness consumes the native owner's qualified, job-selected preparation
// records. The core dispatch admission owns subject freshness — it observes
// the claimed worktree's clean HEAD against the packet subject before the
// action transaction — so this check runs no git at all. It still refuses a
// packet whose oracle lacks a core-qualified subject or a matching
// qualified preparation per control.
export interface OracleReadinessDeps {
  runner: DispatchRunner
  directory: string
  signal: AbortSignal
}

export type OracleReadinessRefusal = { message: string }

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function strings(value: unknown): string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string") ? value : []
}

function preparationMatches(preparation: NativeOraclePreparation, control: Record<string, unknown>, packet: AgentLanePacket): boolean {
  const source = control.recipe_source
  const actual = preparation.recipe_source
  const names = preparation.selected_test_names
  const cases = strings(control.case_ids)
  const refs = strings(control.readiness_evidence_refs)
  const argv = strings(control.argv)
  return preparation.protocol === "native_oracle_v2" && preparation.phase === "prepare" && preparation.qualification === "ready"
    && refs.includes(preparation.run_ref) && preparation.work_id === packet.work_id
    && preparation.contract_version === packet.inputs.binding.contract_version && preparation.subject_commit === packet.inputs.work_context?.subject_commit
    && preparation.logical_cwd === control.cwd && argv.length > 0
    && preparation.logical_argv_digest === `sha256:${createHash("sha256").update(JSON.stringify(argv)).digest("hex")}`
    && isRecord(source) && preparation.project_id === source.project_id && actual.kind === "repository_file" && source.kind === actual.kind
    && source.project_id === actual.project_id && source.path === actual.path && source.commit_oid === actual.commit_oid
    && preparation.streams_complete && preparation.stdout.complete && preparation.stderr.complete
    && preparation.stdout.stream === "stdout" && preparation.stderr.stream === "stderr" && preparation.stdout.ref.length > 0 && preparation.stderr.ref.length > 0
    && Array.isArray(names) && names.length > 0 && new Set(names).size === names.length && preparation.selected_distinct_count === names.length
    && cases.length > 0 && isRecord(preparation.case_to_test_map)
    && cases.every((id) => {
      const witnesses = strings(preparation.case_to_test_map[id])
      return witnesses.length > 0 && witnesses.every((name) => names.includes(name))
    })
}

export async function verifyPacketOracleReadiness(packet: AgentLanePacket, deps: OracleReadinessDeps): Promise<OracleReadinessRefusal | null> {
  const oracle = packet.inputs.worker_job?.acceptance_oracle as unknown
  if (!isRecord(oracle)) return null
  const subject = packet.inputs.work_context?.subject_commit
  const recovery = 'run concord_work_transition worktree_verify with command ["git","rev-parse","--verify","HEAD^{commit}"], work_id, and a fresh idempotency_key on the clean candidate; refresh context and explicitly prepare the pinned native oracle before rebuilding the packet'
  if (typeof subject !== "string" || !/^[0-9a-f]{40}([0-9a-f]{24})?$/.test(subject)) {
    return { message: `acceptance oracle has no core-qualified raw subject_commit; ${recovery}` }
  }
  const preparations = packet.inputs.work_context?.oracle_preparations ?? []
  const controls = Array.isArray(oracle.controls) ? oracle.controls : []
  if (controls.length === 0) return { message: "acceptance oracle has no controls with qualified native preparation" }
  for (const control of controls) {
    if (!isRecord(control) || !preparations.some((preparation) => preparationMatches(preparation, control, packet))) {
      const id = isRecord(control) ? control.control_id : "control:?"
      return { message: `acceptance oracle control ${id} has no matching qualified native preparation in its retained readiness evidence; explicitly request worktree_verify oracle phase prepare, then record its actual preparation run reference in the ready job and refresh the packet` }
    }
  }
  return null
}
