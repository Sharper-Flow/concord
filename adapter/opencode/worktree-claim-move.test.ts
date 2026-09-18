// A claimed worktree is the session's worktree only when the session runs in
// it. These tests hold the claim hook to that contract: the move runs after a
// successful claim, the landing is read back from the host, and every failure
// mode is a typed refusal whose remedy is an idempotent replay (issue #822).
import { afterEach, afterAll, describe, expect, test } from "bun:test"
import { mkdir, mkdtemp, rm, symlink } from "node:fs/promises"
import { join, resolve } from "node:path"
import { configureHostLease } from "./host-lease"
import ConcordAdapterPlugin from "./concord-plugin"
import { hostControlPlane } from "./move-session"
import { moveSessionToClaimedWorktree } from "./concord"
import { armedClaimedWorktree, clearClaimedWorktree } from "./claimed-worktree"
import { ensureConductLink } from "./project-link"

const context = (overrides: Partial<Parameters<typeof moveSessionToClaimedWorktree>[1]> = {}) =>
  ({ sessionID: "session-1", messageID: "message-1", abort: new AbortController().signal, directory: "/old", ...overrides }) as Parameters<typeof moveSessionToClaimedWorktree>[1]

const claimArgs = (path: string) => ({ operation: "worktree_claim", input: { work_id: "work-1", path } }) as Parameters<typeof moveSessionToClaimedWorktree>[0]

const okEnvelope = () => ({ schema_version: "1.0", outcome: "ok" }) as Parameters<typeof moveSessionToClaimedWorktree>[2]

async function fakeHost(handlers: { post?: (url: string, body: any) => { status: number; body: any }; get?: (url: string) => { status: number; body: any } }) {
  const raw = {
    post: async (_url: string, body: any) => {
      const result = handlers.post?.(_url, body) ?? { status: 204, body: null }
      return { data: result.body, response: new Response(null, { status: result.status }) }
    },
    get: async (_url: string) => {
      const result = handlers.get?.(_url) ?? { status: 200, body: { directory: "/claimed" } }
      return { data: result.body, response: new Response(null, { status: result.status }) }
    },
  }
  await ConcordAdapterPlugin({ client: { _client: raw } as never, serverUrl: new URL("http://127.0.0.1:4096") })
}

afterEach(async () => {
  await ConcordAdapterPlugin({})
})

describe("worktree_claim moves the session into the claimed worktree", () => {
  test("returns the successful envelope when the session lands in the claimed path", async () => {
    await fakeHost({})
    const envelope = await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), okEnvelope())
    expect(envelope.outcome).toBe("ok")
  })

  // The confirmed landing arms the session's active claimed worktree, so the
  // dispatch path can compare the host's answer against a record the host
  // does not own.
  test("arms the session's claimed worktree once the landing is confirmed", async () => {
    await fakeHost({})
    try {
      const envelope = await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), okEnvelope())
      expect(envelope.outcome).toBe("ok")
      expect(armedClaimedWorktree("session-1")).toBe("/claimed")
    } finally {
      clearClaimedWorktree("session-1")
    }
  })

  // A refused landing never happened, so nothing may be armed for dispatch.
  test("arms nothing when the landing mismatch refuses", async () => {
    await fakeHost({ get: () => ({ status: 200, body: { directory: "/elsewhere" } }) })
    try {
      const envelope = await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), okEnvelope())
      expect(envelope.outcome).toBe("error")
      expect(armedClaimedWorktree("session-1")).toBeNull()
    } finally {
      clearClaimedWorktree("session-1")
    }
  })

  test("adds the conduct entry to an empty instructions array", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-link-"))
    const config = join(worktree, ".opencode", "opencode.json")
    try {
      await mkdir(join(worktree, ".git"))
      await mkdir(join(worktree, ".opencode"), { recursive: true })
      await Bun.write(config, '{\n  "instructions": []\n}\n')
      await fakeHost({ get: () => ({ status: 200, body: { directory: worktree } }) })

      const envelope = await moveSessionToClaimedWorktree(claimArgs(worktree), context(), okEnvelope())

      expect(envelope.outcome).toBe("ok")
      expect(JSON.parse(await Bun.file(config).text())).toEqual({ instructions: ["current/instructions/*.md"] })
      const ownership = JSON.parse(await Bun.file("project-link-ownership.json").text()) as { links: Record<string, { action: string; scope: string }> }
      expect(ownership.links[resolve(config)]).toMatchObject({ action: "restore", scope: "worktree" })
    } finally {
      await rm(worktree, { recursive: true, force: true })
    }
  })

  test("adds the conduct entry when a JSONC object has a trailing comma", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-jsonc-"))
    const config = join(worktree, ".opencode", "opencode.jsonc")
    try {
      await mkdir(join(worktree, ".git"))
      await mkdir(join(worktree, ".opencode"), { recursive: true })
      await Bun.write(config, '{\n  "theme": "dark",\n}\n')
      await fakeHost({ get: () => ({ status: 200, body: { directory: worktree } }) })

      const envelope = await moveSessionToClaimedWorktree(claimArgs(worktree), context(), okEnvelope())

      expect(envelope.outcome).toBe("ok")
      const text = await Bun.file(config).text()
      expect(text).toContain('"instructions": [')
      expect(text).toContain("current/instructions/*.md")
    } finally {
      await rm(worktree, { recursive: true, force: true })
    }
  })

  test("adds the conduct entry when an object trailing comma has JSONC comments", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-jsonc-object-comments-"))
    const config = join(worktree, ".opencode", "opencode.jsonc")
    try {
      await mkdir(join(worktree, ".git"))
      await mkdir(join(worktree, ".opencode"), { recursive: true })
      await Bun.write(config, '{\n  "theme": "dark", // a line comment\n  /* a block comment */\n}\n')

      await ensureConductLink(resolve(worktree), new AbortController().signal)

      const linked = await Bun.file(config).text()
      expect(linked).toContain('"theme": "dark", // a line comment')
      expect(linked).toContain('"instructions": [')
      expect(linked).not.toContain("*/,\n")
    } finally {
      await rm(worktree, { recursive: true, force: true })
    }
  })

  test("adds the conduct entry when an instructions array ends with comments", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-jsonc-array-comments-"))
    const config = join(worktree, ".opencode", "opencode.jsonc")
    try {
      await mkdir(join(worktree, ".git"))
      await mkdir(join(worktree, ".opencode"), { recursive: true })
      await Bun.write(config, '{\n  "instructions": [\n    "contains // and /* markers", // a line comment\n    /* a block comment */\n  ]\n}\n')

      await ensureConductLink(resolve(worktree), new AbortController().signal)

      const linked = await Bun.file(config).text()
      expect(linked).toContain('"contains // and /* markers", // a line comment')
      expect(linked).toContain("current/instructions/*.md")
    } finally {
      await rm(worktree, { recursive: true, force: true })
    }
  })

  test("parses an instructions entry that contains an open bracket", async () => {
    await mkdir("worktrees", { recursive: true })
    for (const [suffix, comma] of [["without-comma", ""], ["with-comma", ","]]) {
      const worktree = await mkdtemp(join("worktrees", `adapter-jsonc-array-string-bracket-${suffix}-`))
      const config = join(worktree, ".opencode", "opencode.jsonc")
      try {
        await mkdir(join(worktree, ".git"))
        await mkdir(join(worktree, ".opencode"), { recursive: true })
        await Bun.write(config, `{\n  "instructions": ["contains [ an open bracket"${comma}]\n}\n`)

        await ensureConductLink(resolve(worktree), new AbortController().signal)

        const linked = await Bun.file(config).text()
        const parsed = JSON.parse(linked.replaceAll(",]", "]").replaceAll(",}", "}")) as { instructions: string[] }
        expect(parsed.instructions).toContain("current/instructions/*.md")
      } finally {
        await rm(worktree, { recursive: true, force: true })
      }
    }
  })

  test("keeps object trailing-comma detection bounded for repeated block comments", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-jsonc-adversarial-"))
    const config = join(worktree, ".opencode", "opencode.jsonc")
    const adversarialComments = "/*" + "x".repeat(32) + "*/"
    try {
      await mkdir(join(worktree, ".git"))
      await mkdir(join(worktree, ".opencode"), { recursive: true })
      await Bun.write(config, `{"theme":"dark",${adversarialComments.repeat(25)}"final":"value"}\n`)

      const started = performance.now()
      await ensureConductLink(resolve(worktree), new AbortController().signal)
      const elapsed = performance.now() - started

      expect(elapsed).toBeLessThan(1000)
    } finally {
      await rm(worktree, { recursive: true, force: true })
    }
  })

  test("keeps instructions-array trailing-comma detection bounded for repeated block comments", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-jsonc-array-adversarial-"))
    const config = join(worktree, ".opencode", "opencode.jsonc")
    const adversarialComments = "/*" + "x".repeat(32) + "*/"
    try {
      await mkdir(join(worktree, ".git"))
      await mkdir(join(worktree, ".opencode"), { recursive: true })
      await Bun.write(config, `{"instructions":["/operator/rules.md",${adversarialComments.repeat(25)}"final"]}\n`)

      const started = performance.now()
      await ensureConductLink(resolve(worktree), new AbortController().signal)
      const elapsed = performance.now() - started

      expect(elapsed).toBeLessThan(1000)
      expect(await Bun.file(config).text()).toContain("current/instructions/*.md")
    } finally {
      await rm(worktree, { recursive: true, force: true })
    }
  })

  test("locates the instructions array instead of a JSONC decoy", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-jsonc-decoy-"))
    const config = join(worktree, ".opencode", "opencode.jsonc")
    try {
      await mkdir(join(worktree, ".git"))
      await mkdir(join(worktree, ".opencode"), { recursive: true })
      await Bun.write(config, '{\n  "description": "https://example.test//instructions",\n  "comment-text": "/* not a comment */",\n  "decoy": [],\n  "instructions" // comment one\n  /* comment two */ : [\n    "/operator/rules.md"\n  ]\n}\n')
      await fakeHost({ get: () => ({ status: 200, body: { directory: worktree } }) })

      const envelope = await moveSessionToClaimedWorktree(claimArgs(worktree), context(), okEnvelope())

      expect(envelope.outcome).toBe("ok")
      const linked = await Bun.file(config).text()
      expect(linked).toContain('"description": "https://example.test//instructions"')
      expect(linked).toContain('"decoy": []')
      expect(linked).toContain("current/instructions/*.md")
    } finally {
      await rm(worktree, { recursive: true, force: true })
    }
  })

  test("records ownership for an adapter-created worktree config", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-created-link-"))
    const config = join(worktree, ".opencode", "opencode.json")
    try {
      await mkdir(join(worktree, ".git"))
      await fakeHost({ get: () => ({ status: 200, body: { directory: worktree } }) })

      const envelope = await moveSessionToClaimedWorktree(claimArgs(worktree), context(), okEnvelope())

      expect(envelope.outcome).toBe("ok")
      const ownership = JSON.parse(await Bun.file("project-link-ownership.json").text()) as { links: Record<string, { action: string; scope: string }> }
      expect(ownership.links[resolve(config)]).toMatchObject({ action: "remove", scope: "worktree" })
    } finally {
      await rm(worktree, { recursive: true, force: true })
    }
  })

  test("recovers ownership after a config write before the ownership hash", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-link-recovery-"))
    const config = join(worktree, ".opencode", "opencode.json")
    const updated = JSON.stringify({ instructions: ["current/instructions/*.md"] }, null, 2) + "\n"
    try {
      await mkdir(join(worktree, ".git"))
      await mkdir(join(worktree, ".opencode"), { recursive: true })
      await Bun.write(config, updated)
      await Bun.write("project-link-pending.json", JSON.stringify({ schema: 1, links: { [resolve(config)]: { action: "remove", before: null, original: null, updated, scope: "worktree" } } }) + "\n")
      await fakeHost({ get: () => ({ status: 200, body: { directory: worktree } }) })

      const envelope = await moveSessionToClaimedWorktree(claimArgs(worktree), context(), okEnvelope())

      expect(envelope.outcome).toBe("ok")
      const ownership = JSON.parse(await Bun.file("project-link-ownership.json").text()) as { links: Record<string, { action: string; scope: string }> }
      expect(ownership.links[resolve(config)]).toMatchObject({ action: "remove", scope: "worktree" })
      expect(await Bun.file("project-link-pending.json").exists()).toBe(false)
    } finally {
      await rm(worktree, { recursive: true, force: true })
      await rm("project-link-pending.json", { force: true })
    }
  })

  test("serializes ownership records for concurrent adapter-created configs", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktrees = await Promise.all(Array.from({ length: 20 }, (_, index) => mkdtemp(join("worktrees", `adapter-concurrent-${index}-`))))
    try {
      await Promise.all(worktrees.map(async (worktree) => {
        await mkdir(join(worktree, ".git"))
        await ensureConductLink(resolve(worktree), new AbortController().signal)
      }))

      const ownership = JSON.parse(await Bun.file("project-link-ownership.json").text()) as { links: Record<string, { action: string; scope: string }> }
      for (const worktree of worktrees) {
        expect(ownership.links[resolve(worktree, ".opencode", "opencode.json")]).toMatchObject({ action: "remove", scope: "worktree" })
      }
    } finally {
      await Promise.all(worktrees.map((worktree) => rm(worktree, { recursive: true, force: true })))
    }
  })

  test("refuses an outside pending recovery path", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-pending-outside-"))
    const outside = join(".", "adapter-pending-outside.json")
    const updated = JSON.stringify({ instructions: ["current/instructions/*.md"] }, null, 2) + "\n"
    try {
      await mkdir(join(worktree, ".git"))
      await Bun.write(outside, updated)
      await Bun.write("project-link-pending.json", JSON.stringify({ schema: 1, links: { [resolve(outside)]: { action: "remove", before: null, original: null, updated, scope: "worktree" } } }) + "\n")

      await expect(ensureConductLink(resolve(worktree), new AbortController().signal)).rejects.toThrow("outside a managed worktree config")
      expect(await Bun.file(outside).text()).toBe(updated)
      const ownership = (await Bun.file("project-link-ownership.json").exists()
        ? JSON.parse(await Bun.file("project-link-ownership.json").text())
        : { links: {} }) as { links: Record<string, unknown> }
      expect(ownership.links[resolve(outside)]).toBeUndefined()
    } finally {
      await rm(worktree, { recursive: true, force: true })
      await rm(outside, { force: true })
      await rm("project-link-pending.json", { force: true })
    }
  })

  test("refuses a dangling config symlink without writing through it", async () => {
    await mkdir("worktrees", { recursive: true })
    const worktree = await mkdtemp(join("worktrees", "adapter-dangling-link-"))
    const config = join(worktree, ".opencode", "opencode.json")
    const outside = join(worktree, "..", "adapter-dangling-outside.json")
    try {
      await mkdir(join(worktree, ".git"))
      await mkdir(join(worktree, ".opencode"), { recursive: true })
      await Bun.write(outside, '{"keep":true}\n')
      await symlink(`${outside}/missing`, config)
      await fakeHost({ get: () => ({ status: 200, body: { directory: worktree } }) })

      const envelope = await moveSessionToClaimedWorktree(claimArgs(worktree), context(), okEnvelope())

      expect(envelope.outcome).toBe("error")
      if (envelope.outcome === "error") expect((envelope.error as { message?: string }).message).toContain("symlink")
      expect(await Bun.file(outside).text()).toBe('{"keep":true}\n')
      expect(await Bun.file(join(worktree, ".opencode", "opencode.jsonc")).exists()).toBe(false)
    } finally {
      await rm(worktree, { recursive: true, force: true })
      await rm(outside, { force: true })
    }
  })

  test("refuses a worktree symlink that resolves outside the managed root", async () => {
    await mkdir("worktrees", { recursive: true })
    const outside = await mkdtemp(join(".", "adapter-outside-"))
    const worktree = join("worktrees", "adapter-escape")
    try {
      await mkdir(join(outside, ".git"))
      await symlink(resolve(outside), worktree)
      await fakeHost({ get: () => ({ status: 200, body: { directory: worktree } }) })

      const envelope = await moveSessionToClaimedWorktree(claimArgs(worktree), context(), okEnvelope())

      expect(envelope.outcome).toBe("error")
      if (envelope.outcome === "error") expect((envelope.error as { message?: string }).message).toContain("outside the managed worktree root")
      expect(await Bun.file(join(outside, ".opencode", "opencode.json")).exists()).toBe(false)
    } finally {
      await rm(worktree, { force: true })
      await rm(outside, { recursive: true, force: true })
    }
  })

  test("refuses when the host lands the session elsewhere", async () => {
    await fakeHost({ get: () => ({ status: 200, body: { directory: "/somewhere-else" } }) })
    const envelope = await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), okEnvelope())
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") {
      const error = envelope.error as { adapter_reason?: string; recovery_action?: { kind?: string }; message?: string }
      expect(error.adapter_reason).toBe("claim_move_destination_mismatch")
      expect(error.recovery_action).toEqual({ kind: "retry_same_request" })
      expect(error.message).toContain("/claimed")
    }
  })

  test("refuses with the host's own words when the move is rejected", async () => {
    await fakeHost({ post: () => ({ status: 409, body: { data: { message: "worktree /claimed is held by another session" } } }) })
    const envelope = await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), okEnvelope())
    expect(envelope.outcome).toBe("error")
    if (envelope.outcome === "error") {
      const error = envelope.error as { adapter_reason?: string; message?: string }
      expect(error.adapter_reason).toBe("claim_move_refused")
      expect(error.message).toContain("held by another session")
      expect(error.message).toContain("replay worktree_claim")
    }
  })

  test("leaves non-claim operations and refused claims untouched", async () => {
    await fakeHost({})
    const other = { operation: "lifecycle", input: { work_id: "work-1" } } as Parameters<typeof moveSessionToClaimedWorktree>[0]
    expect(await moveSessionToClaimedWorktree(other, context(), okEnvelope())).toEqual(okEnvelope())
    const refused = { schema_version: "1.0", outcome: "error", error: { kind: "version_conflict" } } as unknown as Parameters<typeof moveSessionToClaimedWorktree>[2]
    expect(await moveSessionToClaimedWorktree(claimArgs("/claimed"), context(), refused)).toEqual(refused)
  })
})

// The factory's host-lease claim fails against the unstamped repository
// placeholder; the suite's files share one process in an order no file
// controls, so this file leaves the lease state clean.
afterAll(async () => {
  await rm("project-link-ownership.json", { force: true })
  configureHostLease({ reset: true })
})
