/* build.mjs — bundle the per-page ES modules into ONE request per page.
 *
 * The app is a no-framework multi-page SPA: every HTML loads js/pages/<page>.js
 * which pulls ~14 shared modules (util, ui, shell, api, cart…). On slow 4G that
 * module graph costs a waterfall of round trips before the page can render.
 *
 * This build turns each page entry into a single bundled, minified file in
 * js/bundle/, with --splitting so code shared by pages (the whole shell) is
 * emitted ONCE into hashed chunks and cached across pages — exactly the
 * runtime semantics of the unbundled app (a client-side navigation re-executes
 * only the page entry; the shared chunk stays in the module map, so top-level
 * side effects like shell's window listeners never run twice).
 *
 * Dev without a build: the HTML files reference js/bundle/<page>.js; if you
 * edit js/pages/*.js, rerun:
 *   npm i           (first time)
 *   node build.mjs
 */
import { build } from 'esbuild';
import { readdirSync } from 'node:fs';

const pages = readdirSync('js/pages')
  .filter((f) => f.endsWith('.js'))
  .map((f) => `js/pages/${f}`);

const result = await build({
  entryPoints: pages,
  outdir: 'js/bundle',
  bundle: true,
  splitting: true,       // shared shell/ui/util → ONE hashed chunk, cached across pages
  format: 'esm',
  minify: true,          // whitespace + identifier mangling (Lighthouse "minify JS")
  target: ['es2020'],
  chunkNames: 'chunks/[name]-[hash]',
  entryNames: '[name]',
  sourcemap: false,
  logLevel: 'info',
  metafile: true,
  external: ['dom-parser'], // vendored identicons' dead Node branch (browser uses global DOMParser)
});

const ins = Object.keys(result.metafile.inputs).length;
console.log(`bundled ${pages.length} pages from ${ins} source modules → js/bundle/`);
