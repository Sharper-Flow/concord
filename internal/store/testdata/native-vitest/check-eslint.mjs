import { ESLint } from 'eslint';

const eslint = new ESLint();
const results = await eslint.lintFiles(['src']);
console.log(await (await eslint.loadFormatter('stylish')).format(results));
if (results.some(result => result.errorCount || result.warningCount)) process.exitCode = 1;
