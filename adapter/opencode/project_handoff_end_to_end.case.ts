import { afterEach, expect, test } from "bun:test"
import { Database } from "bun:sqlite"
import { createHash } from "node:crypto"
import { chmod, mkdir, writeFile } from "node:fs/promises"
import { basename, join } from "node:path"
import { fixtureTempRoot, requireOwnedFixtureRun, runFixtureProcess } from "./fixture-temp-root"
requireOwnedFixtureRun()
import ConcordAdapterPlugin from "./concord-plugin"
import { configureConcordAdapter, invokeConcordOperation, projectHandoffConsumeKey, work_start, work_transition } from "./concord"
import { concordBinaryPath, configureCoreBinary, type CoreRunner } from "./core-runtime"
import { configureHostLease } from "./host-lease"
import { resetClaimedWorktrees } from "./claimed-worktree"
import { resetTurnMoveBoundaries } from "./turn-move-boundary"
import { manifestDigest } from "./generated-contracts"
import { hostControlPlane, MOVE_SESSION_ROUTE, SESSION_ROUTE, SESSION_MESSAGES_ROUTE } from "./move-session"
import { dbValue, fixturePublicKeyBase64, type JSONRecord } from "./end-to-end.fixture"

// The Project-session handoff acceptance runs on the real boundary: a real
// core binary against a real store, two Projects in two repositories, the
// real boot route (work_start -> work-resume -> move readbacks ->
// claim-landing -> addressed consume) for both coordinator sessions, the
// real session_vacate mutation with its readback-verified landing
// (CD-0190), and the real project_retirement read deriving
// READY_TO_CLOSE_OR_REPLACE. A clean checkout has no binary on PATH; declare
// the test skipped when no Go toolchain can build one, so a suite run without
// the toolchain reports a skip, never a silent pass.
const routeTest =
  Bun.spawnSync(["go", "version"], { env: { ...process.env } }).exitCode === 0
    ? (name: string, fn: () => Promise<void>) => test(name, fn, { timeout: 300000 })
    : (name: string, fn: () => Promise<void>) => test.skip(name, fn, { timeout: 300000 })

function routeDeclaration(name: string, fn: () => Promise<void>) {
  const selected = process.env.TEST_CONCORD_HANDOFF_CASE
  if (!selected || new RegExp(selected).test(name)) routeTest(name, fn)
}

configureCoreBinary("concord")

const PRODUCT_ID = "product-handoff-e2e"
const SOURCE_PROJECT = "project-handoff-source"
const RECEIVE_PROJECT = "project-handoff-receive"
const CLIENT_REF = "opencode"
const SOURCE_SESSION = "session-handoff-source"
const RECEIVE_SESSION = "session-handoff-receive"
const RECEIVE_SESSION_B = "session-handoff-receive-b"
const AGENT = "Concord"
const PRIVATE_SEED = new Uint8Array(32).fill(11)
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


interface HandoffHost {
  sessions: Record<string, { directory: string; metadata: JSONRecord; messages: JSONRecord[]; parentID?: string }>
  taskCalls: number
  lostConsumeAcknowledgement?: boolean
}

async function runProcess(argv: string[], input = "", cwd?: string): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  return runFixtureProcess(argv, input, { cwd, env: { ...process.env, CONCORD_DB_PATH: process.env.CONCORD_DB_PATH } })
}

async function git(cwd: string, ...args: string[]): Promise<void> {
  const result = await runProcess(["git", ...args], "", cwd)
  expect(result.exitCode, `git ${args.join(" ")}: ${result.stderr}`).toBe(0)
}

// realCoreRunner spawns the real core binary against the real store for
// every transport leg, honoring the child working directory the boot flow
// resolves (work-resume and session-prepare run from the session's own
// directories) and the session-prepare seams: a HOME holding the primary agent
// definitions and a fake `opencode` probe on PATH. This is the boundary the
// acceptance claims: no canned answer stands between the boot flow and the
// core, and no fixture session is launched by anyone — both coordinator
// sessions are host-started before the route runs.
function realCoreRunner(binary: string, dbPath: string, childEnv: Record<string, string>, captured: { invoke?: JSONRecord }, host?: HandoffHost): CoreRunner & {
  run(argv: string[], input: string, signal?: AbortSignal, options?: { cwd?: string }): ReturnType<CoreRunner["run"]>
} {
  return {
    async run(argv: string[], input: string, signal?: AbortSignal, options?: { cwd?: string }) {
      if (argv[1] === "invoke") captured.invoke = JSON.parse(input) as JSONRecord
      const result = await runFixtureProcess([binary, ...argv.slice(1)], input, { cwd: options?.cwd, env: { ...process.env, CONCORD_DB_PATH: dbPath, ...childEnv }, signal })
      if (host?.lostConsumeAcknowledgement && argv[1] === "invoke" && captured.invoke?.operation === "project_handoff_consume") {
        expect(result.exitCode, result.stderr).toBe(0)
        expect(JSON.parse(result.stdout).outcome).toBe("ok")
        host.lostConsumeAcknowledgement = false
        return { exitCode: 1, stdout: "", stderr: "synthetic lost handoff-consume acknowledgement" }
      }
      return result
    },
  } as never
}

// fakeHostControlPlane holds per-session live directories and participation
// metadata: moveSession performs the relocation the boot flow and the vacate
// mover request, the session route reads the directory and metadata back,
// and the PATCH route persists the managed Task scope the boot enrolls. The
// seed names where each session runs before its first move.
async function fakeHostControlPlane(seed: Array<[string, string]>, runner: CoreRunner, retained?: HandoffHost) {
  const host = retained ?? { sessions: {}, taskCalls: 0 }
  for (const [id, directory] of seed) host.sessions[id] ??= { directory, metadata: {}, messages: [] }
  configureHostLease({
    release: { coreBinary: "concord", releaseRoot: "/releases/v11.0.0" },
    runner: {
      async run(argv: string[]) {
        if (argv[1] === "host-lease") {
          return { exitCode: 0, stdout: JSON.stringify({ pid: process.pid, pid_start: 1, release_root: "/releases/v11.0.0", core_binary: "concord", schema_version: 93, manifest_digest: manifestDigest, directory: seed[0][1], worktree: seed[0][1] }), stderr: "" }
        }
        throw new Error("unexpected host-lease invocation: " + argv.join(" "))
      },
    } as never,
  })
  configureConcordAdapter({ runner })
  return await ConcordAdapterPlugin({
    directory: seed[0][1],
    client: {
      _client: {
        get: async ({ url, path }: { url?: string; path?: Record<string, unknown> }) => {
          const id = String(path?.id ?? "")
          const session = host.sessions[id]
          if (!session) return { data: { message: "session missing" }, response: new Response(null, { status: 404 }) }
          if (url === SESSION_MESSAGES_ROUTE) return { data: session.messages, response: new Response("[]", { status: 200 }) }
          return { data: { id, ...session }, response: new Response(null, { status: 200 }) }
        },
        post: async ({ url, body }: { url: string; path?: Record<string, unknown>; body?: unknown }) => {
          if (url === SESSION_MESSAGES_ROUTE) throw new Error("handoff performs zero subtask writes")
          expect(url).toBe(MOVE_SESSION_ROUTE)
          const { sessionID, destination } = body as { sessionID: string; destination: { directory: string } }
          host.sessions[sessionID].directory = destination.directory
          return { data: null, response: new Response(null, { status: 204 }) }
        },
        patch: async ({ url, path, body }: { url: string; path?: Record<string, unknown>; body?: unknown }) => {
          expect(url).toBe(SESSION_ROUTE)
          const id = String(path?.id ?? "")
          const patch = body as { metadata?: Record<string, unknown>; title?: string }
          if (patch.metadata !== undefined) host.sessions[id].metadata = patch.metadata
          return { response: new Response(null, { status: 200 }) }
        },
      },
    },
    serverUrl: new URL("https://example.invalid"),
  } as never)
}

const contextFor = (sessionID: string, directory: string) =>
  ({
    sessionID,
    messageID: "message-1",
    agent: AGENT,
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
  repoReceive: string
  handoffID: string
  captured: { invoke?: JSONRecord }
  childEnv: Record<string, string>
  host: HandoffHost
}

// bootHandoffFixture builds the real two-repository store: one product, two
// Projects with canonical locators in distinct repositories, one shared work
// item holding both memberships, one approved contract, and the recorded
// handoff the source session addressed to the receiving Project through the
// real invoke route. The session-prepare seams (primary agent definitions in a
// fixture HOME and a fake `opencode` probe on PATH) let the real boot flow
// run its prepare step without an installed host.
async function bootHandoffFixture(root: string, restartWitness = false): Promise<HandoffFixture> {
  const binRoot = join(root, "bin")
  const homeRoot = join(root, "home")
  await mkdir(binRoot)
  await mkdir(join(homeRoot, ".config", "opencode", "agents"), { recursive: true })
  await writeFile(join(homeRoot, ".config", "opencode", "agents", `${AGENT}.md`), `${AGENT} synthetic definition\n`)
  const probe = join(binRoot, "opencode")
  await Bun.write(probe, `#!/bin/sh\necho '{"agent":{"${AGENT}":{"mode":"all","disable":false}}}'\n`)
  await chmod(probe, 0o755)
  const childEnv = { HOME: homeRoot, PATH: `${binRoot}:${process.env.PATH ?? ""}` }
  if (restartWitness) {
    // The oracle producer uses the installed toolchain, not the module
    // launcher's cache under the fixture's synthetic HOME.
    const toolchain = await runProcess(["go", "env", "GOROOT"], "", join(import.meta.dir, "..", ".."))
    expect(toolchain.exitCode, toolchain.stderr).toBe(0)
    childEnv.PATH = `${binRoot}:${join(toolchain.stdout.trim(), "bin")}:${process.env.PATH ?? ""}`
  }
  const binary = join(binRoot, "concord")
  const build = await runProcess(["go", "build", "-o", binary, "./cmd/concord"], "", join(import.meta.dir, "..", ".."))
  expect(build.exitCode, `go build: ${build.stderr}`).toBe(0)
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
    if (restartWitness) {
      await Bun.write(join(repo, "AGENTS.md"), repo === repoReceive ? "Synthetic receiver instructions: preserve the bounded handoff.\n" : "Synthetic source instructions: record the bounded handoff.\n")
      await mkdir(join(repo, "oraclekit"))
      await mkdir(join(repo, "test-oracle"))
      await Bun.write(join(repo, "go.mod"), "module example.test/handoff\n\ngo 1.26.0\ntoolchain go1.26.7\n")
      await Bun.write(join(repo, "oraclekit/oracle.go"), "package oraclekit\n\nfunc HandoffReady() bool { return true }\n")
      await Bun.write(join(repo, "oraclekit/oracle_test.go"), 'package oraclekit\n\nimport "testing"\n\nfunc TestHandoffReady(t *testing.T) { if !HandoffReady() { t.Fatal("handoff not ready") } }\n')
      await Bun.write(join(repo, "test-oracle/manifest.json"), JSON.stringify({ kind: "go_top_level_v1", package_cwd: "oraclekit", test_files: ["oraclekit/oracle_test.go"], fixture_files: [], cases: { "case:handoff": ["TestHandoffReady"] } }))
    }
    await git(repo, "init", "--quiet", "--initial-branch=main")
    await git(repo, "config", "user.email", "test@example.invalid")
    await git(repo, "config", "user.name", "Synthetic Test")
    await git(repo, "add", ".")
    await git(repo, "commit", "--quiet", "-m", "fixture")
    await git(repo, "remote", "add", "origin", "https://example.invalid/synthetic.git")
    // The bootstrap preflight and the resume freshness sample both fetch
    // origin's default branch, so the remote maps onto a local bare
    // repository through insteadOf: every fetch stays hermetic while the
    // URL stays one ResolveProject accepts.
    const bareOrigin = join(root, `origin-${basename(repo)}.git`)
    await git(root, "init", "--quiet", "--bare", "--initial-branch=main", bareOrigin)
    await git(repo, "config", `url.${bareOrigin}.insteadOf`, "https://example.invalid/synthetic.git")
    await git(repo, "push", "--quiet", "origin", "main")
    await git(repo, "fetch", "--quiet", "origin")
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
    defect_intake: { failure_shape: "handoff-e2e-route", reproduction: "run the synthetic handoff route", searched: "the synthetic store", related_defect_ids: [] },
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
    public_key: fixturePublicKeyBase64(PRIVATE_SEED),
    capabilities: ["product_read", "work_transition", "work_relate"],
    product_scope: [PRODUCT_ID],
    project_scope: [SOURCE_PROJECT, RECEIVE_PROJECT],
    agent_scope: [AGENT],
  })
  const captured: { invoke?: JSONRecord } = {}
  const host: HandoffHost = { sessions: {}, taskCalls: 0 }
  const sourceWorktree = (bootstrap.worktree as JSONRecord).path as string
  // The host session route answers each session's current directory: the
  // source session runs in its claimed worktree and the receiving sessions
  // are host-started in the receiving repository before any route runs.
  // The real core runner serves every transport leg from here on.
  const seed: Array<[string, string]> = [[SOURCE_SESSION, sourceWorktree], [RECEIVE_SESSION, repoReceive], ...(restartWitness ? [] : [[RECEIVE_SESSION_B, repoReceive] as [string, string]])]
  await fakeHostControlPlane(seed, realCoreRunner(binary, dbPath, childEnv, captured, host), host)
  const sourceContext = contextFor(SOURCE_SESSION, sourceWorktree)
  const invoke = (toolName: string, operation: string, input: JSONRecord, callContext: any) => invokeConcordOperation(toolName, { operation, input } as any, callContext)
  const version = () => dbValue(dbPath, `SELECT version FROM work_items WHERE id='${workID}'`).version as number

  const memberships = await invoke("concord_work_relate", "set_memberships", { work_id: workID, expected_version: await version(), memberships: [{ project_id: SOURCE_PROJECT, role: "primary" }, { project_id: RECEIVE_PROJECT, role: "secondary" }], idempotency_key: "handoff-e2e-memberships" }, sourceContext)
  expect(memberships.outcome, JSON.stringify(memberships)).toBe("ok")
  const walk = async (actionID: string, fields: JSONRecord) => {
    const response = await invoke("concord_work_transition", "workflow_action", { work_id: workID, expected_version: await version(), action_id: actionID, idempotency_key: `handoff-e2e-${actionID}`, fields }, sourceContext)
    expect(response.outcome, `${actionID}: ${JSON.stringify(response)}`).toBe("ok")
  }
  // CD-0198 D2: the merged diagnosis record carries the reproduction,
  // alignment search, and root cause sections the three retired records
  // carried separately.
  await walk("record_diagnosis", { reproduction: "The defect reproduces from a clean store.", searched: "Searched the backlog for duplicate defect work.", outcome: "none_found", root_cause: "The defect is the fold's own pool." })
  const domainList = await invoke("concord_domain", "list", { product_id: PRODUCT_ID, page: { cursor: null, limit: 10 } }, sourceContext)
  expect(domainList.outcome).toBe("ok")
  await walk("approve_contract", {
    premise: "Deliver the bounded cross-Project handoff.",
    outcome_predicates: [HANDOFF_PREDICATE],
    required_evidence: [],
    route_conventions: [],
    spec_mandate: [],
    law_modifies: [],
    // The consequence_v1 head requires the decision fact; none declares
    // engineering work, which records without a consent question.
    decision_domains: [],
    architecture_binding: {
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
    changes: ["adapter/opencode: resume route"],
    idempotency_key: "handoff-e2e-record",
  }, sourceContext)
  expect(recorded.outcome, `record: ${JSON.stringify(recorded)}`).toBe("ok")
  const handoffID = (recorded.result as JSONRecord).handoff_id as string
  expect(handoffID).toBeTruthy()
  return { root, binary, dbPath, workID, sourceWorktree, repoReceive, handoffID, captured, childEnv, host }
}

afterEach(async () => {
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
  configureConcordAdapter({ reset: true })
  configureHostLease({ reset: true })
  await ConcordAdapterPlugin({})
  configureHostLease({ reset: true })
  delete process.env.CONCORD_DB_PATH
})

routeDeclaration("host-started session receives real core context without out-of-surface writes", async () => {
  const fixturePath = process.env.TEST_CONCORD_SESSION_CONTEXT_FIXTURE
  if (!fixturePath) {
    const fixture = await bootHandoffFixture(await fixtureTempRoot("session-context"))
    const path = join(fixture.root, "session-context.json")
    await Bun.write(path, JSON.stringify(fixture))
    // The production runner inherits the native process environment. Supply
    // the fixture database before startup, not only in JavaScript process.env.
    const result = await runFixtureProcess([process.execPath, "test", import.meta.path, "-t", "^host-started session receives real core context without out-of-surface writes$"], "", {
      env: { ...process.env, CONCORD_DB_PATH: fixture.dbPath, TEST_CONCORD_SESSION_CONTEXT_FIXTURE: path },
    })
    process.stdout.write(result.stdout)
    process.stderr.write(result.stderr)
    expect(result.exitCode, `${result.stdout}\n${result.stderr}`).toBe(0)
    return
  }
  const fixture = await Bun.file(fixturePath).json() as HandoffFixture
  const { binary, dbPath, workID, sourceWorktree, childEnv, captured, host } = fixture
  process.env.CONCORD_DB_PATH = dbPath
  const snapshot = (): Record<string, string[]> => {
    const db = new Database(dbPath, { readonly: true })
    try {
      const tables = db.query("SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name").all() as Array<{ name: string }>
      return Object.fromEntries([
        ["schema", db.query("SELECT * FROM sqlite_schema ORDER BY name").all().map((row) => JSON.stringify(row))],
        ...tables.map(({ name }) => [name, db.query(`SELECT * FROM "${name.replaceAll('"', '""')}"`).all().map((row) => JSON.stringify(row)).sort()]),
      ])
    } finally {
      db.close()
    }
  }
  const core = realCoreRunner(binary, dbPath, childEnv, captured, host)
  const calls: string[] = []
  const runner: CoreRunner = {
    async run(argv, input, signal, options?: { cwd?: string }) {
      const before = snapshot()
      const result = await core.run(argv, input, signal, options)
      expect(result.exitCode, `${argv[1]}: ${result.stderr}`).toBe(0)
      calls.push(argv[1])
      const after = snapshot()
      if (argv[1] === "claim-landing") {
        expect(JSON.parse(input)).toMatchObject({ work_id: workID, session_ref: SOURCE_SESSION, landed_directory: sourceWorktree })
        expect(after).not.toEqual(before)
      } else if (argv[1] === "session-prepare") {
        // CD-0061 D4's identity evidence is an ordinary, non-durable commit
        // (session_identity.go), not a Product/workflow projection mutation.
        const receipts = after.effect_receipts.filter((row) => !before.effect_receipts.includes(row))
        expect(receipts).toHaveLength(1)
        const receipt = JSON.parse(receipts[0])
        const actorFields = [["principal_ref", "principal/orchestrator"], ["client_ref", "client/concord-session"], ["agent_ref", `agent/${AGENT}`], ["session_ref", `session/${PRODUCT_ID}`]]
        const actorBytes = "actor-v1\0" + actorFields.map(([key, value]) => `${key}=${Buffer.byteLength(value)}:${value}|`).join("")
        expect(JSON.parse(receipts[0])).toEqual({
          event_id: expect.stringMatching(new RegExp(`^session-orchestrator-identity:${workID}:\\d+$`)),
          effect_seq: expect.any(Number), kind: "session.orchestrator_identity_asserted",
          subject_type: "session", subject_id: workID,
          actor: `actor:${createHash("sha256").update(actorBytes).digest("hex")}`, payload_version: 1,
          payload_digest: expect.stringMatching(/^sha256:[0-9a-f]{64}$/),
          delivery_assertion: 0,
          relation_seq: null,
        })
        before.effect_receipts.push(receipts[0])
        before.effect_receipts.sort()
        expect(before.store_sequences).toHaveLength(2)
        before.store_sequences = before.store_sequences.map((row) => {
          const sequence = JSON.parse(row)
          if (sequence.name !== "effect") return row
          expect(sequence.last + 1).toBe(receipt.effect_seq)
          return JSON.stringify({ ...sequence, last: receipt.effect_seq })
        }).sort()
        expect(before.store_revision).toHaveLength(1)
        before.store_revision = before.store_revision.map((row) => {
          const revision = JSON.parse(row)
          return JSON.stringify({ ...revision, revision: revision.revision + 1 })
        })
        expect(after).toEqual(before)
      } else {
        expect(after, `${argv[1]} must not write outside the typed landing`).toEqual(before)
      }
      return result
    },
  }
  configureCoreBinary(binary)
  const plugin = await fakeHostControlPlane([[SOURCE_SESSION, sourceWorktree]], runner, host)
  expect(host.sessions[SOURCE_SESSION].metadata).toEqual({})
  const boot = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(SOURCE_SESSION, sourceWorktree)))
  expect(boot.outcome, JSON.stringify(boot)).toBe("ok")
  expect(boot).toMatchObject({ work_id: workID, worktree_path: sourceWorktree, session_id: SOURCE_SESSION })
  expect(calls).toContain("work-resume")
  expect(calls).toContain("session-prepare")
  expect(calls).toContain("claim-landing")

  const beforeDelivery = snapshot()
  const expected = await runner.run([binary, "continuity-block", sourceWorktree], "", new AbortController().signal)
  const packet = JSON.parse(expected.stdout)
  expect(packet).toMatchObject({ schema_version: "1.0", session_type: "operator", product_id: PRODUCT_ID, work_id: workID, manifest_digest: manifestDigest })
  expect(packet.continuity.pinned.work_pin.work_id).toBe(workID)
  expect(concordBinaryPath()).toBe(binary)
  expect(await hostControlPlane().hasManagedParent(SOURCE_SESSION)).toBe(false)
  expect(await hostControlPlane().sessionDirectory(SOURCE_SESSION)).toBe(sourceWorktree)
  const output = { system: ["host system prefix"] }
  await plugin["experimental.chat.system.transform"]({ sessionID: SOURCE_SESSION }, output)
  expect(output.system[0]).toContain(`<!-- concord:continuity:v1 -->\n${expected.stdout}\n<!-- /concord:continuity:v1 -->`)
  expect(snapshot()).toEqual(beforeDelivery)

  // A real but unclaimed directory must not inherit the previous work's packet.
  host.sessions[SOURCE_SESSION].directory = fixture.repoReceive
  const unclaimed = { system: ["unclaimed host system"] }
  await plugin["experimental.chat.system.transform"]({ sessionID: SOURCE_SESSION }, unclaimed)
  expect(unclaimed.system).toEqual(["unclaimed host system"])
  expect(snapshot()).toEqual(beforeDelivery)
  console.log("[pass] host-started session receives real core context without out-of-surface writes")
})

routeDeclaration("capture replays real core effects without duplicate identity", async () => {
  const fixture = await bootHandoffFixture(await fixtureTempRoot("capture-replay"))
  const { binary, dbPath, repoReceive, childEnv, captured, host } = fixture
  const core = realCoreRunner(binary, dbPath, childEnv, captured, host)
  const bootstrapCalls: Array<{ input: JSONRecord; exitCode: number; stdout: string }> = []
  configureConcordAdapter({ runner: {
    async run(argv, input, signal, options) {
      const result = await core.run(argv, input, signal, options)
      if (argv[1] === "work-bootstrap") bootstrapCalls.push({ input: JSON.parse(input), exitCode: result.exitCode, stdout: result.stdout })
      return result
    },
  } })
  const retained = (key: string) => {
    const db = new Database(dbPath, { readonly: true })
    try {
      return {
        counts: db.query(`SELECT (SELECT count(*) FROM work_items) AS work,
          (SELECT count(*) FROM bootstrap_operations) AS journal,
          (SELECT count(*) FROM worktree_claims) AS claim`).get() as { work: number; journal: number; claim: number },
        journal: db.query(`SELECT operation_id, product_id, project_id, work_id, request_digest, request_json, state
          FROM bootstrap_operations WHERE idempotency_key=?`).all(key) as JSONRecord[],
        work: db.query(`SELECT id FROM work_items WHERE id IN
          (SELECT work_id FROM bootstrap_operations WHERE idempotency_key=?)`).all(key) as JSONRecord[],
        claim: db.query(`SELECT op_id, work_id, project_id, set_id, pinned_path, state FROM worktree_claims WHERE op_id IN
          (SELECT operation_id FROM bootstrap_operations WHERE idempotency_key=?)`).all(key) as JSONRecord[],
        entry: db.query(`SELECT claim_op_id, project_id, set_id, path, state FROM worktree_entries WHERE claim_op_id IN
          (SELECT operation_id FROM bootstrap_operations WHERE idempotency_key=?)`).all(key) as JSONRecord[],
      }
    } finally { db.close() }
  }
  const assertIdentity = (result: JSONRecord, state: ReturnType<typeof retained>) => {
    expect(state.journal).toHaveLength(1)
    expect(state.work).toHaveLength(1)
    expect(state.claim).toHaveLength(1)
    const journal = state.journal[0]
    const claim = state.claim[0]
    expect(result).toMatchObject({ product_id: PRODUCT_ID, project_id: RECEIVE_PROJECT, work_id: journal.work_id, worktree_path: claim.pinned_path })
    expect(state.work[0].id).toBe(journal.work_id)
    expect(journal).toMatchObject({ product_id: PRODUCT_ID, project_id: RECEIVE_PROJECT })
    expect(claim).toMatchObject({ op_id: journal.operation_id, work_id: journal.work_id, project_id: RECEIVE_PROJECT })
  }
  for (const failFinalize of [false, true]) {
    const key = failFinalize ? "capture-failed-finalize" : "capture-unlanded"
    const sessionID = failFinalize ? RECEIVE_SESSION_B : RECEIVE_SESSION
    const args = { title: "Synthetic capture replay", value_statement: "A failed start preserves one capture and claim.", kind: "task", task: "Verify exact replay from the landed context.", idempotency_key: key }
    const before = retained(key)
    expect(before.journal).toEqual([])
    if (failFinalize) {
      const db = new Database(dbPath)
      try {
        db.exec(`CREATE TRIGGER refuse_capture_finalize BEFORE UPDATE OF state ON bootstrap_operations
          WHEN NEW.idempotency_key='capture-failed-finalize' AND NEW.state='completed'
          BEGIN SELECT RAISE(ABORT, 'synthetic capture finalize failure'); END`)
      } finally { db.close() }
    }
    let refused: JSONRecord
    try {
      refused = parseToolResult(await work_start.execute(args as any, contextFor(sessionID, repoReceive)))
    } finally {
      if (failFinalize) {
        const db = new Database(dbPath)
        try { db.exec("DROP TRIGGER refuse_capture_finalize") } finally { db.close() }
      }
    }
    expect(refused).toMatchObject({ outcome: "error", error: { kind: failFinalize ? "bootstrap_failure" : "session_directory_mismatch", effect_state: "partial" } })
    expect(bootstrapCalls).toHaveLength(failFinalize ? 3 : 1)
    const child = bootstrapCalls.at(-1)!
    if (failFinalize) {
      expect([1, 2]).toContain(child.exitCode)
      expect(JSON.parse(child.stdout)).toMatchObject({ schema_version: "1.0", outcome: "error", effect_state: "partial" })
      expect(refused.error.message).toContain("synthetic capture finalize failure")
      expect(host.sessions[sessionID].directory).toBe(repoReceive)
    } else {
      expect(child.exitCode).toBe(0)
      expect(host.sessions[sessionID].directory).toBe(refused.worktree_path)
    }
    const capturedState = retained(key)
    assertIdentity(refused, capturedState)
    expect(capturedState.counts).toEqual({ work: before.counts.work + 1, journal: before.counts.journal + 1, claim: before.counts.claim + 1 })
    if (failFinalize) {
      expect(capturedState.journal[0].state).toBe("native_ready")
      expect(capturedState.claim[0].state).toBe("pending")
      expect(capturedState.entry).toEqual([])
      expect(JSON.parse(child.stdout)).toMatchObject({ operation_id: capturedState.journal[0].operation_id, work_id: capturedState.journal[0].work_id, worktree_path: capturedState.claim[0].pinned_path })
      // The native directory exists but its entry did not commit. Recover
      // from the canonical root before the host can land in an active claim.
      const recovered = parseToolResult(await work_start.execute(args as any, contextFor(sessionID, repoReceive)))
      expect(recovered).toMatchObject({ outcome: "error", work_id: refused.work_id, worktree_path: refused.worktree_path, error: { kind: "session_directory_mismatch", effect_state: "partial" } })
      expect(retained(key).counts).toEqual(capturedState.counts)
      expect(bootstrapCalls.at(-1)!.input).toEqual(child.input)
    } else {
      expect(capturedState.journal[0].state).toBe("completed")
      expect(capturedState.entry).toHaveLength(1)
    }
    const replay = parseToolResult(await work_start.execute(args as any, contextFor(sessionID, refused.worktree_path as string)))
    expect(replay).toMatchObject({ outcome: "ok", work_id: refused.work_id, worktree_path: refused.worktree_path })
    const replayedState = retained(key)
    assertIdentity(replay, replayedState)
    expect(replayedState.counts).toEqual(capturedState.counts)
    expect(replayedState.work).toEqual(capturedState.work)
    expect(replayedState.journal).toEqual([{ ...capturedState.journal[0], state: "completed" }])
    expect(replayedState.claim).toEqual([{ ...capturedState.claim[0], state: "verified" }])
    expect(replayedState.entry).toEqual([{ claim_op_id: capturedState.claim[0].op_id, project_id: RECEIVE_PROJECT, set_id: capturedState.claim[0].set_id, path: refused.worktree_path, state: "active" }])
    const replayedChild = bootstrapCalls.at(-1)!
    expect(replayedChild.input).toEqual(child.input)
    expect(replayedChild.input.idempotency_key).toBe(key)
    expect(replayedChild.exitCode).toBe(0)
    expect(JSON.parse(replayedChild.stdout)).toMatchObject({ replayed: true, operation_id: capturedState.journal[0].operation_id, work_id: refused.work_id })
  }
})

routeDeclaration("boots, consumes, and retires through the real core routes", async () => {
  const root = await fixtureTempRoot("handoff-e2e")
  try {
    const fixture = await bootHandoffFixture(root)
    const { dbPath, workID, sourceWorktree, repoReceive, handoffID, captured } = fixture
    const invoke = (toolName: string, operation: string, input: JSONRecord, callContext: any) => invokeConcordOperation(toolName, { operation, input } as any, callContext)
    const occupantOf = (projectID: string) =>
      dbValue(dbPath, `SELECT COALESCE((SELECT group_concat(o.session_ref, ',') FROM worktree_occupancy o WHERE o.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id), '') AS occupant FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE c.work_id='${workID}' AND c.project_id='${projectID}' AND e.state='active'`).occupant as string

    // The source session boots through the real route: the resume read finds
    // its bootstrap claim, both readbacks prove the placement, and the
    // verified claim landing records the source session's occupancy.
    const sourceBoot = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(SOURCE_SESSION, sourceWorktree)))
    expect(sourceBoot.outcome, JSON.stringify(sourceBoot)).toBe("ok")
    expect(occupantOf(SOURCE_PROJECT)).toBe(SOURCE_SESSION)

    // The receiving session's boot names the bounded job: the first
    // work_start from the repository root claims the receiving Project's
    // worktree through the real work-resume route, but the tool context has
    // not landed, so the metadata-only move refuses closed and arms no
    // claim (issue #1322).
    const unlanded = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(RECEIVE_SESSION, repoReceive)))
    expect(unlanded.outcome, JSON.stringify(unlanded)).toBe("error")
    expect(unlanded.error.kind).toBe("session_directory_mismatch")
    expect(unlanded.error.effect_state).toBe("partial")
    // The refusal names the next-turn recovery opportunity, keeps the replay
    // behind an actual target-context confirmation, and asserts neither an
    // armed turn-move boundary nor successful placement.
    expect(unlanded.error.message).toContain("this refusal arms no turn-move boundary")
    expect(unlanded.error.message).toContain("one automatic continuation")
    expect(unlanded.error.message).toContain("replay this same work_start request")
    const receiveWorktree = unlanded.worktree_path as string
    expect(receiveWorktree).toBeTruthy()
    // The bootstrap recorded the claim's occupancy from creation (CD-0179),
    // and the refused boot consumed nothing: the addressed handoff stands
    // unconsumed while the boot reported no success and armed no claim.
    expect(dbValue(dbPath, `SELECT state FROM project_handoffs WHERE handoff_id='${handoffID}'`).state).toBe("recorded")

    // The replay from the landed context runs the whole receiving flow
    // through executeWorkStart: the verified landing records this session's
    // occupancy, and the boot consumes exactly the handoff the resume
    // rendered, by its own id, through the authenticated boundary — with no
    // operator copying and no separate canned consume call.
    const boot = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(RECEIVE_SESSION, receiveWorktree)))
    expect(boot.outcome, JSON.stringify(boot)).toBe("ok")
    const rendered = boot.project_handoff as JSONRecord
    expect(rendered.handoff_id).toBe(handoffID)
    expect(rendered.bounded_job).toContain("verify the receiving repository's adapter surface")
    expect(rendered.source_project_id).toBe(SOURCE_PROJECT)
    expect(rendered.next_action).toContain("consume the handoff")
    expect(boot.worktree_path).toBe(receiveWorktree)
    expect(occupantOf(RECEIVE_PROJECT)).toBe(RECEIVE_SESSION)

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
    // The automatic consume key is the bounded hash of the complete consume
    // identity: the real route sent exactly the construction that stays
    // inside the payload contract's 128-character idempotency_key bound for
    // every accepted work_id and handoff_id length (the reviewed overflow
    // reproduced at a 123-character work_id).
    const sentKey = (captured.invoke?.input as JSONRecord).idempotency_key as string
    expect(sentKey).toBe(projectHandoffConsumeKey(workID, handoffID))
    expect(sentKey.length).toBeLessThanOrEqual(128)
    expect(sentKey).toMatch(/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/)
    // The core recorded the bind on the shared work.
    const bind = dbValue(dbPath, `SELECT state, consumed_by_session_ref AS consumer FROM project_handoffs WHERE handoff_id='${handoffID}'`)
    expect(bind.state).toBe("consumed")
    expect(bind.consumer).toBe(RECEIVE_SESSION)

    // Lost-response recovery through the real routes: the consume committed
    // but its success never reached the receiving session, so it replays
    // work_start. The resume re-renders the receiver's own consumed bind —
    // the bounded job and next action ride again — and the consume replays
    // idempotently against the standing bind instead of re-binding or
    // refusing closed.
    const replay = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(RECEIVE_SESSION, receiveWorktree)))
    expect(replay.outcome, JSON.stringify(replay)).toBe("ok")
    const renderedAgain = replay.project_handoff as JSONRecord
    expect(renderedAgain.handoff_id).toBe(handoffID)
    expect(renderedAgain.bounded_job).toContain("verify the receiving repository's adapter surface")
    expect(renderedAgain.next_action).toContain("consume the handoff")
    expect(dbValue(dbPath, `SELECT state, consumed_by_session_ref AS consumer FROM project_handoffs WHERE handoff_id='${handoffID}'`).consumer).toBe(RECEIVE_SESSION)
    expect(occupantOf(RECEIVE_PROJECT)).toBe(RECEIVE_SESSION)

    // A cold second coordinator session of the receiving Project (CD-0182
    // D5 amendment): it holds no verified placement when its resume read
    // runs, so the consumed frontier — the shared bind the first session
    // recorded — renders nothing on that read. The boot must land, re-read
    // the addressed handoff, render the bounded job, and resolve the
    // standing bind, instead of admitting the session with its job lost or
    // prescribing a fresh handoff and a second session for the same
    // repository. The cold start from the repository root refuses on the
    // metadata-only move first, exactly as the first receiver's did.
    const unlandedB = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(RECEIVE_SESSION_B, repoReceive)))
    expect(unlandedB.outcome, JSON.stringify(unlandedB)).toBe("error")
    expect(unlandedB.error.kind).toBe("session_directory_mismatch")
    expect(unlandedB.error.message).toContain("this refusal arms no turn-move boundary")
    const bootB = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(RECEIVE_SESSION_B, receiveWorktree)))
    expect(bootB.outcome, JSON.stringify(bootB)).toBe("ok")
    const renderedB = bootB.project_handoff as JSONRecord
    expect(renderedB.handoff_id).toBe(handoffID)
    expect(renderedB.bounded_job).toContain("verify the receiving repository's adapter surface")
    expect(renderedB.next_action).toContain("consume the handoff")
    expect(bootB.worktree_path).toBe(receiveWorktree)
    // The second session's landing records its own placement alongside the
    // first receiver's: both placed sessions of the Project hold occupancy.
    expect(String(occupantOf(RECEIVE_PROJECT))).toContain(RECEIVE_SESSION)
    expect(String(occupantOf(RECEIVE_PROJECT))).toContain(RECEIVE_SESSION_B)
    // The second boot resolved the standing shared bind through the
    // authenticated boundary: the consume names the second session, the
    // rendered handoff id, and the bounded consume key — and records no
    // second consumed event, so the bind still names the first consumer.
    expect(captured.invoke?.tool).toBe("concord_work_transition")
    expect(captured.invoke?.operation).toBe("project_handoff_consume")
    expect((captured.invoke?.call_envelope ?? {} as JSONRecord).session_ref).toBe(RECEIVE_SESSION_B)
    expect((captured.invoke?.input as JSONRecord).handoff_id).toBe(handoffID)
    expect((captured.invoke?.input as JSONRecord).idempotency_key).toBe(projectHandoffConsumeKey(workID, handoffID))
    const consumedEvents = dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE kind='work.project_handoff_consumed' AND subject_id='${workID}'`).n as number
    expect(consumedEvents).toBe(1)
    expect(dbValue(dbPath, `SELECT consumed_by_session_ref AS consumer FROM project_handoffs WHERE handoff_id='${handoffID}'`).consumer).toBe(RECEIVE_SESSION)

    // The source session retires through the real owning routes: the
    // session_vacate mutation records the relocation request toward the
    // derived registered main checkout, the adapter's host mover performs
    // the relocation, and the readback-verified landing records itself
    // through the adapter-only vacate-landing verb, releasing the source
    // session's occupancy rows.
    const vacated = parseToolResult(await work_transition.execute({ request: { operation: "session_vacate", input: { idempotency_key: "handoff-e2e-vacate" } } } as any, contextFor(SOURCE_SESSION, sourceWorktree)))
    expect(vacated.outcome, JSON.stringify(vacated)).toBe("ok")
    const destination = (vacated.result as JSONRecord).destination_directory as string
    expect(destination).toBeTruthy()
    const landings = dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE kind='work.session_vacate_landed' AND subject_id='${workID}'`).n as number
    expect(landings).toBe(1)
    expect(occupantOf(SOURCE_PROJECT)).toBe("")

    // The retirement read derives READY_TO_CLOSE_OR_REPLACE from the
    // verified facts only: the recorded addressed handoff, the preserved
    // source worktree, the stopped session-owned execution, and the
    // verified vacate landing.
    const retirement = await invoke("concord_work_trace", "project_retirement", { work_id: workID }, contextFor(SOURCE_SESSION, destination))
    expect(retirement.outcome, JSON.stringify(retirement)).toBe("ok")
    const readiness = retirement.result as JSONRecord
    expect(readiness.state).toBe("ready_to_close_or_replace")
    expect(readiness.recorded_handoff).toBe(true)
    expect(readiness.artifacts_preserved).toBe(true)
    expect(readiness.workers_stopped).toBe(true)
    expect(readiness.vacate_landed).toBe(true)
    expect(readiness.blockers).toEqual([])

    // Retirement derives from facts; the shared work's lifecycle and the
    // receiving session's bind stand untouched, and the retiring session
    // completes or cancels nothing.
    expect(dbValue(dbPath, `SELECT lifecycle FROM work_items WHERE id='${workID}'`).lifecycle).toBe("in_progress")
    expect(dbValue(dbPath, `SELECT state FROM project_handoffs WHERE handoff_id='${handoffID}'`).state).toBe("consumed")
  } finally {
  }
})

const RESTART_WITNESS = "restarts the handoff host and continues the original receiver without duplicate sessions"

function parseToolResult(result: any): JSONRecord {
  try {
    return JSON.parse(String(result.output).split("\n")[0]) as JSONRecord
  } catch {
    throw new Error(`unparsable tool output: ${String(result.output).slice(0, 2000)}`)
  }
}

function parseWorktreeVerifyResult(value: unknown) {
  if (!value || typeof value !== "object" || Array.isArray(value)
    || !("exit_code" in value) || typeof value.exit_code !== "number"
    || !("tracked_files_changed" in value) || typeof value.tracked_files_changed !== "boolean") {
    throw new Error(`invalid worktree verification result: ${JSON.stringify(value)}`)
  }
  const result = { exit_code: value.exit_code, tracked_files_changed: value.tracked_files_changed }
  if (!("oracle" in value)) return { ...result, oracle: undefined }
  const oracle = value.oracle
  if (!oracle || typeof oracle !== "object" || Array.isArray(oracle)
    || !("qualification" in oracle) || typeof oracle.qualification !== "string"
    || !("run_ref" in oracle) || typeof oracle.run_ref !== "string") {
    throw new Error(`invalid native oracle result: ${JSON.stringify(oracle)}`)
  }
  return { ...result, oracle: { qualification: oracle.qualification, run_ref: oracle.run_ref } }
}

function assertReceiverCounts(fixture: HandoffFixture): void {
  const { dbPath, workID, host } = fixture
  expect(dbValue(dbPath, `SELECT count(*) AS n FROM worktree_claims WHERE work_id='${workID}' AND project_id='${RECEIVE_PROJECT}'`).n).toBe(1)
  expect(dbValue(dbPath, `SELECT count(*) AS n FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE c.work_id='${workID}' AND e.project_id='${RECEIVE_PROJECT}'`).n).toBe(1)
  expect(Object.keys(host.sessions).filter((id) => id.startsWith(RECEIVE_SESSION))).toEqual([RECEIVE_SESSION])
}

// Each child loads the adapter and host fixture anew. Only the temporary store
// and the synthetic host's serialized sessions/transcripts cross the boundary.
routeDeclaration(RESTART_WITNESS, async () => {
  const restartPath = process.env.HANDOFF_RESTART_FIXTURE
  if (!restartPath) {
    const fixture = await bootHandoffFixture(await fixtureTempRoot("handoff-restart"), true)
    const { sourceWorktree, workID, host, repoReceive, dbPath } = fixture
    const sourceContext = contextFor(SOURCE_SESSION, sourceWorktree)
    const sourceBoot = parseToolResult(await work_start.execute({ work_id: workID } as any, sourceContext))
    expect(sourceBoot.outcome, JSON.stringify(sourceBoot)).toBe("ok")
    // The receiving session is host-started in the receiving repository:
    // the fixture host placed it at the receiving checkout before
    // any route ran, and no Concord surface launches or names a session.
    expect(host.sessions[RECEIVE_SESSION].directory).toBe(repoReceive)
    const vacated = parseToolResult(await work_transition.execute({ request: { operation: "session_vacate", input: { idempotency_key: "restart-source-vacate" } } } as any, sourceContext))
    expect(vacated.outcome, JSON.stringify(vacated)).toBe("ok")
    expect(host.sessions[SOURCE_SESSION].directory).toBe(vacated.result.destination_directory)
    expect(dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE kind='work.session_vacate_landed' AND subject_id='${workID}'`).n).toBe(1)
    expect(dbValue(dbPath, `SELECT count(*) AS n FROM worktree_occupancy WHERE session_ref='${SOURCE_SESSION}'`).n).toBe(0)
    const path = join(fixture.root, "restart-fixture.json")
    await Bun.write(path, JSON.stringify(fixture))
    for (const phase of ["land", "continue", "resume"]) {
      const result = await runFixtureProcess([process.execPath, "test", import.meta.path, "-t", RESTART_WITNESS], "", {
        cwd: join(import.meta.dir, "..", ".."),
        env: { ...process.env, HANDOFF_RESTART_FIXTURE: path, HANDOFF_RESTART_PHASE: phase },
      })
      // Forward every child case's evidence, including failures, to the owner.
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      expect(result.exitCode, `${phase}: ${result.stdout}\n${result.stderr}`).toBe(0)
    }
    const retained = await Bun.file(path).json()
    assertReceiverCounts(retained)
    expect(retained.host.taskCalls).toBe(0)
    expect(retained.host.sessions[RECEIVE_SESSION].messages).toEqual(retained.originalParentMessages)
    return
  }

  const fixture = await Bun.file(restartPath).json()
  const { binary, dbPath, workID, handoffID, repoReceive, host, childEnv, captured } = fixture
  process.env.CONCORD_DB_PATH = dbPath
  const phase = process.env.HANDOFF_RESTART_PHASE
  const runner = realCoreRunner(binary, dbPath, childEnv, captured, host)
  await fakeHostControlPlane([[RECEIVE_SESSION, host.sessions[RECEIVE_SESSION].directory]], runner, host)
  const invoke = (tool: string, request: any, context: any) => invokeConcordOperation(tool, request, context)
  const handoff = () => dbValue(dbPath, `SELECT state, consumed_by_session_ref AS consumer FROM project_handoffs WHERE handoff_id='${handoffID}'`)
  const consumedCount = () => dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE kind='work.project_handoff_consumed' AND subject_id='${workID}'`).n
  const expectedHandoff = {
    handoff_id: handoffID,
    bounded_job: "verify the receiving repository's adapter surface",
    next_action: "consume the handoff and run the bounded job",
    source_project_id: SOURCE_PROJECT,
  }

  if (phase === "land") {
    const refused = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(RECEIVE_SESSION, repoReceive)))
    expect(refused.error.kind, JSON.stringify(refused)).toBe("session_directory_mismatch")
    expect(handoff()).toEqual({ state: "recorded", consumer: "" })
    expect(consumedCount()).toBe(0)
    fixture.receiveWorktree = refused.worktree_path
    expect(host.sessions[RECEIVE_SESSION].directory).toBe(fixture.receiveWorktree)
    assertReceiverCounts(fixture)
    host.lostConsumeAcknowledgement = true
    const uncertain = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(RECEIVE_SESSION, fixture.receiveWorktree)))
    expect(uncertain.outcome, JSON.stringify(uncertain)).toBe("error")
    expect(host.lostConsumeAcknowledgement).toBe(false)
    expect(handoff()).toEqual({ state: "consumed", consumer: RECEIVE_SESSION })
    expect(consumedCount()).toBe(1)
    fixture.originalHandoff = handoff()
  } else if (phase === "continue") {
    const context = contextFor(RECEIVE_SESSION, fixture.receiveWorktree)
    const replay = parseToolResult(await work_start.execute({ work_id: workID } as any, context))
    expect(replay.outcome, JSON.stringify(replay)).toBe("ok")
    expect(replay.project_handoff).toMatchObject(expectedHandoff)
    expect(replay.worktree_path).toBe(fixture.receiveWorktree)
    expect(replay.project_id).toBe(RECEIVE_PROJECT)
    expect(handoff()).toEqual(fixture.originalHandoff)
    expect(consumedCount()).toBe(1)
    const head = await runProcess(["git", "rev-parse", "HEAD"], "", fixture.receiveWorktree)
    expect(head.exitCode, head.stderr).toBe(0)
    const owner = { owner_id: "owner:handoff", domain_id: `product-root:${PRODUCT_ID}`, mechanism: { project_id: RECEIVE_PROJECT, path: "oraclekit/oracle.go", entry_point: "HandoffReady" }, obligation: "The synthetic handoff check passes.", predicate_ids: [HANDOFF_PREDICATE.predicate_id], law_bindings: [] }
    const oracleCase = { case_id: "case:handoff", owner_id: owner.owner_id, entry_point: "HandoffReady", input_class: "synthetic handoff", expected_state: "The handoff is ready.", control_ids: ["control:handoff"] }
    const control = { control_id: "control:handoff", owner_id: owner.owner_id, predicate_ids: owner.predicate_ids, case_ids: [oracleCase.case_id], recipe_source: { kind: "repository_file", project_id: RECEIVE_PROJECT, path: "test-oracle/manifest.json", commit_oid: head.stdout.trim() }, argv: ["go", "test", "-count=1", "-run", "^(TestHandoffReady)$", "."], cwd: "oraclekit", expected_result: "pass", required_evidence_role: "reported" }
    const subject = await invoke("concord_work_transition", { operation: "worktree_verify", input: { work_id: workID, idempotency_key: "handoff-oracle-subject", command: ["git", "status", "--porcelain"] } }, context)
    expect(subject.outcome, JSON.stringify(subject)).toBe("ok")
    const subjectResult = parseWorktreeVerifyResult(subject.result)
    expect(subjectResult.exit_code).toBe(0)
    expect(subjectResult.tracked_files_changed).toBe(false)
    const prepare = await invoke("concord_work_transition", { operation: "worktree_verify", input: { work_id: workID, idempotency_key: "handoff-oracle-prepare", requested_budget_seconds: 300, oracle: { phase: "prepare", expected_contract_version: 1, control_bundle: { owner, cases: [oracleCase], control } } } }, context)
    expect(prepare.outcome, JSON.stringify(prepare)).toBe("ok")
    const prepareResult = parseWorktreeVerifyResult(prepare.result)
    if (!prepareResult.oracle) throw new Error("native preparation returned no oracle result")
    expect(prepareResult.oracle.qualification).toBe("ready")
    const preparationRef = prepareResult.oracle.run_ref
    const executionInput = { work_id: workID, idempotency_key: "handoff-native-execute", requested_budget_seconds: 300, oracle: { phase: "execute", preparation_run_ref: preparationRef } }
    const execution = await invoke("concord_work_transition", { operation: "worktree_verify", input: executionInput }, context)
    expect(execution.outcome, JSON.stringify(execution)).toBe("ok")
    const nativeResult = parseWorktreeVerifyResult(execution.result)
    expect(nativeResult.oracle?.qualification).toBe("pass")
    host.sessions[RECEIVE_SESSION].messages = [{ info: { id: "original-parent-message", sessionID: RECEIVE_SESSION, role: "assistant" }, parts: [{ type: "text", text: expectedHandoff.bounded_job }] }]
    fixture.originalParentMessages = host.sessions[RECEIVE_SESSION].messages
    fixture.executionInput = executionInput
    fixture.nativeResult = nativeResult
  } else {
    expect(phase).toBe("resume")
    const context = contextFor(RECEIVE_SESSION, fixture.receiveWorktree)
    const boot = parseToolResult(await work_start.execute({ work_id: workID } as any, context))
    expect(boot.outcome, JSON.stringify(boot)).toBe("ok")
    expect(boot.project_handoff).toMatchObject(expectedHandoff)
    const result = await invoke("concord_work_transition", { operation: "worktree_verify", input: fixture.executionInput }, context)
    expect(result.outcome, JSON.stringify(result)).toBe("ok")
    expect(parseWorktreeVerifyResult(result.result)).toEqual(fixture.nativeResult)
    expect(host.sessions[RECEIVE_SESSION].messages).toEqual(fixture.originalParentMessages)
    expect(host.taskCalls).toBe(0)
    expect(handoff()).toEqual(fixture.originalHandoff)
    expect(consumedCount()).toBe(1)
  }
  assertReceiverCounts(fixture)
  await Bun.write(restartPath, JSON.stringify(fixture))
})
