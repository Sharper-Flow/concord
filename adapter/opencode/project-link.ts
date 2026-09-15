import { releaseRoot } from "./generated-release"
import fs from "node:fs"
import path from "node:path"
import { createHash } from "node:crypto"

const CONDUCT_ENTRY = path.join(path.dirname(releaseRoot), "current", "instructions", "*.md")
const PROJECT_LINK_OWNERSHIP = path.join(path.dirname(releaseRoot), "project-link-ownership.json")
const PROJECT_LINK_PENDING = path.join(path.dirname(releaseRoot), "project-link-pending.json")
const PROJECT_LINK_LOCK = path.join(path.dirname(releaseRoot), "project-link.lock")
const PROJECT_LINK_LOCK_TIMEOUT_MS = 10_000
const PROJECT_LINK_LOCK_STALE_MS = 30_000
let localProjectLinkQueue = Promise.resolve()

type ProjectLinkRecord = {
  action: "remove" | "restore" | "preserve"
  scope: "project" | "worktree" | "legacy"
  expected: { exists: boolean; sha256?: string }
  original?: string
}

type ProjectLinkOwnership = { schema: 1; links: Record<string, ProjectLinkRecord> }
type PendingProjectLinkRecord = { scope: "worktree"; action: "remove" | "restore"; before: string | null; original: string | null; updated: string }
type PendingProjectLinks = { schema: 1; links: Record<string, PendingProjectLinkRecord> }

export class ProjectLinkError extends Error {
  readonly code: string

  constructor(code: string, message: string) {
    super(message)
    this.name = "ProjectLinkError"
    this.code = code
  }
}

function stripJSONC(text: string): string {
  let result = ""
  let string = false
  let escaped = false
  let comment: "" | "line" | "block" = ""
  for (let index = 0; index < text.length; index++) {
    const character = text[index]
    const next = text[index + 1]
    if (comment === "line") {
      if (character === "\n") comment = ""
      continue
    }
    if (comment === "block") {
      if (character === "*" && next === "/") {
        comment = ""
        index++
      }
      continue
    }
    if (string) {
      result += character
      if (escaped) escaped = false
      else if (character === "\\") escaped = true
      else if (character === '"') string = false
      continue
    }
    if (character === '"') {
      string = true
      result += character
    } else if (character === "/" && next === "/") {
      comment = "line"
      index++
    } else if (character === "/" && next === "*") {
      comment = "block"
      index++
    } else {
      result += character
    }
  }
  return result.replace(/,(\s*[}\]])/g, "$1")
}

function parseProjectConfig(file: string, text: string): unknown {
  return file.endsWith(".json") ? JSON.parse(text) : JSON.parse(stripJSONC(text))
}

function validateProjectConfig(file: string, text: string): void {
  try {
    parseProjectConfig(file, text)
  } catch (error) {
    throw new Error(`cannot safely write worktree OpenCode config ${file}: ${String(error)}`)
  }
}

function jsoncObjectEnd(text: string): number {
  let depth = 0
  let inString = false
  let escaped = false
  let comment: "" | "line" | "block" = ""
  for (let index = 0; index < text.length; index++) {
    const character = text[index]
    const next = text[index + 1]
    if (comment === "line") {
      if (character === "\n") comment = ""
      continue
    }
    if (comment === "block") {
      if (character === "*" && next === "/") {
        comment = ""
        index++
      }
      continue
    }
    if (!inString && character === "/" && next === "/") {
      comment = "line"
      index++
      continue
    }
    if (!inString && character === "/" && next === "*") {
      comment = "block"
      index++
      continue
    }
    if (inString) {
      if (escaped) escaped = false
      else if (character === "\\") escaped = true
      else if (character === '"') inString = false
      continue
    }
    if (character === '"') inString = true
    else if (character === "{") depth++
    else if (character === "}" && --depth === 0) return index
  }
  throw new Error("worktree OpenCode config has an unbalanced object")
}

function hasTrailingObjectComma(text: string): boolean {
  return lastJSONCSignificantCharacter(text) === ","
}

function lastJSONCSignificantCharacter(text: string): string | undefined {
  let last: string | undefined
  let index = 0
  while (index < text.length) {
    const character = text[index]
    const next = text[index + 1]
    if (/\s/.test(character)) {
      index++
      continue
    }
    if (character === "/" && next === "/") {
      const newline = text.indexOf("\n", index + 2)
      index = newline < 0 ? text.length : newline + 1
      continue
    }
    if (character === "/" && next === "*") {
      const end = text.indexOf("*/", index + 2)
      if (end < 0) return undefined
      index = end + 2
      continue
    }
    if (character === '"') {
      let end = index + 1
      let escaped = false
      while (end < text.length) {
        const current = text[end]
        if (escaped) escaped = false
        else if (current === "\\") escaped = true
        else if (current === '"') break
        end++
      }
      if (end >= text.length) return undefined
      last = '"'
      index = end + 1
      continue
    }
    last = character
    index++
  }
  return last
}

function skipJSONCSpace(text: string, start: number): number {
  let index = start
  for (;;) {
    while (/\s/.test(text[index] ?? "")) index++
    if (text.startsWith("//", index)) {
      const newline = text.indexOf("\n", index + 2)
      index = newline < 0 ? text.length : newline + 1
      continue
    }
    if (text.startsWith("/*", index)) {
      const end = text.indexOf("*/", index + 2)
      if (end < 0) throw new Error("worktree OpenCode config contains an unterminated comment")
      index = end + 2
      continue
    }
    return index
  }
}

function jsoncArrayEnd(text: string, key: string): number {
  const marker = JSON.stringify(key)
  let objectDepth = 0
  let arrayDepth = 0
  let index = 0
  while (index < text.length) {
    const character = text[index]
    const next = text[index + 1]
    if (character === '"') {
      let end = index + 1
      let escaped = false
      while (end < text.length) {
        const current = text[end]
        if (escaped) escaped = false
        else if (current === "\\") escaped = true
        else if (current === '"') break
        end++
      }
      if (end >= text.length) break
      if (objectDepth === 1 && arrayDepth === 0 && text.slice(index, end + 1) === marker) {
        let after = skipJSONCSpace(text, end + 1)
        if (text[after] === ":") {
          after = skipJSONCSpace(text, after + 1)
          if (text[after] === "[") {
            let depth = 0
            let cursor = after
            let string = false
            let stringEscaped = false
            while (cursor < text.length) {
              const current = text[cursor]
              const following = text[cursor + 1]
              if (string) {
                if (stringEscaped) stringEscaped = false
                else if (current === "\\") stringEscaped = true
                else if (current === '"') string = false
              } else if (current === '"') string = true
              else if (current === "/" && following === "/") {
                const newline = text.indexOf("\n", cursor + 2)
                if (newline < 0) break
                cursor = newline + 1
                continue
              } else if (current === "/" && following === "*") {
                const commentEnd = text.indexOf("*/", cursor + 2)
                if (commentEnd < 0) throw new Error("worktree OpenCode config contains an unterminated comment")
                cursor = commentEnd + 2
                continue
              } else if (current === "[") depth++
              else if (current === "]" && --depth === 0) return cursor
              cursor++
            }
          }
        }
      }
      index = end + 1
      continue
    }
    if (character === "/" && next === "/") {
      const newline = text.indexOf("\n", index + 2)
      if (newline < 0) break
      index = newline + 1
      continue
    }
    if (character === "/" && next === "*") {
      const end = text.indexOf("*/", index + 2)
      if (end < 0) throw new Error("worktree OpenCode config contains an unterminated comment")
      index = end + 2
      continue
    }
    if (character === "{") objectDepth++
    else if (character === "}") objectDepth--
    else if (character === "[") arrayDepth++
    else if (character === "]") arrayDepth--
    index++
  }
  throw new Error(`worktree OpenCode config has an unbalanced ${key} array`)
}

function appendInstructionJSONC(original: string, entry: string): string {
  const marker = JSON.stringify(entry)
  const parsed = JSON.parse(stripJSONC(original)) as Record<string, unknown>
  if (parsed.instructions === undefined) {
    const end = jsoncObjectEnd(original)
    const before = original.slice(0, end)
    const separator = before.trimEnd().endsWith("{") || hasTrailingObjectComma(before) ? "" : ","
    return original.slice(0, end) + `${separator}\n  "instructions": [\n    ${marker}\n  ]\n` + original.slice(end)
  }
  if (!Array.isArray(parsed.instructions) || !parsed.instructions.every((value) => typeof value === "string")) {
    throw new Error("worktree OpenCode config has a non-array instructions entry")
  }
  if (parsed.instructions.includes(entry)) return original
  const end = jsoncArrayEnd(original, "instructions")
  const before = original.slice(0, end)
  const arrayStart = before.lastIndexOf("[")
  const trailing = before.slice(arrayStart + 1)
  const hasTrailingComma = lastJSONCSignificantCharacter(trailing) === ","
  const separator = hasTrailingComma ? "\n  " : "\n  ,\n  "
  const arraySeparator = parsed.instructions.length === 0 ? "\n  " : separator
  return before + arraySeparator + marker + "\n" + original.slice(end)
}

function managedWorktree(directory: string): boolean {
  const root = path.join(path.dirname(releaseRoot), "worktrees")
  const lexicalRoot = path.resolve(root)
  const lexicalDirectory = path.resolve(directory)
  const lexicalRelative = path.relative(lexicalRoot, lexicalDirectory)
  if (lexicalRelative === "" || lexicalRelative.startsWith("..") || path.isAbsolute(lexicalRelative)) return false
  let realRoot: string
  let realDirectory: string
  try {
    realRoot = fs.realpathSync(root)
    realDirectory = fs.realpathSync(directory)
  } catch (error) {
    throw new ProjectLinkError("managed_worktree_unreadable", `cannot resolve managed worktree ${directory}: ${String(error)}`)
  }
  const relative = path.relative(realRoot, realDirectory)
  if (relative === "" || relative.startsWith("..") || path.isAbsolute(relative)) {
    throw new ProjectLinkError("managed_worktree_escape", `managed worktree ${directory} resolves outside the managed worktree root ${realRoot}`)
  }
  return true
}

function sha256(text: string): string {
  return createHash("sha256").update(text, "utf8").digest("hex")
}

function readProjectLinkOwnership(): ProjectLinkOwnership {
  if (!fs.existsSync(PROJECT_LINK_OWNERSHIP)) return { schema: 1, links: {} }
  if (fs.lstatSync(PROJECT_LINK_OWNERSHIP).isSymbolicLink()) {
    throw new ProjectLinkError("ownership_symlink", `refusing symlinked project link ownership record ${PROJECT_LINK_OWNERSHIP}`)
  }
  let value: unknown
  try {
    value = JSON.parse(fs.readFileSync(PROJECT_LINK_OWNERSHIP, "utf8"))
  } catch (error) {
    throw new ProjectLinkError("ownership_invalid", `cannot read project link ownership record ${PROJECT_LINK_OWNERSHIP}: ${String(error)}`)
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new ProjectLinkError("ownership_invalid", `refusing malformed project link ownership record ${PROJECT_LINK_OWNERSHIP}`)
  const record = value as Record<string, unknown>
  if (record.schema !== 1 || record.links === null || typeof record.links !== "object" || Array.isArray(record.links)) throw new ProjectLinkError("ownership_invalid", `refusing malformed project link ownership record ${PROJECT_LINK_OWNERSHIP}`)
  const links = record.links as Record<string, unknown>
  if (Object.keys(links).length > 1024) throw new ProjectLinkError("ownership_invalid", `project link ownership record contains too many entries: ${PROJECT_LINK_OWNERSHIP}`)
  const validated: Record<string, ProjectLinkRecord> = {}
  for (const [file, raw] of Object.entries(links)) {
    if (path.resolve(file) !== file || path.normalize(file) !== file || raw === null || typeof raw !== "object" || Array.isArray(raw)) {
      throw new ProjectLinkError("ownership_invalid", `refusing invalid project link ownership entry for ${file}`)
    }
    const candidate = raw as Record<string, unknown>
    if (Object.keys(candidate).some((key) => !["action", "scope", "expected", "original"].includes(key))) throw new ProjectLinkError("ownership_invalid", `refusing unknown project link ownership fields for ${file}`)
    const action = candidate.action
    const scope = candidate.scope
    const expected = candidate.expected
    if (!["remove", "restore", "preserve"].includes(String(action)) || !["project", "worktree", "legacy"].includes(String(scope)) || expected === null || typeof expected !== "object" || Array.isArray(expected)) {
      throw new ProjectLinkError("ownership_invalid", `refusing invalid project link ownership entry for ${file}`)
    }
    const expectedRecord = expected as Record<string, unknown>
    if (Object.keys(expectedRecord).some((key) => key !== "exists" && key !== "sha256")) throw new ProjectLinkError("ownership_invalid", `refusing unknown expected project config fields for ${file}`)
    if (typeof expectedRecord.exists !== "boolean" || (expectedRecord.exists && (typeof expectedRecord.sha256 !== "string" || !/^[0-9a-f]{64}$/.test(expectedRecord.sha256))) || (!expectedRecord.exists && expectedRecord.sha256 !== undefined)) {
      throw new ProjectLinkError("ownership_invalid", `refusing invalid expected project config state for ${file}`)
    }
    if (action === "restore" && typeof candidate.original !== "string") throw new ProjectLinkError("ownership_invalid", `refusing missing original project config for ${file}`)
    if (action !== "restore" && candidate.original !== undefined) throw new ProjectLinkError("ownership_invalid", `refusing unexpected original project config for ${file}`)
    validated[file] = { action: action as ProjectLinkRecord["action"], scope: scope as ProjectLinkRecord["scope"], expected: expectedRecord as ProjectLinkRecord["expected"], ...(action === "restore" ? { original: candidate.original as string } : {}) }
  }
  return { schema: 1, links: validated }
}

function writeProjectLinkOwnership(ownership: ProjectLinkOwnership): void {
  const parent = path.dirname(PROJECT_LINK_OWNERSHIP)
  fs.mkdirSync(parent, { recursive: true })
  if (fs.existsSync(PROJECT_LINK_OWNERSHIP) && fs.lstatSync(PROJECT_LINK_OWNERSHIP).isSymbolicLink()) {
    throw new ProjectLinkError("ownership_symlink", `refusing symlinked project link ownership record ${PROJECT_LINK_OWNERSHIP}`)
  }
  const temporary = `${PROJECT_LINK_OWNERSHIP}.tmp-${process.pid}`
  try {
    fs.writeFileSync(temporary, JSON.stringify({ schema: 1, links: Object.fromEntries(Object.entries(ownership.links).sort(([left], [right]) => left.localeCompare(right))) }, null, 2) + "\n", { encoding: "utf8", flag: "wx" })
    fs.renameSync(temporary, PROJECT_LINK_OWNERSHIP)
  } catch (error) {
    try { fs.unlinkSync(temporary) } catch { /* best effort cleanup */ }
    throw new ProjectLinkError("ownership_write_failed", `cannot update project link ownership record ${PROJECT_LINK_OWNERSHIP}: ${String(error)}`)
  }
}

function writePendingProjectLink(file: string, original: string | null, updated: string): void {
  const parent = path.dirname(PROJECT_LINK_PENDING)
  fs.mkdirSync(parent, { recursive: true })
  const temporary = `${PROJECT_LINK_PENDING}.tmp-${process.pid}`
  const key = path.resolve(file)
  const pending: PendingProjectLinks = {
    schema: 1,
    links: { [key]: { scope: "worktree", action: original === null ? "remove" : "restore", before: original, original, updated } },
  }
  try {
    fs.writeFileSync(temporary, JSON.stringify(pending) + "\n", { encoding: "utf8", flag: "wx" })
    fs.renameSync(temporary, PROJECT_LINK_PENDING)
  } catch (error) {
    try { fs.unlinkSync(temporary) } catch { /* best effort cleanup */ }
    throw new ProjectLinkError("ownership_write_failed", `cannot prepare project link recovery record ${PROJECT_LINK_PENDING}: ${String(error)}`)
  }
}

function clearPendingProjectLink(): void {
  try {
    fs.unlinkSync(PROJECT_LINK_PENDING)
  } catch (error) {
    if (error instanceof Error && "code" in error && (error as NodeJS.ErrnoException).code === "ENOENT") return
    throw new ProjectLinkError("ownership_write_failed", `cannot remove project link recovery record ${PROJECT_LINK_PENDING}: ${String(error)}`)
  }
}

async function waitForProjectLinkLock(): Promise<void> {
  await new Promise<void>((resolve) => setTimeout(resolve, 10))
}

function processIsAlive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch (error) {
    return error instanceof Error && "code" in error && (error as NodeJS.ErrnoException).code === "EPERM"
  }
}

async function acquireProjectLinkLock(): Promise<() => void> {
  let releaseLocal!: () => void
  const previous = localProjectLinkQueue
  localProjectLinkQueue = new Promise<void>((resolve) => { releaseLocal = resolve })
  await previous
  const deadline = Date.now() + PROJECT_LINK_LOCK_TIMEOUT_MS
  try {
    for (;;) {
      const existing = lstatIfPresent(PROJECT_LINK_LOCK)
      if (existing?.isSymbolicLink()) throw new ProjectLinkError("ownership_symlink", `refusing symlinked project link lock ${PROJECT_LINK_LOCK}`)
      try {
        fs.mkdirSync(PROJECT_LINK_LOCK)
        try {
          fs.writeFileSync(path.join(PROJECT_LINK_LOCK, "owner"), `${process.pid}\n`, { encoding: "utf8", flag: "wx" })
        } catch (error) {
          fs.rmSync(PROJECT_LINK_LOCK, { recursive: true, force: true })
          throw error
        }
        return () => {
          try { fs.rmSync(PROJECT_LINK_LOCK, { recursive: true, force: true }) } finally { releaseLocal() }
        }
      } catch (error) {
        if (!(error instanceof Error && "code" in error && (error as NodeJS.ErrnoException).code === "EEXIST")) throw error
        let stale = false
        try {
          const owner = Number.parseInt(fs.readFileSync(path.join(PROJECT_LINK_LOCK, "owner"), "utf8"), 10)
          const age = Date.now() - fs.statSync(PROJECT_LINK_LOCK).mtimeMs
          stale = !Number.isInteger(owner) || (!processIsAlive(owner) && age >= PROJECT_LINK_LOCK_STALE_MS)
        } catch {
          try { stale = Date.now() - fs.statSync(PROJECT_LINK_LOCK).mtimeMs >= PROJECT_LINK_LOCK_STALE_MS } catch { stale = false }
        }
        if (stale) {
          fs.rmSync(PROJECT_LINK_LOCK, { recursive: true, force: true })
          continue
        }
        if (Date.now() >= deadline) throw new ProjectLinkError("ownership_busy", `timed out waiting for project link ownership lock ${PROJECT_LINK_LOCK}`)
        await waitForProjectLinkLock()
      }
    }
  } catch (error) {
    releaseLocal()
    throw error
  }
}

function currentProjectState(file: string): { exists: boolean; sha256?: string } {
  const stat = lstatIfPresent(file)
  if (!stat) return { exists: false }
  if (stat.isSymbolicLink() || !stat.isFile()) throw new ProjectLinkError("project_config_unsafe", `refusing non-file or symlinked worktree OpenCode config ${file}`)
  return { exists: true, sha256: sha256(fs.readFileSync(file, "utf8")) }
}

function ownershipAllowsRepair(file: string, expected: { exists: boolean; sha256?: string }): boolean {
  const actual = currentProjectState(file)
  if (!expected.exists) return !actual.exists
  return !actual.exists || actual.sha256 === expected.sha256
}

function recordProjectLink(file: string, original: string | null, updated: string, scope: "worktree"): void {
  const ownership = readProjectLinkOwnership()
  const key = path.resolve(file)
  const previous = ownership.links[key]
  const action = previous?.action === "remove" || previous?.action === "restore"
    ? previous.action
    : original === null ? "remove" : "restore"
  const next: ProjectLinkRecord = { action, scope, expected: { exists: true, sha256: sha256(updated) } }
  if (action === "restore") next.original = previous?.original ?? original ?? ""
  ownership.links[key] = next
  writeProjectLinkOwnership(ownership)
}

function validateProjectLinkAdoption(file: string): void {
  const ownership = readProjectLinkOwnership()
  const previous = ownership.links[path.resolve(file)]
  if (previous && !ownershipAllowsRepair(file, previous.expected)) {
    throw new ProjectLinkError("project_config_modified", `refusing to adopt operator-modified worktree OpenCode config ${file}`)
  }
}

function recoverPendingProjectLink(): void {
  const pendingStat = lstatIfPresent(PROJECT_LINK_PENDING)
  if (!pendingStat) return
  if (pendingStat.isSymbolicLink()) throw new ProjectLinkError("ownership_symlink", `refusing symlinked project link recovery record ${PROJECT_LINK_PENDING}`)
  let value: unknown
  try {
    value = JSON.parse(fs.readFileSync(PROJECT_LINK_PENDING, "utf8"))
  } catch (error) {
    throw new ProjectLinkError("ownership_invalid", `cannot read project link recovery record ${PROJECT_LINK_PENDING}: ${String(error)}`)
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new ProjectLinkError("ownership_invalid", `refusing malformed project link recovery record ${PROJECT_LINK_PENDING}`)
  const record = value as Partial<PendingProjectLinks>
  if (record.schema !== 1 || record.links === null || typeof record.links !== "object" || Array.isArray(record.links)) {
    throw new ProjectLinkError("ownership_invalid", `refusing malformed project link recovery record ${PROJECT_LINK_PENDING}`)
  }
  const entries = Object.entries(record.links as Record<string, unknown>)
  if (entries.length !== 1) throw new ProjectLinkError("ownership_invalid", `refusing project link recovery record with unsupported entry count ${PROJECT_LINK_PENDING}`)
  const [file, raw] = entries[0]
  if (path.resolve(file) !== file || path.normalize(file) !== file || raw === null || typeof raw !== "object" || Array.isArray(raw)) {
    throw new ProjectLinkError("ownership_invalid", `refusing malformed project link recovery entry ${PROJECT_LINK_PENDING}`)
  }
  const pending = raw as Partial<PendingProjectLinkRecord>
  if (Object.keys(raw).some((key) => !["scope", "action", "before", "original", "updated"].includes(key)) || pending.scope !== "worktree" || (pending.action !== "remove" && pending.action !== "restore") || (pending.before !== null && typeof pending.before !== "string") || typeof pending.updated !== "string" || (pending.original !== null && typeof pending.original !== "string") || (pending.action === "restore" && typeof pending.original !== "string") || (pending.action === "remove" && pending.original !== null)) {
    throw new ProjectLinkError("ownership_invalid", `refusing malformed project link recovery entry ${PROJECT_LINK_PENDING}`)
  }
  validateManagedProjectLinkPath(file)
  const before = pending.before ?? null
  const original = pending.original ?? null
  const current = currentProjectState(file)
  const updatedState = pending.updated === "" ? { exists: false } : { exists: true, sha256: sha256(pending.updated) }
  const beforeState = before === null ? { exists: false } : { exists: true, sha256: sha256(before) }
  const ownership = readProjectLinkOwnership()
  const existing = ownership.links[file]
  if (existing) {
    if (current.exists === existing.expected.exists && (!current.exists || current.sha256 === existing.expected.sha256)) {
      clearPendingProjectLink()
      return
    }
    if (current.exists === updatedState.exists && (!current.exists || current.sha256 === updatedState.sha256)) {
      ownership.links[file] = { ...existing, expected: updatedState }
      writeProjectLinkOwnership(ownership)
      clearPendingProjectLink()
      return
    }
    throw new ProjectLinkError("project_config_modified", `refusing to recover a modified worktree OpenCode config ${file}`)
  }
  if (current.exists === updatedState.exists && (!current.exists || current.sha256 === updatedState.sha256)) {
    if (pending.action === "remove") {
      ownership.links[file] = { action: "remove", scope: "worktree", expected: updatedState }
      writeProjectLinkOwnership(ownership)
    } else {
      recordProjectLink(file, original, pending.updated, "worktree")
    }
    clearPendingProjectLink()
    return
  }
  if (current.exists === beforeState.exists && (!current.exists || current.sha256 === beforeState.sha256)) {
    clearPendingProjectLink()
    return
  }
  throw new ProjectLinkError("project_config_modified", `refusing to recover a modified worktree OpenCode config ${file}`)
}

function validateManagedProjectLinkPath(file: string): void {
  if (path.basename(file) !== "opencode.json" && path.basename(file) !== "opencode.jsonc") {
    throw new ProjectLinkError("ownership_invalid", `refusing project link recovery path outside a managed worktree config ${file}`)
  }
  const configDir = path.dirname(file)
  if (path.basename(configDir) !== ".opencode") {
    throw new ProjectLinkError("ownership_invalid", `refusing project link recovery path outside a managed worktree config ${file}`)
  }
  const root = path.resolve(path.dirname(releaseRoot), "worktrees")
  const worktree = path.dirname(configDir)
  const lexical = path.relative(root, worktree)
  if (lexical === "" || lexical.startsWith("..") || path.isAbsolute(lexical)) {
    throw new ProjectLinkError("ownership_invalid", `refusing project link recovery path outside the managed worktree root ${root}`)
  }
  let realRoot: string
  let realWorktree: string
  try {
    realRoot = fs.realpathSync(root)
    realWorktree = fs.realpathSync(worktree)
  } catch (error) {
    throw new ProjectLinkError("ownership_invalid", `cannot resolve project link recovery path ${file}: ${String(error)}`)
  }
  const relative = path.relative(realRoot, realWorktree)
  if (relative === "" || relative.startsWith("..") || path.isAbsolute(relative) || realWorktree !== worktree) {
    throw new ProjectLinkError("ownership_invalid", `refusing project link recovery path outside a managed worktree ${file}`)
  }
  const configStat = lstatIfPresent(configDir)
  if (configStat?.isSymbolicLink()) throw new ProjectLinkError("ownership_symlink", `refusing symlinked worktree OpenCode directory ${configDir}`)
}

function lstatIfPresent(file: string): fs.Stats | null {
  try {
    return fs.lstatSync(file)
  } catch (error) {
    if (error instanceof Error && "code" in error && (error as NodeJS.ErrnoException).code === "ENOENT") return null
    throw error
  }
}

export async function ensureConductLink(directory: string, signal: AbortSignal): Promise<void> {
  if (signal.aborted || !managedWorktree(directory)) return
  const releaseLock = await acquireProjectLinkLock()
  try {
    await ensureConductLinkLocked(directory, signal)
  } finally {
    releaseLock()
  }
}

async function ensureConductLinkLocked(directory: string, signal: AbortSignal): Promise<void> {
  recoverPendingProjectLink()
  const configDir = path.join(directory, ".opencode")
  const configDirStat = lstatIfPresent(configDir)
  if (configDirStat?.isSymbolicLink()) throw new Error(`refusing symlinked worktree OpenCode directory ${configDir}`)
  fs.mkdirSync(configDir, { recursive: true })
  const json = path.join(configDir, "opencode.json")
  const jsonc = path.join(configDir, "opencode.jsonc")
  const jsonStat = lstatIfPresent(json)
  const jsoncStat = lstatIfPresent(jsonc)
  if (jsonStat?.isSymbolicLink()) throw new Error(`refusing symlinked worktree OpenCode config ${json}`)
  if (jsoncStat?.isSymbolicLink()) throw new Error(`refusing symlinked worktree OpenCode config ${jsonc}`)
  const target = jsonStat ? json : jsoncStat ? jsonc : json
  if (!lstatIfPresent(target)) {
    const created = JSON.stringify({ instructions: [CONDUCT_ENTRY] }, null, 2) + "\n"
    validateProjectConfig(target, created)
    validateProjectLinkAdoption(target)
    writePendingProjectLink(target, null, created)
    await Bun.write(target, created)
    recordProjectLink(target, null, created, "worktree")
    clearPendingProjectLink()
    return
  }
  const original = await Bun.file(target).text()
  let parsed: unknown
  try {
    parsed = parseProjectConfig(target, original)
  } catch {
    throw new Error(`cannot parse worktree OpenCode config ${target}`)
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(`worktree OpenCode config ${target} is not an object`)
  const updated = appendInstructionJSONC(original, CONDUCT_ENTRY)
  if (updated !== original) {
    validateProjectConfig(target, updated)
    validateProjectLinkAdoption(target)
    writePendingProjectLink(target, original, updated)
    await Bun.write(target, updated)
    recordProjectLink(target, original, updated, "worktree")
    clearPendingProjectLink()
  }
}
