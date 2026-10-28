import js from '@eslint/js';
import svelte from 'eslint-plugin-svelte';
import ts from 'typescript-eslint';

export default [js.configs.recommended, ...ts.configs.recommended, ...svelte.configs.recommended, {
  files: ['**/*.svelte'],
  languageOptions: { parserOptions: { parser: ts.parser } },
}];
