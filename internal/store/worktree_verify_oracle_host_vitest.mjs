import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { resolve, relative, isAbsolute, sep } from 'node:path';
import { pathToFileURL } from 'node:url';
import { spawnSync } from 'node:child_process';

const [phase, planFile, reportFile, selected] = process.argv.slice(2);
const root = process.cwd();
const plan = JSON.parse(readFileSync(planFile, 'utf8'));
const requireCondition = (condition, message) => { if (!condition) throw new Error(message); };
requireCondition(phase === 'collect' || phase === 'run', 'unknown producer stage');
requireCondition(process.versions.node === '24.15.0', 'Node pin changed');
for (const [pkg, pin] of [['vitest', '4.1.11'], ['vite', '8.2.2']]) {
  requireCondition(JSON.parse(readFileSync(resolve('node_modules', pkg, 'package.json'), 'utf8')).version === pin, `${pkg} pin changed`);
}
const canonical = value => JSON.stringify(value).replace(/\u2028/g, '\\u2028').replace(/\u2029/g, '\\u2029');
const contained = name => {
  const result = relative(root, resolve(name));
  requireCondition(result && result !== '..' && !result.startsWith(`..${sep}`) && !isAbsolute(result), 'runner input escapes root');
  return result.split(sep).join('/');
};
// Named projects with `extends: true` reload the root config file and never see
// inline overrides, so Vite would default their cache into the digested
// node_modules. The producer wrapper gives every server a fresh stage cache.
const stageDir = mkdtempSync(resolve(process.env.TMPDIR, 'vitest-stage-'));
const wrapper = resolve(stageDir, 'vitest.config.mjs');
writeFileSync(wrapper, [
  `import pinned from ${JSON.stringify(pathToFileURL(resolve(plan.config)).href)};`,
  `export default async env => ({ ...(typeof pinned === 'function' ? await pinned(env) : await pinned), cacheDir: ${JSON.stringify(resolve(stageDir, 'vite-cache'))} });`,
  '',
].join('\n'), { flag: 'wx', mode: 0o600 });
const pinned = name => {
  if (resolve(name) === wrapper) return;
  const file = contained(name);
  requireCondition(plan.pinned.includes(file) || file.startsWith('node_modules/'), `un-pinned configuration input: ${file}`);
};
const files = [...new Set(plan.witnesses.map(witness => JSON.parse(witness)[1]))];
if (plan.sveltekit) {
  pinned('svelte.config.js');
  pinned('tsconfig.json');
  const sync = spawnSync(process.execPath, ['node_modules/@sveltejs/kit/svelte-kit.js', 'sync'], { stdio: 'inherit', env: process.env });
  requireCondition(!sync.error && sync.status === 0, 'fresh SvelteKit input generation failed');
}
const report = { version: 1, phase, collection: [], callbacks: [], results: [], end: 0, complete: false };
const record = (test, state) => {
  const ancestors = [];
  for (let parent = test.parent; parent.type !== 'module'; parent = parent.parent) {
    requireCondition(parent.type === 'suite' && ancestors.length < 16, 'invalid ancestry');
    ancestors.unshift(parent.name);
  }
  const options = test.options;
  requireCondition(!options.fails && !options.concurrent && !options.shuffle && !options.retry && !options.repeats, 'non-serial or non-literal test outcome');
  return {
    id: test.id,
    witness: canonical([test.project.name, contained(test.module.moduleId), ancestors, test.name]),
    state,
  };
};
const boundedPush = (list, value) => {
  requireCondition(list.length < 40, 'callback population exceeds bound');
  list.push(value);
};
const reporter = {
  onTestModuleCollected(module) {
    for (const test of module.children.allTests()) boundedPush(report.callbacks, record(test, test.options.mode));
  },
  onTestCaseResult(test) { boundedPush(report.results, record(test, test.result().state)); },
  onTestRunEnd(modules, errors, reason) {
    report.end++;
    requireCondition(modules.length === 1 && errors.length === 0 && reason === 'passed', 'incomplete or failed run');
  },
};
const { createVitest } = await import(pathToFileURL(resolve('node_modules/vitest/dist/node.js')).href);
let ctx;
try {
  ctx = await createVitest('test', {
    root, config: wrapper, watch: false, reporters: [reporter],
    maxWorkers: 1, fileParallelism: false, pool: 'forks',
    allowOnly: false, passWithNoTests: false, retry: 0,
    coverage: { enabled: false },
    sequence: { concurrent: false, shuffle: false },
  }, {
    configLoader: 'runner',
    envDir: false,
  });
  requireCondition(ctx.config.maxWorkers === 1 && ctx.config.fileParallelism === false, 'unbounded runner concurrency');
  for (const name of ctx.vite.config.configFileDependencies ?? []) pinned(name);
  for (const project of ctx.projects) {
    requireCondition(project.name && !project.config.browser.enabled && !project.config.typecheck.enabled, 'named Node projects only');
    requireCondition(!project.config.testNamePattern && !project.config.shard && !project.config.changed && !project.config.related, 'configuration filters are unsupported');
    requireCondition(project.config.pool === 'forks' && !project.config.retry, 'non-serial project execution');
    for (const name of project.vite.config.configFileDependencies ?? []) pinned(name);
    for (const name of [...project.config.setupFiles, ...project.config.globalSetup]) pinned(name);
  }
  const specs = await ctx.globTestSpecifications(files);
  requireCondition(specs.length > 0 && specs.length <= 40, 'empty or unbounded specification population');
  // 4.1.11 collectTests runs registration/setup, not test bodies. It does not
  // emit reporter callbacks. Execution below must independently witness the
  // same API IDs and structural ancestry through real reporter callbacks.
  const collected = await ctx.collectTests(specs);
  requireCondition(collected.unhandledErrors.length === 0, 'collection errors');
  const tests = [];
  for (const module of collected.testModules) {
    requireCondition(module.errors().length === 0, 'module collection errors');
    for (const test of module.children.allTests()) {
      requireCondition(test.result().state === 'pending', 'collection executed or skipped a test');
      const item = record(test, test.options.mode);
      requireCondition(item.state === 'run', 'skipped/todo/only registration');
      boundedPush(report.collection, item);
      tests.push(test);
    }
  }
  const population = report.collection.map(item => item.witness).sort();
  requireCondition(new Set(population).size === population.length && new Set(report.collection.map(item => item.id)).size === population.length, 'duplicate identity or API-ID collision');
  requireCondition(canonical(population) === canonical([...plan.witnesses].sort()), 'collection differs from selected population');
  if (phase === 'run') {
    const test = tests.find(test => record(test, 'run').witness === selected);
    requireCondition(test, 'selected witness missing');
    const specification = test.toTestSpecification();
    requireCondition(specification.testIds?.length === 1 && specification.testIds[0] === test.id, 'API-ID selection missing');
    const result = await ctx.runTestSpecifications([specification]);
    requireCondition(result.unhandledErrors.length === 0, 'run errors');
    requireCondition(report.end === 1, 'missing terminal callback');
  } else {
    requireCondition(report.callbacks.length === 0 && report.results.length === 0 && report.end === 0, 'collection ran acceptance');
  }
  report.complete = true;
} finally {
  if (ctx) await ctx.close();
}
const output = canonical(report);
requireCondition(Buffer.byteLength(output) <= 256 * 1024, 'report exceeds bound');
writeFileSync(reportFile, output, { flag: 'wx', mode: 0o600 });
