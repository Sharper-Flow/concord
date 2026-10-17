import { afterEach, expect, test } from "bun:test"
import { copyFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

const roots: string[] = []
afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true })
})

function auditFixture(suffix: string, content: string) {
  const root = mkdtempSync(join(tmpdir(), "public-content-"))
  roots.push(root)
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("GIT_")))
  const initialized = Bun.spawnSync(["git", "init", "--quiet", root], { env })
  expect(initialized.exitCode).toBe(0)
  mkdirSync(join(root, "scripts"))
  copyFileSync(new URL("../../scripts/check-public-content.py", import.meta.url), join(root, "scripts/check-public-content.py"))
  writeFileSync(join(root, `fixture${suffix}`), content)
  return Bun.spawnSync(["python3", "scripts/check-public-content.py"], { cwd: root, env })
}

test.each([".go", ".ts", ".mjs", ".js"])("public-content checker rejects a loopback URL in %s", (suffix) => {
  const result = auditFixture(suffix, `const url = "http://${[127, 0, 0, 1].join(".")}:4096"\n`)
  expect(result.exitCode).toBe(1)
  expect(result.stdout.toString()).toContain(`fixture${suffix}:1: private host`)
  expect(result.stderr.toString()).toContain("Public-content audit failed:")
})

test.each([".go", ".ts", ".mjs", ".js"])("public-content checker accepts a synthetic URL in %s", (suffix) => {
  const result = auditFixture(suffix, 'const url = "https://example.invalid"\n')
  expect(result.exitCode).toBe(0)
  expect(result.stdout.toString()).toContain("Public-content audit passed")
  expect(result.stderr.toString()).toBe("")
})

const callableFixtures = [
  ["worker evidence key", "const privateKey = privateKeyObject(await credentials.getPrivateKey(clientRef()))"],
  ["reoccupy fixture key", 'const privateKey = createPrivateKey({ key: Buffer.concat([PRIVATE_KEY_PREFIX, Buffer.from(PRIVATE_SEED)]), format: "der", type: "pkcs8" })'],
  ["arbitrary callee", "const privateKey = unrelatedFactoryFunction()"],
  ["spaced callee", "const privateKey = unrelatedFactoryFunction ()"],
  ["member call", "const privateKey = unrelatedFactoryObject.createKey()"],
] as const

for (const suffix of [".go", ".ts", ".mjs", ".js"]) {
  test.each(callableFixtures)(`public-content checker accepts %s call expressions in ${suffix}`, (_label, content) => {
    const result = auditFixture(suffix, content)
    expect(result.exitCode).toBe(0)
    expect(result.stdout.toString()).toContain("Public-content audit passed")
    expect(result.stderr.toString()).toBe("")
  })

  const literalFixtures = [
    ["quoted assignment", `const privateKey = "${"s".repeat(24)}"`, "credential assignment"],
    ["bare assignment", `privateKey = ${"s".repeat(24)}`, "credential assignment"],
    ["quoted call-like value", 'const privateKey = "' + 'unrelatedFactoryFunction()"', "credential assignment"],
    ["quoted call argument", `const k = decode("sk-live-${"s".repeat(24)}")`, "credential shape"],
    ["bare call argument", `const k = decode(sk-live-${"s".repeat(24)})`, "credential shape"],
    ["credential in a factory argument", `const privateKey = unrelatedFactoryFunction("sk-live-${"s".repeat(24)}")`, "credential shape"],
    ["later literal on the same line", `const privateKey = unrelatedFactoryFunction(); const secret = "${"s".repeat(24)}"`, "credential assignment"],
  ] as const
  test.each(literalFixtures)(`public-content checker rejects %s in ${suffix}`, (_label, content, finding) => {
    const result = auditFixture(suffix, content)
    expect(result.exitCode).toBe(1)
    expect(result.stdout.toString()).toContain(`fixture${suffix}:1: ${finding}`)
    expect(result.stderr.toString()).toContain("Public-content audit failed:")
  })
}
