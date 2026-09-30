// ESLint flat config. TypeScript/Astro are covered by `npm run check` (tsc /
// astro check); ESLint lints the plain JavaScript: build scripts, e2e support,
// Astro integrations. typescript-eslint does not support TS 7 yet, so .ts is
// deliberately excluded here.
import js from '@eslint/js';
import globals from 'globals';

export default [
  {
    ignores: [
      'dist/**', 'node_modules/**', '.astro/**', 'blob-report/**', 'playwright-report/**',
      'screenshots/**', 'public/**', 'deploy/**', 'devtools/**', 'cli/**', 'backend/**',
      '**/*.ts', '**/*.tsx', '**/*.astro',
    ],
  },
  js.configs.recommended,
  {
    files: ['**/*.js', '**/*.mjs', '**/*.cjs'],
    languageOptions: {
      ecmaVersion: 2024,
      sourceType: 'module',
      globals: { ...globals.browser, ...globals.node, ...globals.es2024 },
    },
    rules: {
      'no-empty': ['error', { allowEmptyCatch: true }],
      'no-unused-vars': ['warn', { argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrors: 'none' }],
      'no-control-regex': 'off',
      'no-useless-escape': 'off',
    },
  },
];
