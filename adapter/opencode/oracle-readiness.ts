import { createHash } from "node:crypto"
import type { AgentLanePacket, NativeOracleSelectedGoPreparation, NativeOraclePackagePreparation, NativeOraclePreparation, NativeOracleTargetPreparation } from "./dispatch"

// Readiness consumes the native owner's qualified, job-selected preparation
// records. The core dispatch admission owns subject freshness — it observes
// the claimed worktree's clean HEAD against the packet subject before the
// action transaction — so this check runs no git at all. It still refuses a
// packet whose oracle lacks a core-qualified subject or a matching
// qualified preparation per control.
export type OracleReadinessRefusal = { message: string }

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function strings(value: unknown): string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string") ? value : []
}

// The closed admitted materializer/runner pairs of target_runner_v1. Every
// other pair — including unknown members — refuses before any probe.
const admittedTargetPairs = new Set(["uv:pytest", "none:check", "uv:check", "bun:check"])

// Witness IDs are runner-defined exact identities: 1 to 256 bytes, unique,
// and free of control characters, mirroring the core's closed manifest rule.
const visibleWitnessPattern = /^[^\x00-\x1f\x7f]*$/

function targetWitnessesValid(preparation: NativeOracleTargetPreparation, cases: string[]): boolean {
  if (preparation.selected_test_names !== undefined || preparation.case_to_test_map !== undefined) return false
  if (!admittedTargetPairs.has(`${preparation.target_materializer}:${preparation.target_runner}`)) return false
  const selected = preparation.selected_witness_ids
  if (!Array.isArray(selected) || selected.length < 1 || selected.length > 40) return false
  if (new Set(selected).size !== selected.length || preparation.selected_distinct_count !== selected.length) return false
  if (!selected.every((witness) => witness.length >= 1 && Buffer.byteLength(witness, "utf8") <= 256 && visibleWitnessPattern.test(witness))) return false
  const map = preparation.case_to_witness_map
  if (!isRecord(map) || Object.keys(map).length !== cases.length) return false
  const selectedSet = new Set(selected)
  const covered = new Set<string>()
  for (const id of cases) {
    const witnesses = strings(map[id])
    if (witnesses.length < 1 || witnesses.length > 8 || new Set(witnesses).size !== witnesses.length) return false
    for (const witness of witnesses) {
      if (!selectedSet.has(witness)) return false
      covered.add(witness)
    }
  }
  return covered.size === selected.length
}

function packageWitnessesValid(preparation: NativeOraclePackagePreparation, cases: string[], argv: string[]): boolean {
  const logical = ["go", "test", "-count=1", "."]
  if (argv.length !== logical.length || !argv.every((part, i) => part === logical[i])) return false
  if (preparation.selected_test_names !== undefined || preparation.case_to_test_map !== undefined
    || preparation.target_materializer !== undefined || preparation.target_runner !== undefined
    || preparation.files !== null || preparation.harness_input_count !== undefined || preparation.harness_input_digest !== undefined) return false
  const selected = preparation.selected_witness_ids
  const witness = `go_package:./${preparation.logical_cwd}`
  if (!Array.isArray(selected) || selected.length !== 1 || selected[0] !== witness || preparation.selected_distinct_count !== 1) return false
  const map = preparation.case_to_witness_map
  if (!isRecord(map) || Object.keys(map).length < 1 || Object.keys(map).length > 64) return false
  // The native preparation retains the selected recipe cases.
  // Every package case names the same package.
  return cases.every((id) => Object.hasOwn(map, id)) && Object.values(map).every((value) => {
    const names = strings(value)
    return names.length === 1 && names[0] === witness
  })
}

function goWitnessesValid(preparation: NativeOracleSelectedGoPreparation, cases: string[]): boolean {
  if (preparation.recipe_kind !== undefined || preparation.target_materializer !== undefined || preparation.target_runner !== undefined
    || preparation.selected_witness_ids !== undefined || preparation.case_to_witness_map !== undefined) return false
  const names = preparation.selected_test_names
  return Array.isArray(names) && names.length > 0 && names.length <= 40 && new Set(names).size === names.length
    && preparation.selected_distinct_count === names.length
    && cases.length > 0 && isRecord(preparation.case_to_test_map)
    && cases.every((id) => {
      const witnesses = strings(preparation.case_to_test_map[id])
      return witnesses.length > 0 && witnesses.length <= 8 && new Set(witnesses).size === witnesses.length
        && witnesses.every((name) => names.includes(name))
    })
}

function preparationMatches(preparation: NativeOraclePreparation, control: Record<string, unknown>, packet: AgentLanePacket): boolean {
  const source = control.recipe_source
  const actual = preparation.recipe_source
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
    && cases.length > 0
    && (preparation.recipe_kind === "target_runner_v1"
      ? targetWitnessesValid(preparation, cases)
      : preparation.recipe_kind === "go_package_v1"
        ? packageWitnessesValid(preparation, cases, argv)
      : goWitnessesValid(preparation, cases))
}

export async function verifyPacketOracleReadiness(packet: AgentLanePacket): Promise<OracleReadinessRefusal | null> {
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
