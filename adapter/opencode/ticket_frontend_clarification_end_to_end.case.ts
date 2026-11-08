import { expect, test } from "bun:test"
import { mkdir } from "node:fs/promises"
import { join } from "node:path"
import { fixtureTempRoot, requireOwnedFixtureRun, runFixtureProcess } from "./fixture-temp-root"
import { dbValue, fixturePublicKeyBase64, git, runJSON, type JSONRecord } from "./end-to-end.fixture"
import ConcordAdapterPlugin from "./concord-plugin"
import { configureConcordAdapter, invokeConcordOperation } from "./concord"
import { configureCoreBinary } from "./core-runtime"
import { configureHostLease } from "./host-lease"
import { manifestDigest, payloadSchemas } from "./generated-contracts"
import { sessionDirectoryReader, SESSION_ROUTE } from "./session-directory"

requireOwnedFixtureRun()

const routeTest = Bun.spawnSync(["go", "version"], { env: { ...process.env } }).exitCode === 0 ? test : test.skip
const name = "resumes clarification after a ticket exchange"
const productID = "product-ticket-e2e"
const projectID = "project-ticket-e2e"
const issueKey = "CON-7360"
const neighborKey = "CON-7361"
const author = "Shared identity"
const question = { issue_key: issueKey, author, body: "Which output should be retained?\nWhich format should it use?" }
const changes = {
  answer: { author, body: "Retain the summary. Use plain text." },
  discussion: { author, body: "The summary is for a later reader." },
  description: { author, body: "The output must fit on one page." },
}

// The Linear exchange is a stub: no client, credential or network module
// exists in this fixture, so no Linear call can occur (CD-0213 D7). The
// changed set below stands in for what the frontend hands the resuming
// session; every entry carries the same author, as agent and operator
// comments do while agents post under the operator's identity.
function ticketStub(path: string) {
  return {
    async publish() {
      await Bun.write(path, JSON.stringify({ question }))
    },
    async readChanges() {
      const { question: published } = await Bun.file(path).json()
      return { question: published, changes }
    },
  }
}

if (!process.env.TEST_CONCORD_TICKET_CASE || new RegExp(process.env.TEST_CONCORD_TICKET_CASE).test(name)) routeTest(name, async () => {
  const phase = process.env.TEST_CONCORD_TICKET_PHASE
  if (!phase) {
    const root = await fixtureTempRoot("ticket-clarify")
    const binary = join(root, "concord")
    const dbPath = join(root, "concord.db")
    const repo = join(root, "repo")
    await mkdir(repo)
    await git(repo, "init", "--quiet", "--initial-branch=main")
    await git(repo, "config", "user.email", "test@example.invalid")
    await git(repo, "config", "user.name", "Synthetic Test")
    await Bun.write(join(repo, "README.md"), "synthetic ticket fixture\n")
    await mkdir(join(repo, ".concord/docs/knowledge"), { recursive: true })
    await Bun.write(join(repo, ".concord/docs/knowledge", "domain-registry.json"), JSON.stringify({
      schema_version: "1.0",
      product_key: productID,
      root_domain_id: `product-root:${productID}`,
      domains: [{ domain_id: `product-root:${productID}`, name: "Synthetic root", purpose: "Synthetic test domain", status: "current", architecture_relations: [] }],
    }, null, 2))
    await git(repo, "add", ".")
    await git(repo, "commit", "--quiet", "-m", "fixture")
    await git(repo, "remote", "add", "origin", "https://example.invalid/synthetic.git")
    const origin = join(root, "origin.git")
    await git(root, "init", "--quiet", "--bare", "--initial-branch=main", origin)
    await git(repo, "config", `url.${origin}.insteadOf`, "https://example.invalid/synthetic.git")
    await git(repo, "push", "--quiet", "origin", "main")
    await git(repo, "fetch", "--quiet", "origin")
    await git(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
    const build = await runFixtureProcess(["go", "build", "-o", binary, "./cmd/concord"], "", { cwd: join(import.meta.dir, "..", "..") })
    expect(build.exitCode, build.stderr).toBe(0)
    await runJSON(binary, dbPath, "product-create", { product_id: productID, display_name: "Ticket E2E", stage_maturity: "prototype", stage_audience_commitment: "operator_only", project_id: projectID, project_display_name: "Ticket Project", role: "primary" })
    await runJSON(binary, dbPath, "project-locator-add", { project_id: projectID, locator_id: "repo-ticket", kind: "canonical_path", value: repo, expected_version: 1 })
    await runJSON(binary, dbPath, "project-repository-mode-update", { project_id: projectID, repository_mode: "on", expected_version: 2 })
    await runJSON(binary, dbPath, "product-knowledge-home-designate", { product_id: productID, project_id: projectID, locator_id: "repo-ticket", expected_version: 2 })
    await runJSON(binary, dbPath, "client-register", { client_ref: "opencode", key_id: "ticket-e2e-key", principal_ref: "operator-1", public_key: fixturePublicKeyBase64(new Uint8Array(32).fill(11)), capabilities: ["product_read", "work_define", "work_transition", "work_relate"], product_scope: [productID], project_scope: [projectID], agent_scope: ["Concord"] })
    const fixture = join(root, "fixture.json")
    await Bun.write(fixture, JSON.stringify({ binary, dbPath, repo, worktree: join(root, "worktree"), ticket: join(root, "ticket.json"), snapshot: join(root, "snapshot.json") }))
    for (const childPhase of ["ask", "resume"]) {
      const result = await runFixtureProcess([process.execPath, "test", import.meta.path, "-t", name], "", { env: { ...process.env, TEST_CONCORD_TICKET_FIXTURE: fixture, TEST_CONCORD_TICKET_PHASE: childPhase } })
      process.stdout.write(result.stdout)
      process.stderr.write(result.stderr)
      expect(result.exitCode, `${childPhase}: ${result.stdout}\n${result.stderr}`).toBe(0)
    }
    return
  }

  const { binary, dbPath, repo, worktree, ticket, snapshot } = await Bun.file(process.env.TEST_CONCORD_TICKET_FIXTURE!).json()
  const sessionID = phase === "ask" ? "session-ticket-a" : "session-ticket-b"
  const captured: { invoke?: JSONRecord } = {}
  configureCoreBinary("concord")
  let directory = phase === "ask" ? repo : worktree
  configureHostLease({ release: { coreBinary: "concord", releaseRoot: "/releases/v11.0.0" }, runner: { async run() { return { exitCode: 0, stdout: JSON.stringify({ pid: process.pid, pid_start: 1, release_root: "/releases/v11.0.0", core_binary: "concord", schema_version: 93, manifest_digest: manifestDigest, directory, worktree: directory }), stderr: "" } } } as never })
  configureConcordAdapter({ runner: { async run(argv: string[], input: string, signal?: AbortSignal, options?: { cwd?: string }) { if (argv[1] === "invoke") captured.invoke = JSON.parse(input); return runFixtureProcess([binary, ...argv.slice(1)], input, { cwd: options?.cwd, env: { ...process.env, CONCORD_DB_PATH: dbPath }, signal }) } } as never })
  const bindHost = async () => ConcordAdapterPlugin({ directory, client: { _client: { get: async ({ url, path }: { url: string; path: Record<string, unknown> }) => { expect(url).toBe(SESSION_ROUTE); expect(path.id).toBe(sessionID); return { data: { id: sessionID, directory, metadata: {}, messages: [] }, response: new Response(null, { status: 200 }) } } } }, serverUrl: new URL("https://example.invalid") } as never)
  await bindHost()
  const context = { sessionID, messageID: "message-1", agent: "Concord", directory, worktree: directory, abort: new AbortController().signal, metadata: () => {}, ask: async () => {} } as any
  const invoke = async (tool: string, operation: string, input: JSONRecord) => {
    const result = await invokeConcordOperation(tool, { operation, input } as any, context)
    expect(result.outcome, `${tool}.${operation}: ${JSON.stringify(result)}`).toBe("ok")
    return result
  }
  const walk = async (workID: string, actionID: string, fields: JSONRecord) => {
    const version = dbValue(dbPath, "SELECT version FROM work_items WHERE id=?", workID).version
    return invoke("concord_work_transition", "workflow_action", { work_id: workID, expected_version: version, action_id: actionID, idempotency_key: `ticket-${actionID}`, fields })
  }

  if (phase === "ask") {
    const capture = await invoke("concord_work_define", "capture", { title: "Synthetic clarification", kind: "task", task: "Exercise clarification.", project_ids: [projectID], human_key: issueKey, remote_issue_uuid: "00000000-0000-4000-8000-000000007360", url: "https://linear.app/example/issue/CON-7360/fixture", idempotency_key: "ticket-capture" })
    const workID = (capture.changed_refs as JSONRecord[]).find((ref) => ref.entity_kind === "work_item")?.id
    expect(workID).toBeTruthy()
    await invoke("concord_work_define", "capture", { title: "Synthetic neighbor", kind: "task", task: "A second live item proves the key filter narrows.", project_ids: [projectID], human_key: neighborKey, remote_issue_uuid: "00000000-0000-4000-8000-000000007361", url: "https://linear.app/example/issue/CON-7361/fixture", idempotency_key: "ticket-capture-neighbor" })
    await git(repo, "worktree", "add", "-b", `work/${workID}`, worktree)
    directory = worktree
    context.directory = worktree
    context.worktree = worktree
    await bindHost()
    await walk(workID, "record_intake", { outcome: "proceed" })
    await walk(workID, "pick_type", { type: "change", reason: "The requested output needs a bounded change." })
    const resume = await invoke("concord_work_trace", "resume", { work_id: workID })
    const state = resume.result as JSONRecord
    expect(state).toMatchObject({ type: "change", state: "running", next_steps: ["clarify"] })
    expect(state.steps.find((step: JSONRecord) => step.id === "clarify").attempts ?? []).toEqual([])
    await Bun.write(snapshot, JSON.stringify({ workID, type: state.type, state: state.state, version: state.version, steps: state.steps, next_steps: state.next_steps }))
    await ticketStub(ticket).publish()
    return
  }

  expect(phase).toBe("resume")
  expect(await sessionDirectoryReader().sessionDirectory(sessionID)).toBe(worktree)
  const snapshotState = await Bun.file(snapshot).json()
  const unfiltered = await invoke("concord_work_browse", "list", { product_id: productID })
  expect((unfiltered.result as JSONRecord).items).toHaveLength(2)
  const listing = await invoke("concord_work_browse", "list", { linear_issue_key: issueKey, product_id: productID })
  expect(captured.invoke?.input).toEqual({ linear_issue_key: issueKey, product_id: productID })
  const items = (listing.result as JSONRecord).items as JSONRecord[]
  expect(items.map((item: JSONRecord) => item.linear_issue_key)).toEqual([issueKey])
  const workID = items[0].id
  expect(workID).toBe(snapshotState.workID)
  const resume = await invoke("concord_work_trace", "resume", { work_id: workID })
  const state = resume.result as JSONRecord
  expect({ type: state.type, state: state.state, version: state.version, steps: state.steps, next_steps: state.next_steps }).toEqual({ type: snapshotState.type, state: snapshotState.state, version: snapshotState.version, steps: snapshotState.steps, next_steps: snapshotState.next_steps })
  expect(state).toMatchObject({ type: "change", state: "running", next_steps: ["clarify"] })
  expect(state.steps.find((step: JSONRecord) => step.id === "clarify").attempts ?? []).toEqual([])
  const stub = await ticketStub(ticket).readChanges()
  expect(stub.question).toEqual(question)
  const fields = { resolved_questions: ["Retain the summary.", "Use plain text."], note: `Answer: "${stub.changes.answer.body}" Discussion: "${stub.changes.discussion.body}" Description: "${stub.changes.description.body}"` }
  await walk(workID, "record_clarify", fields)
  expect((captured.invoke?.input as JSONRecord).fields).toEqual(fields)
  const record = JSON.parse(dbValue(dbPath, "SELECT payload FROM workflow_clarify_records WHERE work_id=?", workID).payload)
  expect(record).toEqual(fields)
  expect(Object.keys(record).sort()).toEqual(["note", "resolved_questions"])
  const published = payloadSchemas.work_transition_action_variant_record_clarify.properties.fields
  expect(published.additionalProperties).toBe(false)
  expect(Object.keys(published.properties).sort()).toEqual(["note", "resolved_questions"])
  await walk(workID, "complete_step", { step_id: "clarify", note: "Clarification complete." })
  await walk(workID, "record_plan", { outcomes: "Retain a one-page summary.", approach: "Produce a plain text summary.", decisions: [{ id: "decision:format", question: "What format?", choice: "Plain text.", rationale: "The answer chose it.", rejected: [] }], touches: ["output"], merge_split: "One bounded item.", risks: "The summary may exceed one page." })
}, { timeout: 300000 })
