// Connected regression for repeated session_vacate of one work item by one
// session. The cycle runs the real core binary against a real store and the
// adapter's own host session mover and readback: worktree_claim moves the
// session into the claimed worktree, session_vacate records the operation and
// moves the session to the derived main checkout, work_start resumes
// read-only through work-resume, and a second vacate records its own event.
// A same-key retry after the host lands in main refuses before another move
// or occupancy change. The original Project claim survives both vacates, and
// the same work ID then claims in a second Project.
import { afterAll, afterEach, expect, test } from "bun:test"
import { Database } from "bun:sqlite"
import { createPrivateKey, createPublicKey } from "node:crypto"
import { chmod, mkdir, writeFile } from "node:fs/promises"
import { join } from "node:path"
import { beginOwnedFixture, runFixtureProcess, sweepPendingFixtureScopes } from "./fixture-lifecycle"
afterAll(() => sweepPendingFixtureScopes("run_end"))
import { configureConcordAdapter, invokeConcordOperation, work_start, work_transition } from "./concord"
import { configureCoreBinary } from "./dispatch"
import { hostControlPlane, MANAGED_TASK_SCOPE_KEY, MOVE_SESSION_ROUTE, SESSION_ROUTE } from "./move-session"
import { configureHostLease } from "./host-lease"
import { clearClaimedWorktree, pendingVacateDestination, resetClaimedWorktrees } from "./claimed-worktree"
import { resetTurnMoveBoundaries } from "./turn-move-boundary"

// The cycle drives the real core through its own runner, so argv[0] is
// replaced there. Bind the nominal path the transport resolves instead of
// the unstamped repository placeholder (CD-0111 D1), as the dispatch route
// test does.
configureCoreBinary("concord")

const PRODUCT_ID = "product-reoccupy"
const PROJECT_1 = "project-reoccupy-1"
const PROJECT_2 = "project-reoccupy-2"
const SESSION_ID = "reoccupy-session"
const MESSAGE_ID = "reoccupy-message"
const AGENT = "concord-implement"
const LANE_AGENTS = ["concord-research", "concord-implement", "concord-design", "concord-review", "concord-verify"]
const PRIVATE_SEED = new Uint8Array(32).fill(9)
const PRIVATE_KEY_PREFIX = Buffer.from("302e020100300506032b657004220420", "hex")

function publicKeyBase64(): string {
  const privateKey = createPrivateKey({ key: Buffer.concat([PRIVATE_KEY_PREFIX, Buffer.from(PRIVATE_SEED)]), format: "der", type: "pkcs8" })
  return createPublicKey(privateKey).export({ format: "der", type: "spki" }).subarray(-32).toString("base64")
}

async function runProcess(argv: string[], input = "", cwd?: string, env: Record<string, string> = {}): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  return runFixtureProcess(argv, input, { cwd, env: { ...process.env, ...env } })
}

async function git(cwd: string, ...args: string[]): Promise<string> {
  const result = await runProcess(["git", ...args], "", cwd)
  expect(result.exitCode, `git ${args.join(" ")}: ${result.stderr}`).toBe(0)
  return result.stdout.trim()
}

async function repositoryFixture(root: string, name: string): Promise<{ repo: string }> {
  const repo = join(root, name)
  await mkdir(join(repo, ".concord/docs/knowledge"), { recursive: true })
  await Bun.write(join(repo, ".concord/docs/knowledge", "manifest.json"), JSON.stringify({
    schema_version: "1.2",
    supported_kinds: [],
    indexed_kinds: [],
    knowledge_roots: [],
  }, null, 2))
  await Bun.write(join(repo, ".concord/docs/knowledge", "domain-registry.json"), JSON.stringify({
    schema_version: "1.0",
    product_key: PRODUCT_ID,
    root_domain_id: `product-root:${PRODUCT_ID}`,
    domains: [{ domain_id: `product-root:${PRODUCT_ID}`, name: "Synthetic root", purpose: "Synthetic test domain", status: "current", architecture_relations: [] }],
  }, null, 2))
  await Bun.write(join(repo, "opencode.jsonc"), JSON.stringify({ instructions: ["https://example.invalid/synthetic-instructions"] }))
  await Bun.write(join(repo, "README.md"), "synthetic vacate reoccupy fixture\n")
  await git(repo, "init", "--quiet", "--initial-branch=main")
  await git(repo, "config", "user.email", "test@example.invalid")
  await git(repo, "config", "user.name", "Synthetic Test")
  await git(repo, "add", ".")
  await git(repo, "commit", "--quiet", "-m", "fixture")
  await git(repo, "remote", "add", "origin", "https://example.invalid/synthetic.git")
  // The resume freshness sample fetches origin's default branch, so the
  // remote maps onto a local bare repository through insteadOf: the fetch
  // stays hermetic while the URL stays one ResolveProject accepts.
  await git(root, "init", "--quiet", "--bare", "--initial-branch=main", `${name}-origin.git`)
  await git(repo, "config", `url.${join(root, `${name}-origin.git`)}.insteadOf`, "https://example.invalid/synthetic.git")
  await git(repo, "push", "--quiet", "origin", "main")
  await git(repo, "fetch", "--quiet", "origin")
  await git(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
  return { repo }
}

function dbValue(dbPath: string, sql: string, ...params: string[]): any {
  const db = new Database(dbPath, { readonly: true })
  try {
    return db.query(sql).get(...params)
  } finally {
    db.close()
  }
}

function dbRows(dbPath: string, sql: string, ...params: string[]): any[] {
  const db = new Database(dbPath, { readonly: true })
  try {
    return db.query(sql).all(...params)
  } finally {
    db.close()
  }
}

function contextFor(directory: string): any {
  return {
    sessionID: SESSION_ID,
    messageID: MESSAGE_ID,
    agent: AGENT,
    directory,
    worktree: directory,
    abort: new AbortController().signal,
  }
}

// The suite's files share one process in an order no file controls, so leave
// every module-level seam clean on both sides of the run.
afterEach(async () => {
  resetClaimedWorktrees()
  resetTurnMoveBoundaries()
})
afterAll(() => configureHostLease({ reset: true }))

const connected =
  process.env.CONCORD_BIN || Bun.spawnSync(["go", "version"]).exitCode === 0
    ? test
    : test.skip

connected("vacate, work-resume, vacate in one session keeps one event per request", async () => {
  const scope = await beginOwnedFixture("concord-vacate-reoccupy-")
  await scope.run(async (root) => {
  const dbPath = join(root, "concord.db")
  const binRoot = join(root, "bin")
  const homeRoot = join(root, "home")
  const repoCheckout = join(root, "checkout")
  let binary = process.env.CONCORD_BIN ?? ""
  const previousSelectedProduct = process.env.CONCORD_SELECTED_PRODUCT_ID
  delete process.env.CONCORD_SELECTED_PRODUCT_ID
  // A wrapping zellij pane would append a rename warning to every work_start
  // result; the cycle owns its own host seams and no pane.
  const previousZellijPane = process.env.ZELLIJ_PANE_ID
  delete process.env.ZELLIJ_PANE_ID
  try {
    if (!binary) {
      await mkdir(binRoot, { recursive: true })
      binary = join(binRoot, "concord-core")
      const build = await runProcess(["go", "build", "-o", binary, "./cmd/concord"], "", join(import.meta.dir, "..", ".."))
      expect(build.exitCode, `go build: ${build.stderr}`).toBe(0)
    }
    const repo1 = await repositoryFixture(root, "repo-1")
    const repo2 = await repositoryFixture(root, "repo-2")
    await mkdir(repoCheckout, { recursive: true })

    // The core verifies lane agent identity from $HOME and probes the host
    // agent registry through `opencode debug config`. Both seams are
    // fixture-owned so the cycle never depends on an installed host.
    await mkdir(join(homeRoot, ".config", "opencode", "agents"), { recursive: true })
    for (const lane of LANE_AGENTS) {
      await writeFile(join(homeRoot, ".config", "opencode", "agents", `${lane}.md`), `${lane} synthetic definition\n`)
    }
    const probe = join(binRoot, "opencode")
    await Bun.write(probe, `#!/bin/sh\necho '{"agent":{"${AGENT}":{"mode":"all","disable":false}}}'\n`)
    await chmod(probe, 0o755)
    const childEnv = { HOME: homeRoot, PATH: `${binRoot}:${process.env.PATH ?? ""}`, CONCORD_DB_PATH: dbPath }

    const runCLI = (command: string, value: Record<string, unknown>, cwd?: string) => runProcess([binary, command], JSON.stringify(value), cwd, childEnv).then((result) => {
      expect(result.exitCode, `${command}: ${result.stderr}`).toBe(0)
      return JSON.parse(result.stdout.trim().split("\n").filter(Boolean).pop() as string)
    })

    await runCLI("product-create", {
      product_id: PRODUCT_ID,
      display_name: "Synthetic Reoccupy Product",
      stage_maturity: "prototype",
      stage_audience_commitment: "operator_only",
      project_id: PROJECT_1,
      project_display_name: "Synthetic Reoccupy Project",
      role: "primary",
    })
    const productVersion = dbValue(dbPath, "SELECT version FROM products WHERE id=?", PRODUCT_ID).version as number
    await runCLI("project-create", {
      project_id: PROJECT_2,
      display_name: "Synthetic Reoccupy Project Two",
      product_id: PRODUCT_ID,
      role: "secondary",
      expected_product_version: productVersion,
    })
    await runCLI("project-locator-add", { project_id: PROJECT_1, locator_id: "repo-1", kind: "canonical_path", value: repo1.repo, expected_version: 1 })
    await runCLI("project-locator-add", { project_id: PROJECT_2, locator_id: "repo-2", kind: "canonical_path", value: repo2.repo, expected_version: 1 })
    const designated = dbValue(dbPath, "SELECT version FROM products WHERE id=?", PRODUCT_ID).version as number
    await runCLI("product-knowledge-home-designate", { product_id: PRODUCT_ID, project_id: PROJECT_1, locator_id: "repo-1", expected_version: designated })
    await runCLI("client-register", {
      client_ref: "opencode",
      key_id: "reoccupy-key",
      principal_ref: "operator-1",
      public_key: publicKeyBase64(),
      capabilities: ["product_read", "work_define", "work_transition"],
      product_scope: [PRODUCT_ID],
      project_scope: [PROJECT_1, PROJECT_2],
      agent_scope: [AGENT],
    })

    // The fake host holds one live session and its live directory.
    // moveSession performs the relocation, the session route reads the
    // directory and the participation metadata back, and every move is
    // recorded with the directory the session left. failNextMove models a
    // host that performs the relocation and then fails to answer, so the
    // retry of the same vacate arrives after the landing already holds.
    let sessionDirectory = repo1.repo
    let sessionMetadata: Record<string, unknown> = {}
    let failNextMove = false
    const moves: Array<{ from: string; to: string }> = []
    hostControlPlane().bind({
      get: async ({ url, path }) => {
        expect(url).toBe(SESSION_ROUTE)
        expect(path).toEqual({ id: SESSION_ID })
        return { data: { id: SESSION_ID, directory: sessionDirectory, metadata: sessionMetadata }, response: new Response(null, { status: 200 }) }
      },
      patch: async ({ url, path, body }) => {
        expect(url).toBe(SESSION_ROUTE)
        expect(path).toEqual({ id: SESSION_ID })
        const patch = body as { metadata?: Record<string, unknown>; title?: string }
        if (patch.metadata !== undefined) {
          expect(patch).toEqual({ metadata: { [MANAGED_TASK_SCOPE_KEY]: "managed" } })
          sessionMetadata = patch.metadata
        }
        return { response: new Response(null, { status: 200 }) }
      },
      post: async ({ url, body }) => {
        expect(url).toBe(MOVE_SESSION_ROUTE)
        const destination = (body as { destination: { directory: string } }).destination.directory
        if (sessionDirectory !== destination) moves.push({ from: sessionDirectory, to: destination })
        sessionDirectory = destination
        if (failNextMove) {
          failNextMove = false
          return { data: "synthetic host failure after the relocation", response: new Response(null, { status: 500 }) }
        }
        return { data: null, response: new Response(null, { status: 204 }) }
      },
    })
    const runner = {
      async run(argv: string[], input: string, signal: AbortSignal, options?: { cwd?: string }) {
        return runFixtureProcess([binary, ...argv.slice(1)], input, { cwd: options?.cwd ?? repoCheckout, env: { ...process.env, ...childEnv }, signal })
      },
    }
    configureConcordAdapter({ runner })
    configureHostLease({ reset: true })

    // parseToolResult splits the JSON envelope from any warning lines the
    // tool wrapper appends after it.
    const parseToolResult = (result: any) => {
      try {
        return JSON.parse(String(result.output).split("\n")[0])
      } catch {
        throw new Error(`unparsable tool output: ${String(result.output).slice(0, 2000)}`)
      }
    }
    const invoke = (toolName: string, operation: string, input: Record<string, unknown>, directory: string) =>
      invokeConcordOperation(toolName, { operation, input } as any, contextFor(directory))
    const transition = async (operation: string, input: Record<string, unknown>, directory: string) =>
      parseToolResult(await work_transition.execute({ request: { operation, input } } as any, contextFor(directory)))
    const vacateEvents = () => dbRows(dbPath, "SELECT event_id, payload FROM domain_events WHERE kind='work.session_vacated' AND subject_id=? ORDER BY seq", workID)
    // CD-0178 D3: occupancy lives in worktree_occupancy, one row per
    // session. This flow holds at most one occupant at a time, so the
    // projection reads back as the single recorded session ref or empty.
    const worktreeEntries = () => dbRows(dbPath, "SELECT c.project_id AS project_id, e.path AS path, e.state AS state, COALESCE((SELECT group_concat(o.session_ref, ',') FROM worktree_occupancy o WHERE o.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id), '') AS occupant FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE c.work_id=? ORDER BY c.project_id", workID)

    // One work item holds membership in both Projects from capture, and no
    // worktree. The first work_start resume durably creates the primary
    // Project's worktree, records the session as its occupant, and the
    // mover lands the session in it.
    const captured = await invoke("concord_work_define", "capture", {
      title: "Synthetic vacate reoccupy",
      value_statement: "One session vacates, resumes, and vacates one work item.",
      kind: "bug",
      project_ids: [PROJECT_1, PROJECT_2],
      idempotency_key: "reoccupy-capture",
    }, repo1.repo)
    expect(captured.outcome, JSON.stringify(captured)).toBe("ok")
    const workID = (captured.changed_refs as Array<{ entity_kind: string; id: string }>)[0].id

    const resume = async (directory: string) => parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(directory)))

    // Entry into work: the resume read durably creates the worktree and the
    // mover lands the session in it. The old tool context cannot attest that
    // landing; only a next-turn replay from the worktree reports success.
    const unlandedEntry = await resume(repo1.repo)
    expect(unlandedEntry.outcome, JSON.stringify(unlandedEntry)).toBe("error")
    expect(unlandedEntry.error.kind).toBe("session_directory_mismatch")
    const worktree1 = unlandedEntry.worktree_path as string
    expect(moves).toEqual([{ from: repo1.repo, to: worktree1 }])
    const entered = await resume(worktree1)
    expect(entered.outcome, JSON.stringify(entered)).toBe("ok")
    expect(entered.worktree_path).toBe(worktree1)
    expect(moves).toHaveLength(1)
    expect(worktreeEntries()).toEqual([{ project_id: PROJECT_1, path: worktree1, state: "active", occupant: SESSION_ID }])

    // First vacate: the core records the relocation request toward the
    // derived main checkout and leaves the recorded occupancy standing. The
    // host performs the relocation and then fails to answer, so the adapter
    // reports the typed retryable transport failure without recording the
    // verified landing: the session's row stands, and the removal gates
    // never see the live session's worktree as empty.
    failNextMove = true
    const first = await transition("session_vacate", { idempotency_key: "reoccupy-vacate-1" }, worktree1)
    expect(first.outcome, JSON.stringify(first)).toBe("error")
    expect(first.replayed).toBe(false)
    expect((first.error as any).kind).toBe("transport_failure")
    expect((first.error as any).recovery_action).toEqual({ kind: "retry_same_request" })
    expect(vacateEvents()).toHaveLength(1)
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
      { from: worktree1, to: repo1.repo },
    ])
    expect(worktreeEntries()[0].occupant).toBe(SESSION_ID)

    // A retry of one unchanged vacate after the landed-but-unconfirmed move
    // recovers: the replay resolves to the recorded relocation request and
    // moves nothing, and the verified landing the readback earns records
    // through the adapter-only vacate-landing verb, releasing the session's
    // row so no stale row strands the session from claiming other work.
    const replay = await transition("session_vacate", { idempotency_key: "reoccupy-vacate-1" }, worktree1)
    expect(replay.outcome, JSON.stringify(replay)).toBe("ok")
    expect(replay.replayed).toBe(true)
    expect(moves).toHaveLength(2)
    expect(vacateEvents()).toHaveLength(1)
    expect(worktreeEntries()[0].occupant).toBe("")
    const recoveries = dbRows(dbPath, "SELECT payload FROM domain_events WHERE kind='work.session_vacate_landed' AND subject_id=? ORDER BY seq", workID)
    expect(recoveries).toHaveLength(1)
    expect(JSON.parse(recoveries[0].payload as string)).toMatchObject({ work_id: workID, session_ref: SESSION_ID, landed_directory: repo1.repo })

    // Work resume's read remains read-only. A move back from main refuses
    // while the tool context still reports main; next-turn replay from the
    // worktree succeeds without another move or vacate event, and the
    // verified landing records the resumed session as the occupant, so the
    // removal gates hold the worktree for it.
    const unlandedResume = await resume(repo1.repo)
    expect(unlandedResume.outcome, JSON.stringify(unlandedResume)).toBe("error")
    expect(unlandedResume.error.kind).toBe("session_directory_mismatch")
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
      { from: worktree1, to: repo1.repo },
      { from: repo1.repo, to: worktree1 },
    ])
    const resumed = await resume(worktree1)
    expect(resumed.outcome, JSON.stringify(resumed)).toBe("ok")
    expect(resumed.worktree_path).toBe(worktree1)
    expect(moves).toHaveLength(3)
    expect(vacateEvents()).toHaveLength(1)
    expect(worktreeEntries()[0].occupant).toBe(SESSION_ID)

    // Second vacate, new request identity: the same session leaves the same
    // worktree again, the core records a second, distinct operation, and the
    // verified landing the move readback earns releases the session's rows.
    const second = await transition("session_vacate", { idempotency_key: "reoccupy-vacate-2" }, worktree1)
    expect(second.outcome, JSON.stringify(second)).toBe("ok")
    expect(second.replayed).toBe(false)
    const events = vacateEvents()
    expect(events).toHaveLength(2)
    expect(events[0].event_id).not.toBe(events[1].event_id)
    for (const event of events) {
      expect(JSON.parse(event.payload as string)).toMatchObject({
        work_id: workID,
        project_id: PROJECT_1,
        session_ref: SESSION_ID,
        source_directory: worktree1,
        destination_directory: repo1.repo,
      })
    }
    // Both landings recorded their own event and each released the
    // session's rows in one transaction: the recovery landing, then the
    // second vacate's landing.
    const landings = dbRows(dbPath, "SELECT payload FROM domain_events WHERE kind='work.session_vacate_landed' AND subject_id=? ORDER BY seq", workID)
    expect(landings).toHaveLength(2)
    for (const landing of landings) {
      expect(JSON.parse(landing.payload as string)).toMatchObject({ work_id: workID, session_ref: SESSION_ID, landed_directory: repo1.repo })
    }

    // Both vacates preserve the original Project claim, active and
    // unoccupied. The vacated session then claims the same work item in the
    // second Project through the durable resume route; the host move that
    // would follow is outside this cycle's vacate proof.
    const backend = await runCLI("work-resume", { product_id: PRODUCT_ID, project_id: PROJECT_2, work_id: workID, session_ref: SESSION_ID }, repo2.repo)
    const worktree2 = backend.worktree.path as string
    expect(moves).toHaveLength(4)
    const entries = worktreeEntries()
    expect(entries).toHaveLength(2)
    expect(entries[0]).toMatchObject({ project_id: PROJECT_1, path: worktree1, state: "active", occupant: "" })
    expect(entries[1]).toMatchObject({ project_id: PROJECT_2, path: worktree2, state: "active", occupant: SESSION_ID })
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    clearClaimedWorktree(SESSION_ID)
    resetClaimedWorktrees()
    resetTurnMoveBoundaries()
    if (previousSelectedProduct === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
    else process.env.CONCORD_SELECTED_PRODUCT_ID = previousSelectedProduct
    if (previousZellijPane === undefined) delete process.env.ZELLIJ_PANE_ID
    else process.env.ZELLIJ_PANE_ID = previousZellijPane
    await scope.close()
  }
  })
  scope.ensureReleased()
}, 300_000)

// Connected regression for the committed-refusal recovery (CD-0190 D3). A
// vacate whose host readback names a directory outside every registered
// Project leaves the committed request and the occupancy rows standing: a
// plain retry resolves its Project from that directory and refuses before the
// pending-request replay can run. The adapter keeps the committed destination
// for the session, so the retry first moves the host session to the
// registered main checkout and resolves the core call from it, and the
// readback-verified landing releases the rows.
connected("a readback outside every Project recovers through the remembered destination", async () => {
  const scope = await beginOwnedFixture("concord-vacate-outside-")
  await scope.run(async (root) => {
  const dbPath = join(root, "concord.db")
  const binRoot = join(root, "bin")
  const homeRoot = join(root, "home")
  const repoCheckout = join(root, "checkout")
  const outside = join(root, "outside")
  let binary = process.env.CONCORD_BIN ?? ""
  const previousSelectedProduct = process.env.CONCORD_SELECTED_PRODUCT_ID
  delete process.env.CONCORD_SELECTED_PRODUCT_ID
  const previousZellijPane = process.env.ZELLIJ_PANE_ID
  delete process.env.ZELLIJ_PANE_ID
  try {
    if (!binary) {
      await mkdir(binRoot, { recursive: true })
      binary = join(binRoot, "concord-core")
      const build = await runProcess(["go", "build", "-o", binary, "./cmd/concord"], "", join(import.meta.dir, "..", ".."))
      expect(build.exitCode, `go build: ${build.stderr}`).toBe(0)
    }
    const repo1 = await repositoryFixture(root, "repo-1")
    await mkdir(repoCheckout, { recursive: true })
    await mkdir(join(homeRoot, ".config", "opencode", "agents"), { recursive: true })
    for (const lane of LANE_AGENTS) {
      await writeFile(join(homeRoot, ".config", "opencode", "agents", `${lane}.md`), `${lane} synthetic definition\n`)
    }
    const probe = join(binRoot, "opencode")
    await Bun.write(probe, `#!/bin/sh\necho '{"agent":{"${AGENT}":{"mode":"all","disable":false}}}'\n`)
    await chmod(probe, 0o755)
    const childEnv = { HOME: homeRoot, PATH: `${binRoot}:${process.env.PATH ?? ""}`, CONCORD_DB_PATH: dbPath }
    const runCLI = (command: string, value: Record<string, unknown>, cwd?: string) => runProcess([binary, command], JSON.stringify(value), cwd, childEnv).then((result) => {
      expect(result.exitCode, `${command}: ${result.stderr}`).toBe(0)
      return JSON.parse(result.stdout.trim().split("\n").filter(Boolean).pop() as string)
    })
    await runCLI("product-create", {
      product_id: PRODUCT_ID,
      display_name: "Synthetic Outside Product",
      stage_maturity: "prototype",
      stage_audience_commitment: "operator_only",
      project_id: PROJECT_1,
      project_display_name: "Synthetic Outside Project",
      role: "primary",
    })
    await runCLI("project-locator-add", { project_id: PROJECT_1, locator_id: "repo-1", kind: "canonical_path", value: repo1.repo, expected_version: 1 })
    await runCLI("client-register", {
      client_ref: "opencode",
      key_id: "outside-key",
      principal_ref: "operator-1",
      public_key: publicKeyBase64(),
      capabilities: ["product_read", "work_define", "work_transition"],
      product_scope: [PRODUCT_ID],
      project_scope: [PROJECT_1],
      agent_scope: [AGENT],
    })

    // The fake host holds one live session. moveRegresses models the host
    // that performs the relocation and then leaves the session outside every
    // registered Project, so the readback names that directory.
    let sessionDirectory = repo1.repo
    let sessionMetadata: Record<string, unknown> = {}
    let moveRegresses = false
    const moves: Array<{ from: string; to: string }> = []
    hostControlPlane().bind({
      get: async () => ({ data: { id: SESSION_ID, directory: sessionDirectory, metadata: sessionMetadata }, response: new Response(null, { status: 200 }) }),
      patch: async ({ body }) => {
        const patch = body as { metadata?: Record<string, unknown> }
        if (patch.metadata !== undefined) sessionMetadata = patch.metadata
        return { response: new Response(null, { status: 200 }) }
      },
      post: async ({ body }) => {
        const destination = (body as { destination: { directory: string } }).destination.directory
        if (sessionDirectory !== destination) moves.push({ from: sessionDirectory, to: destination })
        sessionDirectory = moveRegresses ? outside : destination
        return { data: null, response: new Response(null, { status: 204 }) }
      },
    })
    const runner = {
      async run(argv: string[], input: string, signal: AbortSignal, options?: { cwd?: string }) {
        return runFixtureProcess([binary, ...argv.slice(1)], input, { cwd: options?.cwd ?? repoCheckout, env: { ...process.env, ...childEnv }, signal })
      },
    }
    configureConcordAdapter({ runner })
    configureHostLease({ reset: true })

    const parseToolResult = (result: any) => {
      try {
        return JSON.parse(String(result.output).split("\n")[0])
      } catch {
        throw new Error(`unparsable tool output: ${String(result.output).slice(0, 2000)}`)
      }
    }
    const invoke = (toolName: string, operation: string, input: Record<string, unknown>, directory: string) =>
      invokeConcordOperation(toolName, { operation, input } as any, contextFor(directory))
    const transition = async (operation: string, input: Record<string, unknown>, directory: string) =>
      parseToolResult(await work_transition.execute({ request: { operation, input } } as any, contextFor(directory)))
    const vacateEvents = () => dbRows(dbPath, "SELECT event_id, payload FROM domain_events WHERE kind='work.session_vacated' AND subject_id=? ORDER BY seq", workID)
    const worktreeEntries = () => dbRows(dbPath, "SELECT c.project_id AS project_id, e.path AS path, e.state AS state, COALESCE((SELECT group_concat(o.session_ref, ',') FROM worktree_occupancy o WHERE o.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id), '') AS occupant FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE c.work_id=? ORDER BY c.project_id", workID)

    const captured = await invoke("concord_work_define", "capture", {
      title: "Synthetic outside readback",
      value_statement: "One vacate readback lands outside every Project and recovers.",
      kind: "bug",
      project_ids: [PROJECT_1],
      idempotency_key: "outside-capture",
    }, repo1.repo)
    expect(captured.outcome, JSON.stringify(captured)).toBe("ok")
    const workID = (captured.changed_refs as Array<{ entity_kind: string; id: string }>)[0].id

    // Entry into work: the resume read durably creates the worktree and the
    // mover lands the session in it on the next-turn replay.
    const unlandedEntry = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(repo1.repo)))
    expect(unlandedEntry.outcome, JSON.stringify(unlandedEntry)).toBe("error")
    expect(unlandedEntry.error.kind, JSON.stringify(unlandedEntry)).toBe("session_directory_mismatch")
    const worktree1 = unlandedEntry.worktree_path as string
    const entered = parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(worktree1)))
    expect(entered.outcome, JSON.stringify(entered)).toBe("ok")
    expect(worktreeEntries()).toEqual([{ project_id: PROJECT_1, path: worktree1, state: "active", occupant: SESSION_ID }])

    // The vacate commits its relocation request, the host performs the move,
    // and the readback names the directory outside every Project: the typed
    // refusal reports the possible effect, and the adapter remembers the
    // committed destination for the retry.
    moveRegresses = true
    const first = await transition("session_vacate", { idempotency_key: "outside-vacate-1" }, worktree1)
    expect(first.outcome, JSON.stringify(first)).toBe("error")
    expect((first.error as any).adapter_reason).toBe("vacate_destination_mismatch")
    expect((first.error as any).effect_state).toBe("possible")
    expect((first.error as any).recovery_action).toEqual({ kind: "retry_same_request" })
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
      { from: worktree1, to: repo1.repo },
    ])
    expect(vacateEvents()).toHaveLength(1)
    expect(worktreeEntries()[0].occupant).toBe(SESSION_ID)

    // The retry from the stale tool context first moves the host session to
    // the remembered destination and resolves the core call from it: the
    // pending-request replay appends nothing, and the readback-verified
    // landing records through the vacate-landing verb and releases the rows.
    moveRegresses = false
    const replay = await transition("session_vacate", { idempotency_key: "outside-vacate-2" }, worktree1)
    expect(replay.outcome, JSON.stringify(replay)).toBe("ok")
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
      { from: worktree1, to: repo1.repo },
      { from: outside, to: repo1.repo },
    ])
    expect(vacateEvents()).toHaveLength(1)
    const recoveries = dbRows(dbPath, "SELECT payload FROM domain_events WHERE kind='work.session_vacate_landed' AND subject_id=? ORDER BY seq", workID)
    expect(recoveries).toHaveLength(1)
    expect(JSON.parse(recoveries[0].payload as string)).toMatchObject({ work_id: workID, session_ref: SESSION_ID, landed_directory: repo1.repo })
    expect(worktreeEntries()[0].occupant).toBe("")

    // The completed request replays as an idempotent completed replay: a
    // further session_vacate from the verified destination appends nothing
    // and reports ok, so the uncertain landing result has a working recovery.
    const confirm = await transition("session_vacate", { idempotency_key: "outside-vacate-3" }, repo1.repo)
    expect(confirm.outcome, JSON.stringify(confirm)).toBe("ok")
    expect(vacateEvents()).toHaveLength(1)
    expect(dbRows(dbPath, "SELECT payload FROM domain_events WHERE kind='work.session_vacate_landed' AND subject_id=?", workID)).toHaveLength(1)
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    clearClaimedWorktree(SESSION_ID)
    resetClaimedWorktrees()
    resetTurnMoveBoundaries()
    if (previousSelectedProduct === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
    else process.env.CONCORD_SELECTED_PRODUCT_ID = previousSelectedProduct
    if (previousZellijPane === undefined) delete process.env.ZELLIJ_PANE_ID
    else process.env.ZELLIJ_PANE_ID = previousZellijPane
    await scope.close()
  }
  })
  scope.ensureReleased()
}, 300_000)

// Connected regression for the unreadable post-commit answer (CD-0190 D3).
// The core commits the relocation request before it answers, so a successful
// response the adapter cannot read classifies with the state-driven replay as
// the recovery — session_vacate has no work id, so no generic reconciliation
// can drive — and the adapter remembers nothing the core did not return. The
// same-key retry then resolves the pending request from the source worktree
// the session still runs in, and the verified landing records and releases.
connected("an unreadable ok answer recovers through the same-key replay from the source", async () => {
  const scope = await beginOwnedFixture("concord-vacate-unreadable-")
  await scope.run(async (root) => {
  const dbPath = join(root, "concord.db")
  const binRoot = join(root, "bin")
  const homeRoot = join(root, "home")
  const repoCheckout = join(root, "checkout")
  let binary = process.env.CONCORD_BIN ?? ""
  const previousSelectedProduct = process.env.CONCORD_SELECTED_PRODUCT_ID
  delete process.env.CONCORD_SELECTED_PRODUCT_ID
  const previousZellijPane = process.env.ZELLIJ_PANE_ID
  delete process.env.ZELLIJ_PANE_ID
  try {
    if (!binary) {
      await mkdir(binRoot, { recursive: true })
      binary = join(binRoot, "concord-core")
      const build = await runProcess(["go", "build", "-o", binary, "./cmd/concord"], "", join(import.meta.dir, "..", ".."))
      expect(build.exitCode, `go build: ${build.stderr}`).toBe(0)
    }
    const repo1 = await repositoryFixture(root, "repo-1")
    await mkdir(repoCheckout, { recursive: true })
    await mkdir(join(homeRoot, ".config", "opencode", "agents"), { recursive: true })
    for (const lane of LANE_AGENTS) {
      await writeFile(join(homeRoot, ".config", "opencode", "agents", `${lane}.md`), `${lane} synthetic definition\n`)
    }
    const probe = join(binRoot, "opencode")
    await Bun.write(probe, `#!/bin/sh\necho '{"agent":{"${AGENT}":{"mode":"all","disable":false}}}'\n`)
    await chmod(probe, 0o755)
    const childEnv = { HOME: homeRoot, PATH: `${binRoot}:${process.env.PATH ?? ""}`, CONCORD_DB_PATH: dbPath }
    const runCLI = (command: string, value: Record<string, unknown>, cwd?: string) => runProcess([binary, command], JSON.stringify(value), cwd, childEnv).then((result) => {
      expect(result.exitCode, `${command}: ${result.stderr}`).toBe(0)
      return JSON.parse(result.stdout.trim().split("\n").filter(Boolean).pop() as string)
    })
    await runCLI("product-create", {
      product_id: PRODUCT_ID,
      display_name: "Synthetic Unreadable Product",
      stage_maturity: "prototype",
      stage_audience_commitment: "operator_only",
      project_id: PROJECT_1,
      project_display_name: "Synthetic Unreadable Project",
      role: "primary",
    })
    await runCLI("project-locator-add", { project_id: PROJECT_1, locator_id: "repo-1", kind: "canonical_path", value: repo1.repo, expected_version: 1 })
    await runCLI("client-register", {
      client_ref: "opencode",
      key_id: "unreadable-key",
      principal_ref: "operator-1",
      public_key: publicKeyBase64(),
      capabilities: ["product_read", "work_define", "work_transition"],
      product_scope: [PRODUCT_ID],
      project_scope: [PROJECT_1],
      agent_scope: [AGENT],
    })

    const sessionDirectory = { value: repo1.repo }
    let sessionMetadata: Record<string, unknown> = {}
    const moves: Array<{ from: string; to: string }> = []
    hostControlPlane().bind({
      get: async () => ({ data: { id: SESSION_ID, directory: sessionDirectory.value, metadata: sessionMetadata }, response: new Response(null, { status: 200 }) }),
      patch: async ({ body }) => {
        const patch = body as { metadata?: Record<string, unknown> }
        if (patch.metadata !== undefined) sessionMetadata = patch.metadata
        return { response: new Response(null, { status: 200 }) }
      },
      post: async ({ body }) => {
        const destination = (body as { destination: { directory: string } }).destination.directory
        if (sessionDirectory.value !== destination) moves.push({ from: sessionDirectory.value, to: destination })
        sessionDirectory.value = destination
        return { data: null, response: new Response(null, { status: 204 }) }
      },
    })
    // The first session_vacate answer is lost in transit: the real core runs,
    // commits, and answers ok, and the runner returns an unparsable success.
    let dropNextVacateAnswer = false
    const spawnCore = async (argv: string[], input: string, signal: AbortSignal, options?: { cwd?: string }) => {
      return runFixtureProcess([binary, ...argv.slice(1)], input, { cwd: options?.cwd ?? repoCheckout, env: { ...process.env, ...childEnv }, signal })
    }
    const runner = {
      async run(argv: string[], input: string, signal: AbortSignal, options?: { cwd?: string }) {
        if (dropNextVacateAnswer && argv[1] === "invoke") {
          const parsed = JSON.parse(input) as { operation?: string }
          if (parsed?.operation === "session_vacate") {
            dropNextVacateAnswer = false
            const real = await spawnCore(argv, input, signal, options)
            if (real.exitCode === 0) return { exitCode: 0, stdout: "concord core answer lost in transit\n", stderr: "" }
            return real
          }
        }
        return spawnCore(argv, input, signal, options)
      },
    }
    configureConcordAdapter({ runner })
    configureHostLease({ reset: true })

    const parseToolResult = (result: any) => {
      try {
        return JSON.parse(String(result.output).split("\n")[0])
      } catch {
        throw new Error(`unparsable tool output: ${String(result.output).slice(0, 2000)}`)
      }
    }
    const invoke = (toolName: string, operation: string, input: Record<string, unknown>, directory: string) =>
      invokeConcordOperation(toolName, { operation, input } as any, contextFor(directory))
    const transition = async (operation: string, input: Record<string, unknown>, directory: string) =>
      parseToolResult(await work_transition.execute({ request: { operation, input } } as any, contextFor(directory)))
    const vacateEvents = () => dbRows(dbPath, "SELECT event_id, payload FROM domain_events WHERE kind='work.session_vacated' ORDER BY seq")
    const landings = () => dbRows(dbPath, "SELECT payload FROM domain_events WHERE kind='work.session_vacate_landed' ORDER BY seq")
    const occupants = () => dbRows(dbPath, "SELECT COALESCE((SELECT group_concat(o.session_ref, ',') FROM worktree_occupancy o WHERE o.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id), '') AS occupant FROM worktree_entries e WHERE e.state='active' ORDER BY e.path").map((row: any) => row.occupant as string)

    const captured = await invoke("concord_work_define", "capture", {
      title: "Synthetic unreadable answer",
      value_statement: "One vacate answer is lost in transit and the replay recovers.",
      kind: "bug",
      project_ids: [PROJECT_1],
      idempotency_key: "unreadable-capture",
    }, repo1.repo)
    expect(captured.outcome, JSON.stringify(captured)).toBe("ok")
    const workID = (captured.changed_refs as Array<{ entity_kind: string; id: string }>)[0].id
    const resume = async (directory: string) => parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(directory)))

    const unlandedEntry = await resume(repo1.repo)
    expect(unlandedEntry.outcome, JSON.stringify(unlandedEntry)).toBe("error")
    const worktree1 = unlandedEntry.worktree_path as string
    const entered = await resume(worktree1)
    expect(entered.outcome, JSON.stringify(entered)).toBe("ok")
    expect(occupants()).toEqual([SESSION_ID])

    // The vacate commits, and its successful answer is lost in transit: the
    // refusal classifies with the state-driven replay recovery, reports the
    // possible effect, and remembers nothing the core did not return.
    dropNextVacateAnswer = true
    const first = await transition("session_vacate", { idempotency_key: "unreadable-vacate-1" }, worktree1)
    expect(first.outcome, JSON.stringify(first)).toBe("error")
    expect((first.error as any).kind).toBe("malformed_response")
    expect((first.error as any).effect_state).toBe("possible")
    expect((first.error as any).recovery_action).toEqual({ kind: "retry_same_request" })
    expect((first.error as any).message).toContain("replay session_vacate")
    expect(pendingVacateDestination(SESSION_ID)).toBeNull()
    expect(vacateEvents()).toHaveLength(1)
    expect(landings()).toHaveLength(0)
    expect(occupants()).toEqual([SESSION_ID])
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
    ])

    // The same-key retry from the source worktree resolves the pending
    // request state-driven, appends nothing, and the move the answer drives
    // earns the verified landing that releases the row.
    const replay = await transition("session_vacate", { idempotency_key: "unreadable-vacate-1" }, worktree1)
    expect(replay.outcome, JSON.stringify(replay)).toBe("ok")
    expect(replay.replayed).toBe(true)
    expect(vacateEvents()).toHaveLength(1)
    expect(landings()).toHaveLength(1)
    expect(JSON.parse(landings()[0].payload as string)).toMatchObject({ work_id: workID, session_ref: SESSION_ID, landed_directory: repo1.repo })
    expect(occupants()).toEqual([""])
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
      { from: worktree1, to: repo1.repo },
    ])
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    clearClaimedWorktree(SESSION_ID)
    resetClaimedWorktrees()
    resetTurnMoveBoundaries()
    if (previousSelectedProduct === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
    else process.env.CONCORD_SELECTED_PRODUCT_ID = previousSelectedProduct
    if (previousZellijPane === undefined) delete process.env.ZELLIJ_PANE_ID
    else process.env.ZELLIJ_PANE_ID = previousZellijPane
    await scope.close()
  }
  })
  scope.ensureReleased()
}, 300_000)

// Connected regression for the state-driven vacate replay (CD-0190 D2/D3).
// A committed vacate whose landing recorded, followed by a later claim of
// other work, must refuse a same-key replay with effect none: the adapter
// never moves the host session out of claimed work for a request that cannot
// complete, the later claim's occupancy row stands, and the refusal's
// recovery — the later claim's own verified landing or vacate — works.
connected("a same-key replay after a later claim refuses without moving the host", async () => {
  const scope = await beginOwnedFixture("concord-vacate-later-claim-")
  await scope.run(async (root) => {
  const dbPath = join(root, "concord.db")
  const binRoot = join(root, "bin")
  const homeRoot = join(root, "home")
  const repoCheckout = join(root, "checkout")
  let binary = process.env.CONCORD_BIN ?? ""
  const previousSelectedProduct = process.env.CONCORD_SELECTED_PRODUCT_ID
  delete process.env.CONCORD_SELECTED_PRODUCT_ID
  const previousZellijPane = process.env.ZELLIJ_PANE_ID
  delete process.env.ZELLIJ_PANE_ID
  try {
    if (!binary) {
      await mkdir(binRoot, { recursive: true })
      binary = join(binRoot, "concord-core")
      const build = await runProcess(["go", "build", "-o", binary, "./cmd/concord"], "", join(import.meta.dir, "..", ".."))
      expect(build.exitCode, `go build: ${build.stderr}`).toBe(0)
    }
    const repo1 = await repositoryFixture(root, "repo-1")
    await mkdir(repoCheckout, { recursive: true })
    await mkdir(join(homeRoot, ".config", "opencode", "agents"), { recursive: true })
    for (const lane of LANE_AGENTS) {
      await writeFile(join(homeRoot, ".config", "opencode", "agents", `${lane}.md`), `${lane} synthetic definition\n`)
    }
    const probe = join(binRoot, "opencode")
    await Bun.write(probe, `#!/bin/sh\necho '{"agent":{"${AGENT}":{"mode":"all","disable":false}}}'\n`)
    await chmod(probe, 0o755)
    const childEnv = { HOME: homeRoot, PATH: `${binRoot}:${process.env.PATH ?? ""}`, CONCORD_DB_PATH: dbPath }
    const runCLI = (command: string, value: Record<string, unknown>, cwd?: string) => runProcess([binary, command], JSON.stringify(value), cwd, childEnv).then((result) => {
      expect(result.exitCode, `${command}: ${result.stderr}`).toBe(0)
      return JSON.parse(result.stdout.trim().split("\n").filter(Boolean).pop() as string)
    })
    await runCLI("product-create", {
      product_id: PRODUCT_ID,
      display_name: "Synthetic Later Claim Product",
      stage_maturity: "prototype",
      stage_audience_commitment: "operator_only",
      project_id: PROJECT_1,
      project_display_name: "Synthetic Later Claim Project",
      role: "primary",
    })
    await runCLI("project-locator-add", { project_id: PROJECT_1, locator_id: "repo-1", kind: "canonical_path", value: repo1.repo, expected_version: 1 })
    await runCLI("client-register", {
      client_ref: "opencode",
      key_id: "later-claim-key",
      principal_ref: "operator-1",
      public_key: publicKeyBase64(),
      capabilities: ["product_read", "work_define", "work_transition"],
      product_scope: [PRODUCT_ID],
      project_scope: [PROJECT_1],
      agent_scope: [AGENT],
    })

    const sessionDirectory = { value: repo1.repo }
    let sessionMetadata: Record<string, unknown> = {}
    const moves: Array<{ from: string; to: string }> = []
    hostControlPlane().bind({
      get: async () => ({ data: { id: SESSION_ID, directory: sessionDirectory.value, metadata: sessionMetadata }, response: new Response(null, { status: 200 }) }),
      patch: async ({ body }) => {
        const patch = body as { metadata?: Record<string, unknown> }
        if (patch.metadata !== undefined) sessionMetadata = patch.metadata
        return { response: new Response(null, { status: 200 }) }
      },
      post: async ({ body }) => {
        const destination = (body as { destination: { directory: string } }).destination.directory
        if (sessionDirectory.value !== destination) moves.push({ from: sessionDirectory.value, to: destination })
        sessionDirectory.value = destination
        return { data: null, response: new Response(null, { status: 204 }) }
      },
    })
    // The first vacate's landing commits in the real core and then fails to
    // answer, so the refusal keeps the remembered destination and the armed
    // record while the rows are already released: the exact state a later
    // claim grows out of.
    let failNextLandingAnswer = false
    const spawnCore = async (argv: string[], input: string, signal: AbortSignal, options?: { cwd?: string }) => {
      return runFixtureProcess([binary, ...argv.slice(1)], input, { cwd: options?.cwd ?? repoCheckout, env: { ...process.env, ...childEnv }, signal })
    }
    const runner = {
      async run(argv: string[], input: string, signal: AbortSignal, options?: { cwd?: string }) {
        const real = await spawnCore(argv, input, signal, options)
        if (failNextLandingAnswer && argv[1] === "vacate-landing" && real.exitCode === 0) {
          failNextLandingAnswer = false
          return { exitCode: 1, stdout: "", stderr: "concord vacate-landing: write failed after commit: broken pipe" }
        }
        return real
      },
    }
    configureConcordAdapter({ runner })
    configureHostLease({ reset: true })

    const parseToolResult = (result: any) => {
      try {
        return JSON.parse(String(result.output).split("\n")[0])
      } catch {
        throw new Error(`unparsable tool output: ${String(result.output).slice(0, 2000)}`)
      }
    }
    const invoke = (toolName: string, operation: string, input: Record<string, unknown>, directory: string) =>
      invokeConcordOperation(toolName, { operation, input } as any, contextFor(directory))
    const transition = async (operation: string, input: Record<string, unknown>, directory: string) =>
      parseToolResult(await work_transition.execute({ request: { operation, input } } as any, contextFor(directory)))
    const vacateEvents = () => dbRows(dbPath, "SELECT event_id, payload FROM domain_events WHERE kind='work.session_vacated' ORDER BY seq")
    const landings = () => dbRows(dbPath, "SELECT payload FROM domain_events WHERE kind='work.session_vacate_landed' ORDER BY seq")
    const occupants = () => dbRows(dbPath, "SELECT COALESCE((SELECT group_concat(o.session_ref, ',') FROM worktree_occupancy o WHERE o.worktree_id = e.set_id || ':' || e.project_id || ':' || e.claim_op_id), '') AS occupant FROM worktree_entries e WHERE e.state='active' ORDER BY e.path").map((row: any) => row.occupant as string)

    for (const [index, key] of ["later-claim-capture-1", "later-claim-capture-2"].entries()) {
      const captured = await invoke("concord_work_define", "capture", {
        title: `Synthetic later claim ${index + 1}`,
        value_statement: "One session claims other work after a vacate whose landing recorded.",
        kind: "bug",
        project_ids: [PROJECT_1],
        idempotency_key: key,
      }, repo1.repo)
      expect(captured.outcome, JSON.stringify(captured)).toBe("ok")
    }
    const works = dbRows(dbPath, "SELECT id FROM work_items ORDER BY id")
    const work1 = works[0].id as string
    const work2 = works[1].id as string
    const resume = (workID: string) => async (directory: string) => parseToolResult(await work_start.execute({ work_id: workID } as any, contextFor(directory)))
    const resumeWork1 = resume(work1)
    const resumeWork2 = resume(work2)

    const unlandedEntry = await resumeWork1(repo1.repo)
    expect(unlandedEntry.outcome, JSON.stringify(unlandedEntry)).toBe("error")
    const worktree1 = unlandedEntry.worktree_path as string
    const entered = await resumeWork1(worktree1)
    expect(entered.outcome, JSON.stringify(entered)).toBe("ok")
    expect(occupants()).toEqual([SESSION_ID])

    // The vacate commits, the host move lands, and the verified landing
    // records and releases — but its answer is lost, so the refusal reports
    // the possible effect and the adapter keeps the remembered destination.
    failNextLandingAnswer = true
    const first = await transition("session_vacate", { idempotency_key: "later-claim-vacate-1" }, worktree1)
    expect(first.outcome, JSON.stringify(first)).toBe("error")
    expect((first.error as any).adapter_reason).toBe("vacate_landing_refused")
    expect((first.error as any).effect_state).toBe("possible")
    expect((first.error as any).recovery_action).toEqual({ kind: "retry_same_request" })
    expect(vacateEvents()).toHaveLength(1)
    expect(landings()).toHaveLength(1)
    expect(occupants()).toEqual([""])
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
      { from: worktree1, to: repo1.repo },
    ])

    // The session claims other work in the same Project and lands there.
    const unlandedSecond = await resumeWork2(repo1.repo)
    expect(unlandedSecond.outcome, JSON.stringify(unlandedSecond)).toBe("error")
    const worktree2 = unlandedSecond.worktree_path as string
    const enteredSecond = await resumeWork2(worktree2)
    expect(enteredSecond.outcome, JSON.stringify(enteredSecond)).toBe("ok")
    expect(occupants().sort()).toEqual(["", SESSION_ID])

    // The same-key replay of the first vacate re-reads the committed state:
    // the request's landing already completed and the session holds the later
    // claim's row, so the replay refuses with effect none, and the adapter
    // never moves the host session out of claimed work to hear the refusal.
    const replay = await transition("session_vacate", { idempotency_key: "later-claim-vacate-1" }, worktree2)
    expect(replay.outcome, JSON.stringify(replay)).toBe("error")
    expect((replay.error as any).kind).toBe("invalid_input")
    expect((replay.error as any).effect_state).toBe("none")
    expect((replay.error as any).message).toContain("belong to a later claim")
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
      { from: worktree1, to: repo1.repo },
      { from: repo1.repo, to: worktree2 },
    ])
    expect(vacateEvents()).toHaveLength(1)
    expect(landings()).toHaveLength(1)
    expect(occupants().sort()).toEqual(["", SESSION_ID])

    // The refusal's recovery works: the later claim's own vacate moves the
    // session to the registered main checkout, records its verified landing,
    // and releases the later claim's row.
    const leave = await transition("session_vacate", { idempotency_key: "later-claim-vacate-2" }, worktree2)
    expect(leave.outcome, JSON.stringify(leave)).toBe("ok")
    expect(vacateEvents()).toHaveLength(2)
    expect(landings()).toHaveLength(2)
    expect(occupants().sort()).toEqual(["", ""])
    expect(moves).toEqual([
      { from: repo1.repo, to: worktree1 },
      { from: worktree1, to: repo1.repo },
      { from: repo1.repo, to: worktree2 },
      { from: worktree2, to: repo1.repo },
    ])
  } finally {
    configureConcordAdapter({ reset: true })
    hostControlPlane().bind(undefined)
    clearClaimedWorktree(SESSION_ID)
    resetClaimedWorktrees()
    resetTurnMoveBoundaries()
    if (previousSelectedProduct === undefined) delete process.env.CONCORD_SELECTED_PRODUCT_ID
    else process.env.CONCORD_SELECTED_PRODUCT_ID = previousSelectedProduct
    if (previousZellijPane === undefined) delete process.env.ZELLIJ_PANE_ID
    else process.env.ZELLIJ_PANE_ID = previousZellijPane
    await scope.close()
  }
  })
  scope.ensureReleased()
}, 300_000)
