import { afterEach, expect, test } from "bun:test"
import { Database } from "bun:sqlite"
import { createPrivateKey, createPublicKey } from "node:crypto"
import { chmod, mkdir, writeFile } from "node:fs/promises"
import { basename, join } from "node:path"
import { fixtureTempRoot, requireOwnedFixtureRun, runFixtureProcess } from "./fixture-temp-root"
requireOwnedFixtureRun()
import ConcordAdapterPlugin from "./concord-plugin"
import { configureConcordAdapter, invokeConcordOperation, projectHandoffConsumeKey, resetConsumedProjectHandoffs, work_start, work_transition } from "./concord"
import { configureCoreBinary, type DispatchRunner } from "./dispatch"
import { configureHostLease } from "./host-lease"
import { resetClaimedWorktrees } from "./claimed-worktree"
import { resetTurnMoveBoundaries } from "./turn-move-boundary"
import { manifestDigest } from "./generated-contracts"
import { MOVE_SESSION_ROUTE, SESSION_ROUTE } from "./move-session"

// The Project-session handoff acceptance runs on the real boundary: a real
// core binary against a real store, two Projects in two repositories, the
// real boot route (work_start -> work-resume -> move readbacks ->
// claim-landing -> addressed consume) for both coordinator sessions, the
// real session_vacate mutation with its readback-verified landing
// (CD-0190), and the real project_retirement read deriving
// READY_TO_CLOSE_OR_REPLACE. A clean checkout has no binary on PATH; declare
// the test skipped when neither CONCORD_BIN nor a Go toolchain can produce
// one, so a suite run without the toolchain reports a skip, never a silent
// pass.
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
const RECEIVE_SESSION_B = "session-handoff-receive-b"
const AGENT = "concord-implement"
const LANE_AGENTS = ["concord-research", "concord-implement", "concord-design", "concord-review", "concord-verify"]
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
  return runFixtureProcess(argv, input, { cwd, env: { ...process.env, CONCORD_DB_PATH: process.env.CONCORD_DB_PATH } })
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
// every transport leg, honoring the child working directory the boot flow
// resolves (work-resume and session-prepare run from the session's own
// directories) and the session-prepare seams: a HOME holding the lane agent
// definitions and a fake `opencode` probe on PATH. This is the boundary the
// acceptance claims: no canned answer stands between the boot flow and the
// core.
function realCoreRunner(binary: string, dbPath: string, childEnv: Record<string, string>, captured: { invoke?: JSONRecord }): DispatchRunner {
  return {
    async run(argv: string[], input: string, signal?: AbortSignal, options?: { cwd?: string }) {
      if (argv[1] === "invoke") captured.invoke = JSON.parse(input) as JSONRecord
      return runFixtureProcess([binary, ...argv.slice(1)], input, { cwd: options?.cwd, env: { ...process.env, CONCORD_DB_PATH: dbPath, ...childEnv }, signal })
    },
  } as never
}

// fakeHostControlPlane holds per-session live directories and participation
// metadata: moveSession performs the relocation the boot flow and the vacate
// mover request, the session route reads the directory and metadata back,
// and the PATCH route persists the managed Task scope the boot enrolls. The
// seed names where each session runs before its first move.
async function fakeHostControlPlane(seed: Array<[string, string]>, runner: DispatchRunner) {
  const directories = new Map(seed)
  const metadata = new Map<string, Record<string, unknown>>()
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
  await ConcordAdapterPlugin({
    client: {
      _client: {
        get: async ({ path }: { path?: Record<string, unknown> }) => {
          const id = String(path?.id ?? "")
          if (!directories.has(id)) return { data: { message: "session missing" }, response: new Response(null, { status: 404 }) }
          return { data: { id, directory: directories.get(id), metadata: metadata.get(id) ?? {} }, response: new Response(null, { status: 200 }) }
        },
        post: async ({ url, body }: { url: string; body?: unknown }) => {
          expect(url).toBe(MOVE_SESSION_ROUTE)
          const { sessionID, destination } = body as { sessionID: string; destination: { directory: string } }
          directories.set(sessionID, destination.directory)
          return { data: null, response: new Response(null, { status: 204 }) }
        },
        patch: async ({ url, path, body }: { url: string; path?: Record<string, unknown>; body?: unknown }) => {
          expect(url).toBe(SESSION_ROUTE)
          const id = String(path?.id ?? "")
          const patch = body as { metadata?: Record<string, unknown>; title?: string }
          if (patch.metadata !== undefined) metadata.set(id, patch.metadata)
          return { response: new Response(null, { status: 200 }) }
        },
      },
    },
    serverUrl: new URL("http://127.0.0.1:4096"),
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
}

// bootHandoffFixture builds the real two-repository store: one product, two
// Projects with canonical locators in distinct repositories, one shared work
// item holding both memberships, one approved contract, and the recorded
// handoff the source session addressed to the receiving Project through the
// real invoke route. The session-prepare seams (lane agent definitions in a
// fixture HOME and a fake `opencode` probe on PATH) let the real boot flow
// run its prepare step without an installed host.
async function bootHandoffFixture(root: string): Promise<HandoffFixture> {
  const binRoot = join(root, "bin")
  const homeRoot = join(root, "home")
  await mkdir(binRoot)
  await mkdir(join(homeRoot, ".config", "opencode", "agents"), { recursive: true })
  for (const lane of LANE_AGENTS) {
    await writeFile(join(homeRoot, ".config", "opencode", "agents", `${lane}.md`), `${lane} synthetic definition\n`)
  }
  const probe = join(binRoot, "opencode")
  await Bun.write(probe, `#!/bin/sh\necho '{"agent":{"${AGENT}":{"mode":"all","disable":false}}}'\n`)
  await chmod(probe, 0o755)
  const childEnv = { HOME: homeRoot, PATH: `${binRoot}:${process.env.PATH ?? ""}` }
  let binBuildRoot = ""
  let binary = process.env.CONCORD_BIN ?? ""
  if (!binary) {
    binBuildRoot = binRoot
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
    agent_scope: [AGENT],
  })
  const captured: { invoke?: JSONRecord } = {}
  const sourceWorktree = (bootstrap.worktree as JSONRecord).path as string
  // The host session route answers each session's current directory: the
  // source session runs in its claimed worktree and the receiving session
  // starts in the receiving repository. The real core runner serves every
  // transport leg from here on.
  await fakeHostControlPlane([[SOURCE_SESSION, sourceWorktree], [RECEIVE_SESSION, repoReceive], [RECEIVE_SESSION_B, repoReceive]], realCoreRunner(binary, dbPath, childEnv, captured))
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
  return { root, binary, dbPath, workID, sourceWorktree, repoReceive, handoffID, captured }
}

afterEach(async () => {
  resetConsumedProjectHandoffs()
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
  configureConcordAdapter({ reset: true })
  configureHostLease({ reset: true })
  await ConcordAdapterPlugin({})
  configureHostLease({ reset: true })
  delete process.env.CONCORD_DB_PATH
})

routeDeclaration("boots, consumes, and retires through the real core routes", async () => {
  const root = await fixtureTempRoot("handoff-e2e")
  try {
    const fixture = await bootHandoffFixture(root)
    const { dbPath, workID, sourceWorktree, repoReceive, handoffID, captured } = fixture
    const parseToolResult = (result: any) => {
      try {
        return JSON.parse(String(result.output).split("\n")[0]) as JSONRecord
      } catch {
        throw new Error(`unparsable tool output: ${String(result.output).slice(0, 2000)}`)
      }
    }
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
    // The refusal names the next-turn recovery opportunity, keeps the replay
    // behind an actual target-context confirmation, and asserts neither an
    // armed turn-move boundary nor successful placement.
    expect(unlanded.error.message).toContain("this refusal arms no turn-move boundary")
    expect(unlanded.error.message).toContain("ask the operator to send the next message")
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
    const consumedEvents = dbValue(dbPath, `SELECT count(*) AS n FROM domain_events WHERE kind='work.project_handoff_consumed' AND subject_id='${workID}'`).n as number
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
    const landings = dbValue(dbPath, `SELECT count(*) AS n FROM domain_events WHERE kind='work.session_vacate_landed' AND subject_id='${workID}'`).n as number
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
