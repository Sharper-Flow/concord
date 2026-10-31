import { afterEach, expect, test } from "bun:test"
import { Database } from "bun:sqlite"
import { createHash } from "node:crypto"
import { chmod, mkdir, writeFile } from "node:fs/promises"
import { basename, join } from "node:path"
import { fixtureTempRoot, requireOwnedFixtureRun, runFixtureProcess } from "./fixture-temp-root"
requireOwnedFixtureRun()
import ConcordAdapterPlugin from "./concord-plugin"
import { configureConcordAdapter, invokeConcordOperation, work_transition } from "./concord"
import { concordBinaryPath, configureCoreBinary, type CoreRunner } from "./core-runtime"
import { configureHostLease } from "./host-lease"
import { manifestDigest } from "./generated-contracts"
import { hostControlPlane, SESSION_ROUTE, SESSION_MESSAGES_ROUTE } from "./move-session"
import { dbValue, fixturePublicKeyBase64, type JSONRecord } from "./end-to-end.fixture"

// The core owns capture, session identity, continuity and handoff state. Git
// and the fixture host own checkout creation and session placement. No SQL
// fixture rows stand in for a production admission or preservation check.
// A clean checkout needs Go to build the real core; absence reports a skip.
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

// Every core transport leg uses the real store. Session preparation runs in
// the host checkout with synthetic installed agent definitions and registry.
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

// The host starts sessions in their checkouts. Concord may read their live
// directories and update participation metadata, but must never move them.
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
        post: async ({ url }: { url: string; path?: Record<string, unknown>; body?: unknown }) => {
          if (url === SESSION_MESSAGES_ROUTE) throw new Error("handoff performs zero subtask writes")
          throw new Error(`Concord must not create or relocate host sessions: ${url}`)
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
  receiveWorktree: string
  artifactPath: string
  artifactRef: string
  handoffID: string
  captured: { invoke?: JSONRecord }
  childEnv: Record<string, string>
  host: HandoffHost
}

// Capture and all workflow/handoff mutations cross the authenticated adapter.
// The fixture creates ordinary Git worktrees, not Concord ownership rows.
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
      await Bun.write(join(repo, "go.mod"), "module example.test/handoff\n\ngo 1.26.0\ntoolchain go1.26.10\n")
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
    // Native verification retains real default-branch refs without network IO.
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
  await runJSON("client-register", {
    client_ref: CLIENT_REF,
    key_id: "handoff-e2e-key",
    principal_ref: "operator-1",
    public_key: fixturePublicKeyBase64(PRIVATE_SEED),
    capabilities: ["product_read", "work_define", "work_transition", "work_relate"],
    product_scope: [PRODUCT_ID],
    project_scope: [SOURCE_PROJECT, RECEIVE_PROJECT],
    agent_scope: [AGENT],
  })
  const captured: { invoke?: JSONRecord } = {}
  const host: HandoffHost = { sessions: {}, taskCalls: 0 }
  const runner = realCoreRunner(binary, dbPath, childEnv, captured, host)
  await fakeHostControlPlane([[SOURCE_SESSION, repoSource]], runner, host)
  const invoke = (toolName: string, operation: string, input: JSONRecord, callContext: any) => invokeConcordOperation(toolName, { operation, input } as any, callContext)
  const captureInput = {
    title: "Synthetic handoff route",
    value_statement: "The bounded handoff crosses Projects without operator copying.",
    kind: "bug", task: "Exercise the handoff route.", project_ids: [SOURCE_PROJECT],
    idempotency_key: "handoff-e2e-capture",
    defect_intake: { failure_shape: "handoff-e2e-route", reproduction: "run the synthetic handoff route", searched: "the synthetic store", related_defect_ids: [] },
  }
  const capture = await invoke("concord_work_define", "capture", captureInput, contextFor(SOURCE_SESSION, repoSource))
  expect(capture.outcome, JSON.stringify(capture)).toBe("ok")
  const workID = capture.changed_refs.find((ref: JSONRecord) => ref.entity_kind === "work_item").id as string
  expect(workID).toBeTruthy()
  const replay = await invoke("concord_work_define", "capture", captureInput, contextFor(SOURCE_SESSION, repoSource))
  expect(replay.outcome, JSON.stringify(replay)).toBe("ok")
  expect(replay.replayed).toBe(true)
  expect(replay.changed_refs).toEqual(capture.changed_refs)
  expect(dbValue(dbPath, "SELECT count(*) AS n FROM work_items").n).toBe(1)
  const sourceWorktree = join(root, "source-checkout")
  const receiveWorktree = join(root, "receive-checkout")
  await git(repoSource, "worktree", "add", "-b", `work/${workID}`, sourceWorktree)
  await git(repoReceive, "worktree", "add", "-b", `work/${workID}`, receiveWorktree)
  host.sessions[SOURCE_SESSION].directory = sourceWorktree
  const seed: Array<[string, string]> = [[SOURCE_SESSION, sourceWorktree], [RECEIVE_SESSION, receiveWorktree], ...(restartWitness ? [] : [[RECEIVE_SESSION_B, receiveWorktree] as [string, string]])]
  await fakeHostControlPlane(seed, runner, host)
  const sourceContext = contextFor(SOURCE_SESSION, sourceWorktree)
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
  const artifactPath = join(sourceWorktree, "README.md")
  const artifactRef = `sha256:${createHash("sha256").update(new Uint8Array(await Bun.file(artifactPath).arrayBuffer())).digest("hex")}:${artifactPath}`
  const recorded = await invoke("concord_work_transition", "project_handoff_record", {
    work_id: workID,
    target_project_id: RECEIVE_PROJECT,
    bounded_job: "verify the receiving repository's adapter surface",
    next_action: "consume the handoff and run the bounded job",
    changes: ["adapter/opencode: handoff route"],
    artifact_refs: [artifactRef],
    idempotency_key: "handoff-e2e-record",
  }, sourceContext)
  expect(recorded.outcome, `record: ${JSON.stringify(recorded)}`).toBe("ok")
  const handoffID = (recorded.result as JSONRecord).handoff_id as string
  expect(handoffID).toBeTruthy()
  return { root, binary, dbPath, workID, sourceWorktree, repoReceive, receiveWorktree, artifactPath, artifactRef, handoffID, captured, childEnv, host }
}

async function prepareSession(fixture: HandoffFixture, directory: string): Promise<void> {
  const runner = realCoreRunner(fixture.binary, fixture.dbPath, fixture.childEnv, fixture.captured, fixture.host)
  const result = await runner.run([fixture.binary, "session-prepare"], JSON.stringify({ product_id: PRODUCT_ID, work_id: fixture.workID, agent: AGENT }), undefined, { cwd: directory })
  expect(result.exitCode, result.stderr).toBe(0)
  expect(JSON.parse(result.stdout)).toMatchObject({ product_id: PRODUCT_ID, work_id: fixture.workID, directory, agent: AGENT })
}

async function readAddressedHandoff(fixture: HandoffFixture, sessionID: string): Promise<JSONRecord> {
  const directory = await hostControlPlane().sessionDirectory(sessionID)
  expect(directory).toBe(fixture.receiveWorktree)
  const continuity = await invokeConcordOperation("concord_work_trace", { operation: "continuity", input: { work_id: fixture.workID } } as any, contextFor(sessionID, directory!))
  expect(continuity.outcome, JSON.stringify(continuity)).toBe("ok")
  expect(continuity.result.work_id).toBe(fixture.workID)
  expect(continuity.result.pinned.work_pin.work_id).toBe(fixture.workID)
  const handoff = continuity.result.pinned.pending_project_handoff
  expect(handoff, JSON.stringify(continuity)).toMatchObject({ handoff_id: fixture.handoffID, target_project_id: RECEIVE_PROJECT })
  return handoff
}

afterEach(async () => {
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
      expect(after, `${argv[1]} must not write to the database`).toEqual(before)
      return result
    },
  }
  configureCoreBinary(binary)
  const plugin = await fakeHostControlPlane([[SOURCE_SESSION, sourceWorktree]], runner, host)
  expect(host.sessions[SOURCE_SESSION].metadata).toEqual({})
  const prepared = await runner.run([binary, "session-prepare"], JSON.stringify({ product_id: PRODUCT_ID, work_id: workID, agent: AGENT }), new AbortController().signal, { cwd: sourceWorktree })
  expect(prepared.exitCode, prepared.stderr).toBe(0)
  const boot = JSON.parse(prepared.stdout)
  expect(boot).toMatchObject({ work_id: workID, product_id: PRODUCT_ID, directory: sourceWorktree, agent: AGENT })
  expect(boot.prompt).toContain(workID)
  expect(calls).toContain("session-prepare")
  expect(host.sessions[SOURCE_SESSION].directory).toBe(sourceWorktree)

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

  // A different Project's main checkout must not inherit this work's packet.
  host.sessions[SOURCE_SESSION].directory = fixture.repoReceive
  const unclaimed = { system: ["unclaimed host system"] }
  await plugin["experimental.chat.system.transform"]({ sessionID: SOURCE_SESSION }, unclaimed)
  expect(unclaimed.system).toEqual(["unclaimed host system"])
  expect(snapshot()).toEqual(beforeDelivery)
  console.log("[pass] host-started session receives real core context without out-of-surface writes")
})

routeDeclaration("capture replays real core effects without duplicate identity", async () => {
  const fixture = await bootHandoffFixture(await fixtureTempRoot("capture-replay"))
  expect(dbValue(fixture.dbPath, "SELECT count(*) AS n FROM work_items").n).toBe(1)
  const head = await runProcess(["git", "branch", "--show-current"], "", fixture.sourceWorktree)
  expect(head.exitCode, head.stderr).toBe(0)
  expect(head.stdout.trim()).toBe(`work/${fixture.workID}`)
})

routeDeclaration("prepares, consumes, and retires through the real core routes", async () => {
  const root = await fixtureTempRoot("handoff-e2e")
  try {
    const fixture = await bootHandoffFixture(root)
    const { dbPath, workID, sourceWorktree, receiveWorktree, handoffID, captured, host } = fixture
    const invoke = (toolName: string, operation: string, input: JSONRecord, callContext: any) => invokeConcordOperation(toolName, { operation, input } as any, callContext)
    await prepareSession(fixture, sourceWorktree)
    await prepareSession(fixture, receiveWorktree)
    expect(dbValue(dbPath, `SELECT state FROM project_handoffs WHERE handoff_id='${handoffID}'`).state).toBe("recorded")
    const foreign = await invoke("concord_work_transition", "project_handoff_consume", { work_id: workID, handoff_id: handoffID, idempotency_key: "handoff-foreign-project" }, contextFor(SOURCE_SESSION, sourceWorktree))
    expect(foreign.outcome, JSON.stringify(foreign)).toBe("error")
    expect(foreign.error.effect_state).toBe("none")
    expect(dbValue(dbPath, `SELECT state FROM project_handoffs WHERE handoff_id='${handoffID}'`).state).toBe("recorded")
    const receiveContext = contextFor(RECEIVE_SESSION, receiveWorktree)
    const rendered = await readAddressedHandoff(fixture, RECEIVE_SESSION)
    expect(rendered.handoff_id).toBe(handoffID)
    expect(rendered.bounded_job).toContain("verify the receiving repository's adapter surface")
    expect(rendered.source_project_id).toBe(SOURCE_PROJECT)
    expect(rendered.next_action).toContain("consume the handoff")
    expect(rendered.artifact_refs).toEqual([fixture.artifactRef])
    const consumeKey = "handoff-e2e-consume-receiver"
    const consumed = await consumeHandoff(workID, rendered.handoff_id, consumeKey, receiveContext)
    expect(consumed, JSON.stringify(consumed)).toMatchObject({ outcome: "ok", result: { handoff_id: handoffID } })
    expect(host.sessions[RECEIVE_SESSION].directory).toBe(receiveWorktree)

    // The consume carries the continuity-rendered id and the host's actual
    // directory through the authenticated adapter boundary.
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
    const sentKey = (captured.invoke?.input as JSONRecord).idempotency_key as string
    expect(sentKey).toBe(consumeKey)
    expect(sentKey.length).toBeLessThanOrEqual(128)
    expect(sentKey).toMatch(/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/)
    // The core recorded the bind on the shared work.
    const bind = dbValue(dbPath, `SELECT state, consumed_by_session_ref AS consumer FROM project_handoffs WHERE handoff_id='${handoffID}'`)
    expect(bind.state).toBe("consumed")
    expect(bind.consumer).toBe(RECEIVE_SESSION)

    // The same receiver re-reads its bounded job and replays the same bind.
    const renderedAgain = await readAddressedHandoff(fixture, RECEIVE_SESSION)
    expect(renderedAgain.handoff_id).toBe(handoffID)
    expect(renderedAgain.bounded_job).toContain("verify the receiving repository's adapter surface")
    expect(renderedAgain.next_action).toContain("consume the handoff")
    expect(dbValue(dbPath, `SELECT state, consumed_by_session_ref AS consumer FROM project_handoffs WHERE handoff_id='${handoffID}'`).consumer).toBe(RECEIVE_SESSION)
    const replay = await consumeHandoff(workID, renderedAgain.handoff_id, consumeKey, receiveContext)
    expect(replay).toMatchObject({ outcome: "ok", replayed: true, result: { handoff_id: handoffID } })

    // A second host-started receiver resolves the same work-and-Project bind.
    await prepareSession(fixture, receiveWorktree)
    const renderedB = await readAddressedHandoff(fixture, RECEIVE_SESSION_B)
    expect(renderedB.handoff_id).toBe(handoffID)
    expect(renderedB.bounded_job).toContain("verify the receiving repository's adapter surface")
    expect(renderedB.next_action).toContain("consume the handoff")
    const consumeKeyB = "handoff-e2e-consume-receiver-b"
    const consumedB = await consumeHandoff(workID, renderedB.handoff_id, consumeKeyB, contextFor(RECEIVE_SESSION_B, receiveWorktree))
    expect(consumedB).toMatchObject({ outcome: "ok", result: { handoff_id: handoffID, already_consumed: true } })
    expect(host.sessions[RECEIVE_SESSION_B].directory).toBe(receiveWorktree)
    expect(captured.invoke?.tool).toBe("concord_work_transition")
    expect(captured.invoke?.operation).toBe("project_handoff_consume")
    expect((captured.invoke?.call_envelope ?? {} as JSONRecord).session_ref).toBe(RECEIVE_SESSION_B)
    expect((captured.invoke?.input as JSONRecord).handoff_id).toBe(handoffID)
    expect((captured.invoke?.input as JSONRecord).idempotency_key).toBe(consumeKeyB)
    const consumedEvents = dbValue(dbPath, `SELECT count(*) AS n FROM effect_receipts WHERE kind='work.project_handoff_consumed' AND subject_id='${workID}'`).n as number
    expect(consumedEvents).toBe(1)
    expect(dbValue(dbPath, `SELECT consumed_by_session_ref AS consumer FROM project_handoffs WHERE handoff_id='${handoffID}'`).consumer).toBe(RECEIVE_SESSION)

    // Retirement checks handoff and artifacts. Host
    // checkout placement is not a Concord completion predicate.
    const retirement = await invoke("concord_work_trace", "project_retirement", { work_id: workID }, contextFor(SOURCE_SESSION, sourceWorktree))
    expect(retirement.outcome, JSON.stringify(retirement)).toBe("ok")
    const readiness = retirement.result as JSONRecord
    expect(readiness.state).toBe("ready_to_close_or_replace")
    expect(readiness.recorded_handoff).toBe(true)
    expect(readiness.artifacts_preserved).toBe(true)
    expect(readiness.blockers).toEqual([])
    expect(host.sessions[SOURCE_SESSION].directory).toBe(sourceWorktree)

    const artifact = await Bun.file(fixture.artifactPath).text()
    try {
      await Bun.write(fixture.artifactPath, "tampered handoff artifact\n")
      const damaged = await invoke("concord_work_trace", "project_retirement", { work_id: workID }, contextFor(SOURCE_SESSION, sourceWorktree))
      expect(damaged.outcome, JSON.stringify(damaged)).toBe("ok")
      expect(damaged.result.state).toBe("pending")
      expect(damaged.result.artifacts_preserved).toBe(false)
      expect(damaged.result.blockers.length).toBeGreaterThan(0)
    } finally {
      await Bun.write(fixture.artifactPath, artifact)
    }

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

async function consumeHandoff(workID: string, handoffID: string, key: string, context: any): Promise<JSONRecord> {
  return parseToolResult(await work_transition.execute({ request: { operation: "project_handoff_consume", input: { work_id: workID, handoff_id: handoffID, idempotency_key: key } } }, context))
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
  const { dbPath, host, receiveWorktree } = fixture
  expect(dbValue(dbPath, "SELECT count(*) AS n FROM work_items").n).toBe(1)
  expect(Object.keys(host.sessions).filter((id) => id.startsWith(RECEIVE_SESSION))).toEqual([RECEIVE_SESSION])
  expect(host.sessions[RECEIVE_SESSION].directory).toBe(receiveWorktree)
}

// Each child loads the adapter and host fixture anew. Only the temporary store
// and the synthetic host's serialized sessions/transcripts cross the boundary.
routeDeclaration(RESTART_WITNESS, async () => {
  const restartPath = process.env.HANDOFF_RESTART_FIXTURE
  if (!restartPath) {
    const fixture = await bootHandoffFixture(await fixtureTempRoot("handoff-restart"), true)
    const { sourceWorktree, receiveWorktree, host } = fixture
    await prepareSession(fixture, sourceWorktree)
    expect(host.sessions[RECEIVE_SESSION].directory).toBe(receiveWorktree)
    expect(host.sessions[SOURCE_SESSION].directory).toBe(sourceWorktree)
    const path = join(fixture.root, "restart-fixture.json")
    await Bun.write(path, JSON.stringify(fixture))
    for (const phase of ["consume", "continue", "recover"]) {
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
  const { binary, dbPath, workID, handoffID, host, childEnv, captured } = fixture
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

  if (phase === "consume") {
    await prepareSession(fixture, fixture.receiveWorktree)
    expect(handoff()).toEqual({ state: "recorded", consumer: "" })
    expect(consumedCount()).toBe(0)
    expect(host.sessions[RECEIVE_SESSION].directory).toBe(fixture.receiveWorktree)
    assertReceiverCounts(fixture)
    const rendered = await readAddressedHandoff(fixture, RECEIVE_SESSION)
    expect(rendered).toMatchObject(expectedHandoff)
    host.lostConsumeAcknowledgement = true
    const uncertain = await consumeHandoff(workID, rendered.handoff_id, "handoff-restart-consume", contextFor(RECEIVE_SESSION, fixture.receiveWorktree))
    expect(uncertain, JSON.stringify(uncertain)).toMatchObject({ outcome: "error", error: { kind: "operation_conflict", adapter_reason: "unknown_effect", effect_state: "possible" } })
    expect(uncertain.error.message).toContain("synthetic lost handoff-consume acknowledgement")
    expect(host.lostConsumeAcknowledgement).toBe(false)
    expect(handoff()).toEqual({ state: "consumed", consumer: RECEIVE_SESSION })
    expect(consumedCount()).toBe(1)
    fixture.originalHandoff = handoff()
  } else if (phase === "continue") {
    const context = contextFor(RECEIVE_SESSION, fixture.receiveWorktree)
    await prepareSession(fixture, fixture.receiveWorktree)
    const rendered = await readAddressedHandoff(fixture, RECEIVE_SESSION)
    expect(rendered).toMatchObject(expectedHandoff)
    const replay = await consumeHandoff(workID, rendered.handoff_id, "handoff-restart-consume", context)
    expect(replay).toMatchObject({ outcome: "ok", replayed: true, result: { handoff_id: handoffID } })
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
    expect(phase).toBe("recover")
    const context = contextFor(RECEIVE_SESSION, fixture.receiveWorktree)
    await prepareSession(fixture, fixture.receiveWorktree)
    expect(await readAddressedHandoff(fixture, RECEIVE_SESSION)).toMatchObject(expectedHandoff)
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
