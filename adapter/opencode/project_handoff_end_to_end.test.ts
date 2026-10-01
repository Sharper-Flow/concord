import { afterEach, describe, expect, test } from "bun:test"
import { Database } from "bun:sqlite"
import { createPrivateKey, createPublicKey } from "node:crypto"
import { mkdir, mkdtemp, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureConcordAdapter, consumeAddressedProjectHandoff, consumedProjectHandoff, invokeConcordOperation, resetConsumedProjectHandoffs } from "./concord"
import { configureCoreBinary, type DispatchRunner } from "./dispatch"
import { configureHostLease } from "./host-lease"
import { manifestDigest } from "./generated-contracts"

// The Project-session handoff acceptance runs on the real boundary: a real
// core binary against a real store, two Projects in two repositories, the
// real boot/resume route naming the bounded job, and the real
// claim-landing verb recording the receiving session's placement. A clean
// checkout has no binary on PATH; declare the test skipped when neither
// CONCORD_BIN nor a Go toolchain can produce one, so a suite run without
// the toolchain reports a skip, never a silent pass.
const routeDeclaration =
  process.env.CONCORD_BIN || Bun.spawnSync(["go", "version"]).exitCode === 0
    ? (name: string, fn: () => Promise<void>) => test(name, fn, { timeout: 300000 })
    : (name: string, fn: () => Promise<void>) => test.skip(name, fn, { timeout: 300000 })

configureCoreBinary("concord")

const PRODUCT_ID = "product-handoff-e2e"
const SOURCE_PROJECT = "project-handoff-source"
const RECEIVE_PROJECT = "project-handoff-receive"
const CLIENT_REF = "opencode"
const SOURCE_SESSION = "session-handoff-source"
const RECEIVE_SESSION = "session-handoff-receive"
const PRIVATE_SEED = new Uint8Array(32).fill(11)
const PRIVATE_KEY_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex")
const HANDOFF_PREDICATE = {
  predicate_id: "predicate:handoff-e2e-consume",
  ordinal: 0,
  outcome_kind: "check",
  outcome_payload: {
    kind: "check",
    check_ref: "check:bun-test/adapter/opencode/project_handoff_end_to_end.test.ts",
    expected_result: "pass",
    immutable_subject_ref: "contract:work-handoff-e2e:1",
  },
}

type JSONRecord = Record<string, any>

function privateKeyObject() {
  return createPrivateKey({ key: Buffer.concat([PRIVATE_KEY_PREFIX, Buffer.from(PRIVATE_SEED)]), format: "der", type: "pkcs8" })
}

function publicKeyBase64(): string {
  return createPublicKey(privateKeyObject()).export({ format: "der", type: "spki" }).subarray(-32).toString("base64")
}

async function runProcess(argv: string[], input = "", cwd?: string): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  const child = Bun.spawn(argv, { cwd, env: { ...process.env, CONCORD_DB_PATH: process.env.CONCORD_DB_PATH }, stdin: "pipe", stdout: "pipe", stderr: "pipe" })
  await child.stdin.write(input)
  await child.stdin.end()
  const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
  return { exitCode, stdout, stderr }
}

async function git(cwd: string, ...args: string[]): Promise<void> {
  const result = await runProcess(["git", ...args], "", cwd)
  expect(result.exitCode, `git ${args.join(" ")}: ${result.stderr}`).toBe(0)
}

function dbValue(dbPath: string, sql: string): any {
  const db = new Database(dbPath, { readonly: true })
  try {
    return db.query(sql).get()
  } finally {
    db.close()
  }
}

// realCoreRunner spawns the real core binary against the real store for
// every transport leg. This is the boundary the acceptance claims: no
// canned answer stands between the adapter's consume and the core.
function realCoreRunner(binary: string, dbPath: string, captured: { invoke?: JSONRecord }): DispatchRunner {
  return {
    async run(argv: string[], input: string, signal?: AbortSignal) {
      if (argv[1] === "invoke") captured.invoke = JSON.parse(input) as JSONRecord
      const child = Bun.spawn([binary, ...argv.slice(1)], { env: { ...process.env, CONCORD_DB_PATH: dbPath }, stdin: "pipe", stdout: "pipe", stderr: "pipe" })
      if (signal?.aborted) child.kill()
      await child.stdin.write(input)
      await child.stdin.end()
      const [stdout, stderr, exitCode] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited])
      return { exitCode, stdout, stderr }
    },
  } as never
}

async function fakeHostControlPlane(directory: string, runner: DispatchRunner) {
  configureHostLease({
    release: { coreBinary: "concord", releaseRoot: "/releases/v11.0.0" },
    runner: {
      async run(argv: string[]) {
        if (argv[1] === "host-lease") {
          return { exitCode: 0, stdout: JSON.stringify({ pid: process.pid, pid_start: 1, release_root: "/releases/v11.0.0", core_binary: "concord", schema_version: 93, manifest_digest: manifestDigest, directory, worktree: directory }), stderr: "" }
        }
        throw new Error("unexpected host-lease invocation: " + argv.join(" "))
      },
    } as never,
  })
  configureConcordAdapter({ runner })
  await ConcordAdapterPlugin({
    client: {
      _client: {
        post: async () => {
          throw new Error("unexpected POST")
        },
        get: async (request: { path?: { id?: string } }) => ({
          data: { id: request?.path?.id ?? RECEIVE_SESSION, directory },
          response: new Response(null, { status: 200 }),
        }),
      },
    },
    serverUrl: new URL("http://127.0.0.1:4096"),
  } as never)
}

const contextFor = (sessionID: string, directory: string) =>
  ({
    sessionID,
    messageID: "message-1",
    agent: "concord-implement",
    directory,
    worktree: directory,
    abort: new AbortController().signal,
    metadata: () => {},
    ask: async () => {},
  }) as any

interface HandoffFixture {
  root: string
  binary: string
  dbPath: string
  workID: string
  sourceWorktree: string
  handoffID: string
  captured: { invoke?: JSONRecord }
}

// bootHandoffFixture builds the real two-repository store: one product, two
// Projects with canonical locators in distinct repositories, one shared work
// item holding both memberships, one approved contract, and the recorded
// handoff the source session addressed to the receiving Project through the
// real invoke route.
async function bootHandoffFixture(root: string): Promise<HandoffFixture> {
  let binRoot = ""
  let binary = process.env.CONCORD_BIN ?? ""
  if (!binary) {
    binRoot = join(root, "bin")
    await mkdir(binRoot)
    binary = join(binRoot, "concord")
    const build = await runProcess(["go", "build", "-o", binary, "./cmd/concord"], "", join(import.meta.dir, "..", ".."))
    expect(build.exitCode, `go build: ${build.stderr}`).toBe(0)
  }
  process.env.CONCORD_DB_PATH = join(root, "concord.db")
  const dbPath = process.env.CONCORD_DB_PATH
  const repoSource = join(root, "repo-source")
  const repoReceive = join(root, "repo-receive")
  for (const repo of [repoSource, repoReceive]) {
    await mkdir(join(repo, ".concord/docs/knowledge"), { recursive: true })
    await Bun.write(join(repo, ".concord/docs/knowledge", "manifest.json"), JSON.stringify({ schema_version: "1.2", supported_kinds: [], indexed_kinds: [], knowledge_roots: [] }, null, 2))
    await Bun.write(join(repo, ".concord/docs/knowledge", "domain-registry.json"), JSON.stringify({
      schema_version: "1.0",
      product_key: PRODUCT_ID,
      root_domain_id: `product-root:${PRODUCT_ID}`,
      domains: [{ domain_id: `product-root:${PRODUCT_ID}`, name: "Synthetic root", purpose: "Synthetic test domain", status: "current", architecture_relations: [] }],
    }, null, 2))
    await Bun.write(join(repo, "README.md"), "synthetic handoff fixture\n")
    await git(repo, "init", "--quiet", "--initial-branch=main")
    await git(repo, "config", "user.email", "test@example.invalid")
    await git(repo, "config", "user.name", "Synthetic Test")
    await git(repo, "add", ".")
    await git(repo, "commit", "--quiet", "-m", "fixture")
    await git(repo, "remote", "add", "origin", "https://example.invalid/synthetic.git")
    await git(repo, "update-ref", "refs/remotes/origin/main", "HEAD")
    await git(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
  }
  const runJSON = async (command: string, value: JSONRecord, cwd?: string): Promise<JSONRecord> => {
    const result = await runProcess([binary, command], JSON.stringify(value), cwd)
    expect(result.exitCode, `${command}: ${result.stderr}`).toBe(0)
    return JSON.parse(result.stdout.trim().split("\n").filter(Boolean)[0]) as JSONRecord
  }
  await runJSON("product-create", { product_id: PRODUCT_ID, display_name: "Handoff E2E", stage_maturity: "prototype", stage_audience_commitment: "operator_only", project_id: SOURCE_PROJECT, project_display_name: "Source Project", role: "primary" })
  await runJSON("project-create", { project_id: RECEIVE_PROJECT, display_name: "Receiving Project", product_id: PRODUCT_ID, role: "secondary", expected_product_version: 2 })
  await runJSON("project-locator-add", { project_id: SOURCE_PROJECT, locator_id: "repo-source", kind: "canonical_path", value: repoSource, expected_version: 1 })
  await runJSON("project-locator-add", { project_id: RECEIVE_PROJECT, locator_id: "repo-receive", kind: "canonical_path", value: repoReceive, expected_version: 1 })
  await runJSON("product-knowledge-home-designate", { product_id: PRODUCT_ID, project_id: SOURCE_PROJECT, locator_id: "repo-source", expected_version: 3 })
  const bootstrap = await runJSON("work-bootstrap", {
    product_id: PRODUCT_ID,
    project_id: SOURCE_PROJECT,
    title: "Synthetic handoff route",
    value_statement: "The bounded handoff crosses Projects without operator copying.",
    kind: "bug",
    task: "Exercise the handoff route.",
    idempotency_key: "handoff-e2e-bootstrap",
    priority: 1,
    urgency: "standard",
    tags: [],
    workflow_type_ref: "",
    external_ref: "",
    governing_requirements: [],
    ref: "HEAD",
  }, repoSource)
  const workID = bootstrap.work_id as string
  await runJSON("client-register", {
    client_ref: CLIENT_REF,
    key_id: "handoff-e2e-key",
    principal_ref: "operator-1",
    public_key: publicKeyBase64(),
    capabilities: ["product_read", "work_transition", "work_relate"],
    product_scope: [PRODUCT_ID],
    project_scope: [SOURCE_PROJECT, RECEIVE_PROJECT],
    agent_scope: ["concord-implement"],
  })
  const captured: { invoke?: JSONRecord } = {}
  const sourceWorktree = (bootstrap.worktree as JSONRecord).path as string
  // The host session route answers the session's current directory: after
  // the boot move the session reads back inside the claimed worktree. The
  // real core runner serves every transport leg from here on.
  await fakeHostControlPlane(sourceWorktree, realCoreRunner(binary, dbPath, captured))
  const sourceContext = contextFor(SOURCE_SESSION, sourceWorktree)
  const invoke = (toolName: string, operation: string, input: JSONRecord, callContext: any) => invokeConcordOperation(toolName, { operation, input } as any, callContext)
  const version = () => dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number

  const memberships = await invoke("concord_work_relate", "set_memberships", { work_id: workID, expected_version: await version(), memberships: [{ project_id: SOURCE_PROJECT, role: "primary" }, { project_id: RECEIVE_PROJECT, role: "secondary" }], idempotency_key: "handoff-e2e-memberships" }, sourceContext)
  expect(memberships.outcome, JSON.stringify(memberships)).toBe("ok")
  const walk = async (actionID: string, fields: JSONRecord) => {
    const response = await invoke("concord_work_transition", "workflow_action", { work_id: workID, expected_version: await version(), action_id: actionID, idempotency_key: `handoff-e2e-${actionID}`, fields }, sourceContext)
    expect(response.outcome, `${actionID}: ${JSON.stringify(response)}`).toBe("ok")
  }
  await walk("record_reproduction", {})
  await walk("record_alignment", { searched: "Searched the backlog for duplicate defect work.", outcome: "none_found" })
  await walk("record_root_cause", {})
  const domainList = await invoke("concord_domain", "list", { product_id: PRODUCT_ID, page: { cursor: null, limit: 10 } }, sourceContext)
  expect(domainList.outcome).toBe("ok")
  const registryHash = ((domainList.result as JSONRecord).registry as JSONRecord).content_hash as string
  await walk("approve_contract", {
    premise: "Deliver the bounded cross-Project handoff.",
    outcome_predicates: [HANDOFF_PREDICATE],
    required_evidence: [],
    route_conventions: [],
    spec_mandate: [],
    law_modifies: [],
    architecture_binding: {
      domain_registry_content_hash: registryHash,
      home_domain_id: `product-root:${PRODUCT_ID}`,
      affected_domain_ids: [`product-root:${PRODUCT_ID}`],
      domain_modifies: [],
      domain_relation_modifies: [],
      law_additions: [],
      verification_obligations: [],
    },
  })
  const recorded = await invoke("concord_work_transition", "project_handoff_record", {
    work_id: workID,
    target_project_id: RECEIVE_PROJECT,
    bounded_job: "verify the receiving repository's adapter surface",
    next_action: "consume the handoff and run the bounded job",
    changes: ["adapter/opencode: opener route"],
    idempotency_key: "handoff-e2e-record",
  }, sourceContext)
  expect(recorded.outcome, `record: ${JSON.stringify(recorded)}`).toBe("ok")
  const handoffID = (recorded.result as JSONRecord).handoff_id as string
  expect(handoffID).toBeTruthy()
  return { root, binary, dbPath, workID, sourceWorktree, handoffID, captured }
}

afterEach(async () => {
  resetConsumedProjectHandoffs()
  configureConcordAdapter({ reset: true })
  configureHostLease({ reset: true })
  await ConcordAdapterPlugin({})
  configureHostLease({ reset: true })
  delete process.env.CONCORD_DB_PATH
})

routeDeclaration("consumes the addressed handoff through the real core on the real boot route", async () => {
  const root = await mkdtemp(join(tmpdir(), "concord-handoff-e2e-"))
  try {
    const fixture = await bootHandoffFixture(root)
    const { binary, dbPath, workID, sourceWorktree, handoffID, captured } = fixture
    const runJSON = async (command: string, value: JSONRecord, cwd?: string): Promise<JSONRecord> => {
      const result = await runProcess([binary, command], JSON.stringify(value), cwd)
      expect(result.exitCode, `${command}: ${result.stderr}`).toBe(0)
      return JSON.parse(result.stdout.trim().split("\n").filter(Boolean)[0]) as JSONRecord
    }
    const repoReceive = join(root, "repo-receive")

    // The Project-selected boot route names the bounded job: work-resume
    // claims the receiving Project's worktree and renders the addressed
    // handoff the receiving session must consume, with no operator copying.
    const resume = await runJSON("work-resume", { product_id: PRODUCT_ID, project_id: RECEIVE_PROJECT, work_id: workID }, repoReceive)
    const rendered = resume.project_handoff as JSONRecord
    expect(rendered.handoff_id).toBe(handoffID)
    expect(rendered.bounded_job).toContain("verify the receiving repository's adapter surface")
    expect(rendered.source_project_id).toBe(SOURCE_PROJECT)
    const receiveWorktree = (resume.worktree as JSONRecord).path as string
    const resolve = await runJSON("project-resolve", { directory: receiveWorktree })
    expect(resolve.project_id).toBe(RECEIVE_PROJECT)

    // The consume refuses closed before the verified landing: no placement,
    // no bind, no managed execution.
    const receiveContext = contextFor(RECEIVE_SESSION, receiveWorktree)
    const transport = { sessionDirectory: receiveWorktree, ambient: { projectID: RECEIVE_PROJECT, productIDs: [PRODUCT_ID], scopeVersion: resolve.scope_version as string, mainWorktree: false } }
    const unplaced = await consumeAddressedProjectHandoff(workID, handoffID, receiveContext, transport)
    expect(unplaced.consumed).toBe(false)
    expect(unplaced.message).toContain("no verified placement")
    expect(consumedProjectHandoff(RECEIVE_SESSION)).toBeNull()
    const untouched = dbValue(dbPath, `SELECT state, COALESCE(consumed_by_session_ref,'') AS consumer FROM project_handoffs WHERE handoff_id='${handoffID}'`)
    expect(untouched.state).toBe("recorded")
    expect(untouched.consumer).toBe("")

    // The real claim-landing verb records the verified placement: the host
    // readback is the core's own /proc identity, and the occupancy row is
    // the placement evidence the consume binds against.
    const landing = await runJSON("claim-landing", { work_id: workID, session_ref: RECEIVE_SESSION, landed_directory: receiveWorktree, host_pid: process.pid })
    expect(landing.work_id).toBe(workID)

    const consumed = await consumeAddressedProjectHandoff(workID, handoffID, receiveContext, transport)
    expect(consumed.consumed).toBe(true)
    expect(consumed.handoffID).toBe(handoffID)
    expect(consumedProjectHandoff(RECEIVE_SESSION)?.handoffID).toBe(handoffID)
    // The real authenticated call envelope: the consume rode the transport
    // the boot flow resolved, named the receiving session and Project, and
    // carried the handoff identity the boot route rendered.
    const envelope = (captured.invoke?.call_envelope ?? {}) as JSONRecord
    expect(captured.invoke?.tool).toBe("concord_work_transition")
    expect(captured.invoke?.operation).toBe("project_handoff_consume")
    expect(envelope.session_ref).toBe(RECEIVE_SESSION)
    expect(envelope.client_ref).toBe(CLIENT_REF)
    expect(envelope.directory).toBe(receiveWorktree)
    expect(envelope.worktree).toBe(receiveWorktree)
    expect(envelope.ambient_project_id).toBe(RECEIVE_PROJECT)
    expect(envelope.manifest_digest).toBe(manifestDigest)
    expect((captured.invoke?.input as JSONRecord).handoff_id).toBe(handoffID)
    // The core recorded the bind on the shared work.
    const bind = dbValue(dbPath, `SELECT state, consumed_by_session_ref AS consumer FROM project_handoffs WHERE handoff_id='${handoffID}'`)
    expect(bind.state).toBe("consumed")
    expect(bind.consumer).toBe(RECEIVE_SESSION)
    // The receiving bind touches neither the shared work's lifecycle nor
    // the source session's half: retirement stays the source session's own
    // verified path.
    const sourceState = dbValue(dbPath, `SELECT lifecycle FROM work_items WHERE id='${workID}'`)
    expect(sourceState.lifecycle).toBe("in_progress")
  } finally {
    await rm(root, { recursive: true, force: true })
  }
})
