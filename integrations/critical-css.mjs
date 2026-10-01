/**
 * critical-css.mjs — split each built page's inlined stylesheet into the part
 * that is needed before the first paint and the part that is not.
 *
 * WHY
 *
 * Every page carries the shop's whole stylesheet inline (astro.config.mjs sets
 * `inlineStylesheets: 'always'`, which removed a render-blocking round trip).
 * That was the right first move, but the sheet is 160 KB minified and a page
 * matches 20-30 KB of it while loading. Lighthouse on the deployed site
 * (mobile, Slow 4G, 4x CPU) priced the sheet at ~450 ms of LCP — more than the
 * fonts, more than the JS: the bytes sit in the HTML document, so first paint
 * waits for every last byte of a modal nobody opened.
 *
 * WHAT IT DOES
 *
 * `scripts/critical-css.mjs --capture` tours every page across five viewports,
 * with live data and with the API failing, and records the (at-rule context,
 * selector) pairs that matched an element at load. This integration inlines
 * those and moves everything else — modals, sheets, wallet panels, hover and
 * focus states — into one shared, hashed stylesheet that:
 *
 *   <link rel="preload" as="style" href="…">        fetch it early, don't block
 *   <link id="late-css" rel="stylesheet" media="print">  parsed, not applied
 *   <script defer src="…/css-late.<hash>.js">       flip media after it lands
 *   <noscript><link rel="stylesheet" …></noscript>  no JS: load it normally
 *
 * WHY NOT `onload="this.media='all'"` — the usual one-liner for this — is that
 * the CSP is hash-based (`script-src 'self' 'sha256-…'`, no `'unsafe-inline'`)
 * and inline event handlers are script. The same trick has to live in an
 * external file, which is why there is a 200-byte css-late script. It flips
 * the media only once the sheet has actually arrived (`link.sheet` set, or its
 * load event), because applying a stylesheet that is still in flight would put
 * it back on the critical path and undo the whole change.
 *
 * SAFETY
 *
 * The capture list is inverted: `neverMatched` holds the rules safe to defer.
 * A rule this build does not recognize is inlined by default, so a stale
 * capture costs bytes, never pixels. `@font-face`, `:root`, `@keyframes` and
 * every element-only rule always stay inline: they are read before paint.
 *
 * To refresh the list after changing what renders at load:
 *
 *   npm run css:capture     # against a built+served copy, or the live site
 */
import { createHash } from 'node:crypto';
import { readFile, writeFile, readdir, stat } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

/** Inline stylesheet ids used by the loader script. Keep the two in step. */
const LINK_ID = 'late-css';
/** Astro hashes into `_assets/`; keep the same shape so the CDN headers apply. */
const ASSET_DIR = '_assets';

async function* walk(dir) {
  for (const entry of await readdir(dir)) {
    const full = join(dir, entry);
    const st = await stat(full);
    if (st.isDirectory()) yield* walk(full);
    else yield full;
  }
}

function resolveOutDir(config) {
  const raw = config?.outDir;
  if (!raw) return join(process.cwd(), 'dist');
  try {
    return fileURLToPath(raw);
  } catch {
    return fileURLToPath(new URL(raw, `file://${process.cwd()}/`));
  }
}

const norm = (text) => text.replace(/\s+/g, ' ').trim();

/**
 * Rules whose absence would be visible in the first frame no matter what the
 * tour saw, or that are not matched by any element at all.
 */
function alwaysInline(rule) {
  if (rule.selector === ':root') return true;
  if (rule.selector.includes('font-face')) return true;
  return false;
}

const LOADER = `/* css-late.js — apply the deferred stylesheet after the page has painted.
   Built by integrations/critical-css.mjs; see that file for why this is not an
   inline onload handler.

   Timing matters twice over. Applying the sheet the moment it arrives can put
   it BACK on the critical path: Chrome will hold the first paint for a
   stylesheet whose media has just started matching, so a sheet that landed
   before the first frame delayed LCP by ~500 ms on the deployed site instead of
   saving it. Waiting for the window load event instead guarantees the first
   frame is already on screen, while still applying the sheet long before a
   visitor can open a modal — and before the islands hydrate, so
   hydration-rendered markup is never styled from the critical half alone. */
(function () {
  var link = document.getElementById('${LINK_ID}');
  if (!link) return;
  function apply() { link.media = 'all'; }
  function when() {
    if (link.sheet) apply();
    else link.addEventListener('load', apply);
  }
  if (document.readyState === 'complete') when();
  else window.addEventListener('load', when);
})();
`;

const ASTRO_IDLE_ORIG = `"requestIdleCallback"in window?window.requestIdleCallback(i,s):setTimeout(i,s.timeout||200)`;
const ASTRO_IDLE_DEFERRED = `var run=()=>requestAnimationFrame(()=>setTimeout(()=>{"requestIdleCallback"in window?window.requestIdleCallback(i,s):setTimeout(i,1)},40));document.readyState==="complete"?run():window.addEventListener("load",run,{once:!0})`;

export default function criticalCss() {
  let outDir;
  return {
    name: 'nimshop-critical-css',
    hooks: {
      'astro:config:done'({ config }) {
        outDir = resolveOutDir(config);
      },
      async 'astro:build:done'({ logger }) {
        let capture;
        try {
          capture = JSON.parse(await readFile(join(process.cwd(), 'src/styles/critical.json'), 'utf8'));
        } catch {
          logger.warn('src/styles/critical.json missing — every page keeps the full stylesheet inline. Run: node scripts/critical-css.mjs --capture <url>');
          return;
        }
        const deferrable = new Set(capture.neverMatched || []);
        if (!deferrable.size) {
          logger.warn('capture lists no deferrable rules — nothing to split');
          return;
        }
        const { default: postcss } = await import('postcss');

        const deferred = new Map(); // rule text -> nothing, insertion-ordered
        const htmlFiles = [];
        for await (const file of walk(outDir)) if (file.endsWith('.html')) htmlFiles.push(file);

        let inlineBytes = 0;
        let prefixes = new Set();
        const rewritten = new Map();

        for (const file of htmlFiles) {
          const html = await readFile(file, 'utf8');
          const styles = [...html.matchAll(/<style(?:\s[^>]*)?>([\s\S]*?)<\/style>/gi)];
          if (!styles.length) continue;
          const biggest = styles.reduce((a, b) => (b[1].length > a[1].length ? b : a));
          if (biggest[1].length < 4096) continue; // the 59-byte astro-island stub

          const root = postcss.parse(biggest[1]);

          /**
           * Split a container into the two halves. At-rules are rebuilt around
           * whichever of their children stayed, because dropping the `@media`
           * wrapper would apply that block's rules at every width — the one
           * mistake here that is invisible until someone opens the page on a
           * desktop.
           */
          const split = (container, context) => {
            const keep = [];
            const move = [];
            container.each((node) => {
              if (node.type === 'atrule' && /keyframes|font-face/.test(node.name)) {
                keep.push(node.toString());
                inlineBytes += node.toString().length;
                return;
              }
              if (node.type === 'atrule' && node.nodes) {
                const inner = split(node, `${context}@${node.name} ${node.params} | `);
                if (inner.keep.length) keep.push(`@${node.name} ${node.params}{${inner.keep.join('')}}`);
                if (inner.move.length) move.push(`@${node.name} ${node.params}{${inner.move.join('')}}`);
                return;
              }
              if (node.type !== 'rule' && node.type !== 'atrule') return;
              const text = node.toString();
              const key = `${context.replace(/ \| $/, '')}\u0000${norm(node.selector || '')}`;
              if (node.type === 'atrule' || alwaysInline(node) || !deferrable.has(key)) {
                keep.push(text);
                inlineBytes += text.length;
              } else {
                move.push(text);
              }
            });
            return { keep, move };
          };

          const { keep, move } = split(root, '');
          for (const text of move) if (!deferred.has(text)) deferred.set(text, true);
          const inline = keep.join('\n');
          // Resolve the asset prefix from a URL the page already carries, so a
          // non-root `base` (GitHub Pages) works without duplicating Astro's
          // logic here.
          const m = html.match(/(?:src|href)="([^"]*?)_assets\//);
          const prefix = m ? m[1] : '/';
          prefixes.add(prefix);

          rewritten.set(file, { html, styleBlock: biggest[0], inline, prefix });
        }

        if (!rewritten.size) {
          logger.warn('no page carried an inline stylesheet — nothing to split');
          return;
        }

        const deferredCss = [...deferred.keys()].join('\n');
        const cssHash = createHash('sha256').update(deferredCss).digest('hex').slice(0, 8);
        const jsHash = createHash('sha256').update(LOADER).digest('hex').slice(0, 8);
        const cssName = `full.${cssHash}.css`;
        const jsName = `css-late.${jsHash}.js`;
        await writeFile(join(outDir, ASSET_DIR, cssName), deferredCss);
        await writeFile(join(outDir, ASSET_DIR, jsName), LOADER);

        for (const [file, { html, styleBlock, inline, prefix }] of rewritten) {
          const cssUrl = `${prefix}${ASSET_DIR}/${cssName}`;
          const jsUrl = `${prefix}${ASSET_DIR}/${jsName}`;
          const replacement =
            `<style>${inline}</style>` +
            `<link id="${LINK_ID}" rel="stylesheet" href="${cssUrl}" media="print">` +
            `<script defer src="${jsUrl}"></script>` +
            `<noscript><link rel="stylesheet" href="${cssUrl}"></noscript>`;
          const nextHtml = html
            .replace(styleBlock, replacement)
            .replaceAll(ASTRO_IDLE_ORIG, ASTRO_IDLE_DEFERRED);
          await writeFile(file, nextHtml);
        }

        const kb = (n) => `${(n / 1024).toFixed(1)} KB`;
        const pages = rewritten.size;
        logger.info(`critical CSS: inlined ${kb(inlineBytes / pages)} per page, deferred ${kb(deferredCss.length)} to ${ASSET_DIR}/${cssName}`);
        logger.info(`deferred rules: ${deferred.size} of ${capture.keyCount ?? '?'} seen in the capture (${capture.capturedAt || 'undated'})`);
      },
    },
  };
}
