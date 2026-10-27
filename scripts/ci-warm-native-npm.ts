// Networked CI preparation only; never invoked by the offline native oracle.
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

export function archiveSpecs(lock: any): string[] {
  if (![1, 2].includes(lock?.lockfileVersion) || !lock.packages || typeof lock.packages !== "object" || Array.isArray(lock.packages)) {
    throw new Error("unsupported fixture lock");
  }
  const specs = new Set<string>();
  for (const [key, entry] of Object.entries(lock.packages)) {
    if (!Array.isArray(entry) || typeof entry[0] !== "string") throw new Error(`invalid lock entry: ${key}`);
    // Bundled members have no independent registry archive. Their containing
    // registry archive is warmed regardless of platform selection.
    if (entry.length === 4 && entry[1] === "" && entry[2]?.bundled === true) continue;
    const spec = entry[0];
    if (entry.length !== 4 || !/^(?:@[a-z0-9._-]+\/)?[a-z0-9._-]+@\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/.test(spec)
        || entry[1] !== "" || typeof entry[3] !== "string" || !/^sha512-[A-Za-z0-9+/]+={0,2}$/.test(entry[3])) {
      throw new Error(`non-public-registry fixture entry: ${key}`);
    }
    specs.add(spec);
  }
  if (!specs.size) throw new Error("empty fixture archive closure");
  return [...specs].sort();
}

async function main() {
  if (Bun.version !== "1.4.0") throw new Error("Bun 1.4.0 required");
  const cache = process.env.OC_NATIVE_NPM_CACHE;
  if (!cache || !cache.startsWith("/")) throw new Error("OC_NATIVE_NPM_CACHE must be an absolute private cache path");
  // One public fixture, one manifest/lock lookup. Never inspect consumer paths.
  const fixture = resolve(import.meta.dir, "../internal/store/testdata/native-vitest");
  const manifest = JSON.parse(await readFile(join(fixture, "package.json"), "utf8"));
  const lock = (await import(pathToFileURL(join(fixture, "bun.lock")).href)).default;
  if (JSON.stringify(Object.entries(manifest.devDependencies ?? {}).sort()) !==
      JSON.stringify(Object.entries(lock.workspaces?.[""]?.devDependencies ?? {}).sort())) {
    throw new Error("fixture manifest/lock dependency drift");
  }
  const specs = archiveSpecs(lock);
  const scratch = await mkdtemp(join(tmpdir(), "native-npm-warm-"));
  try {
    const userconfig = join(scratch, "user.npmrc");
    const globalconfig = join(scratch, "global.npmrc");
    await Bun.write(userconfig, "");
    await Bun.write(globalconfig, "");
    // npm's CLI configuration fixes registry/cache/config; no inherited npm or
    // Node overrides, project .npmrc, credentials, lifecycle, or package install.
    const env = { PATH: process.env.PATH!, HOME: scratch, LANG: "C.UTF-8" };
    const node = Bun.spawnSync(["node", "--version"], { env, cwd: scratch });
    if (node.exitCode || node.stdout.toString().trim() !== "v24.15.0") throw new Error("Node 24.15.0 required");
    for (const spec of specs) {
      const child = Bun.spawn(["npm", "cache", "add", "--ignore-scripts", "--registry=https://registry.npmjs.org/",
        `--cache=${cache}`, `--userconfig=${userconfig}`, `--globalconfig=${globalconfig}`, "--", spec],
        { cwd: scratch, env, stdout: "inherit", stderr: "inherit" });
      if (await child.exited !== 0) throw new Error(`npm archive warming failed: ${spec}`);
    }
    console.log(`Warmed ${specs.length} public fixture archives into ${cache}`);
  } finally {
    await rm(scratch, { recursive: true, force: true });
  }
}

if (import.meta.main) await main();
