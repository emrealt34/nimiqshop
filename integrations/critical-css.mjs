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
/** Interactive chrome that must never wait for the deferred sheet. Overlays,
 *  sheets, menus and the bars only appear on interaction — usually after the
 *  flip, but a flip that races (slow network, blocked request) left them
 *  unstyled, and the deferred sheet's own rule order once inverted the
 *  `.overlay` cascade (base rule after its @media override → the desktop
 *  dialog stuck to the bottom edge). A few KB in the first paint buys
 *  "chrome is always correct, whenever it opens". */
const ALWAYS_INLINE_SELECTORS = [
  '.overlay', '.sheet', '.sheet-backdrop', '.toast', '.toasts', '.toast-stack',
  '.tabbar', '.topbar', '.acct-menu', '.country-pop',
  // Card geometry. The thumb's aspect-ratio + object-fit rules used to live
  // in the deferred sheet, so the first paint drew every logo at its
  // intrinsic (square, tall) size and the whole grid visibly shrank when
  // css-late flipped ~300 ms in ("the list comes big, then fixes itself").
  // A few KB of card chrome in the first frame buys a grid whose card boxes
  // are final from the moment they exist — before any logo even loads.
  '.thumb', '.product-img', '.product-card',
  // The products grid's column rules exist TWICE in the sheet (readable +
  // minified section). When one copy stayed inline and its duplicate went
  // deferred, the deferred copy landed later in the document and out-ranked
  // the inline @media override after the flip: the grid re-columned 5→6 and
  // every card visibly shrank ~350 ms in. Grid rules are two lines; keep
  // them all in the first frame.
  '.grid',
];
function alwaysInline(rule) {
  if (rule.selector === ':root') return true;
  if (rule.selector.includes('font-face')) return true;
  // Theme rules ship inline, ALL of them. The capture tour walks the pages in
  // the default (light) theme, so every `[data-theme=dark] …` rule lands in
  // neverMatched and gets deferred — and the deferred sheet only flips on
  // AFTER first paint. The pre-paint theme script sets data-theme=dark on
  // <html>, but with no dark variables in the critical CSS the first frame
  // resolves every var() to its light value: dark-theme users got a white
  // page that snapped to black ~1s in. ~6 KB of variables and overrides,
  // inlined on every page, is the cheap end of that trade.
  if ((rule.selector || '').includes('data-theme')) return true;
  return ALWAYS_INLINE_SELECTORS.some((sel) => (rule.selector || '').includes(sel));
}

const buildLoader = (islandScriptBody) => `/* css-late.js — apply the deferred stylesheet and hydrate islands after first paint. */
(function () {
  var ready = false;
  var queue = [];
  function runQueue() {
    while (queue.length) {
      var fn = queue.shift();
      try { fn(); } catch (e) {}
    }
  }
  function flush() {
    if (ready) return;
    ready = true;
    var link = document.getElementById('${LINK_ID}');
    if (link) {
      var dh = link.getAttribute('data-href');
      if (dh && !link.getAttribute('href')) link.setAttribute('href', dh);
      var done = false;
      function finish() {
        if (done) return;
        done = true;
        link.media = 'all';
        runQueue();
      }
      if (link.sheet) finish();
      else {
        link.addEventListener('load', finish, { once: true });
        link.addEventListener('error', finish, { once: true });
        setTimeout(finish, 150);
      }
    } else {
      runQueue();
    }
  }
  function schedule() {
    setTimeout(flush, 180);
  }
  if (document.readyState === 'complete') schedule();
  else window.addEventListener('load', schedule, { once: true });

  var l = function (n) {
    var i = async function () { await (await n())(); };
    if (ready) setTimeout(i, 1);
    else queue.push(i);
  };
  (self.Astro || (self.Astro = {})).idle = l;
  window.dispatchEvent(new Event('astro:idle'));
})();
${islandScriptBody}
`;

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
        let islandScriptBody = '';
        let repCss = '';

        for (const file of htmlFiles) {
          const html = await readFile(file, 'utf8');
          for (const m of html.matchAll(/<script>([\s\S]*?)<\/script>/g)) {
            if (m[1].includes('customElements.define("astro-island"')) {
              islandScriptBody = m[1];
              break;
            }
          }
          const styles = [...html.matchAll(/<style(?:\s[^>]*)?>([\s\S]*?)<\/style>/gi)];
          if (!styles.length) continue;
          const biggest = styles.reduce((a, b) => (b[1].length > a[1].length ? b : a));
          if (biggest[1].length < 4096) continue; // the 59-byte astro-island stub

          const root = postcss.parse(biggest[1]);
          const htmlNoStyle = html.replace(biggest[0], '');
          const pageClasses = new Set(['in-nimiq-pay', 'active', 'open', 'dark', 'light', 'tabbar', 'tab-ico', 'tab-lbl', 'nav-badge']);
          for (const cm of htmlNoStyle.matchAll(/class="([^"]*)"/g)) {
            for (const c of cm[1].split(/\s+/)) if (c) pageClasses.add(c);
          }
          const pageIds = new Set(['tabbar-root']);
          for (const im of htmlNoStyle.matchAll(/id="([^"]*)"/g)) {
            if (im[1]) pageIds.add(im[1]);
          }
          const selectorMatchesPage = (sel) => {
            if (!sel) return true;
            for (const part of sel.split(',')) {
              const clsMatches = [...part.matchAll(/\.([a-zA-Z0-9_-]+)/g)].map((m) => m[1]);
              const idMatches = [...part.matchAll(/#([a-zA-Z0-9_-]+)/g)].map((m) => m[1]);
              if (clsMatches.every((c) => pageClasses.has(c)) && idMatches.every((i) => pageIds.has(i))) {
                return true;
              }
            }
            return false;
          };
          let seenRootVars = false;

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
                // EVERY @font-face stays inline — including the body webfont
                // (nunito-var) that used to be deferred here. A face the
                // browser has not seen cannot be downloaded, and with
                // font-display:optional the ~100 ms use-it-or-lose-it window
                // starts at first layout: registering the face only when the
                // deferred sheet flips meant the real font could never win
                // the window and every load visibly swapped fallback→webfont
                // a second in. The faces are inline, the woff2 files are
                // preloaded in <head>, and optional guarantees the swap is
                // either instant (cached/fast) or simply skipped for that
                // pageview — text never changes typeface after paint.
                const text = node.toString();
                keep.push(text);
                inlineBytes += text.length;
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
              if (node.type === 'rule' && node.selector === ':root' && !context && text.includes('--bg:')) {
                if (seenRootVars) return;
                seenRootVars = true;
              }
              const key = `${context.replace(/ \| $/, '')}\u0000${norm(node.selector || '')}`;
              const isPageCritical = /\.(?:cb-|pt-)/.test(node.selector || '');
              if (node.type === 'atrule' || alwaysInline(node) || ((!deferrable.has(key) || isPageCritical) && selectorMatchesPage(node.selector))) {
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
          // Remember one representative copy of the source sheet: the deferred
          // file is re-emitted in THIS order at the end (see deferredCss),
          // because deduping across pages in walk order once inverted a
          // same-specificity cascade (a @media override landed before its base
          // rule and silently lost).
          if (!repCss || /(^|\/)index\.html$/.test(file)) repCss = biggest[1];
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

        const loaderCode = buildLoader(islandScriptBody);
        // Re-emit the deferred rules in SOURCE order. The set was deduped
        // across pages in filesystem walk order, which is unrelated to the
        // cascade: a base rule and its same-specificity @media override can
        // end up inverted in the deferred sheet, and the override then loses
        // forever (measured once for real: the desktop overlay centering).
        // Every page carries the same full sheet, so positions in one
        // representative copy give the canonical order; rules absent from it
        // keep their first-seen order at the end (stable sort).
        const deferredCss = [...deferred.keys()]
          .sort((a, b) => {
            const ia = repCss.indexOf(a);
            const ib = repCss.indexOf(b);
            return (ia === -1 ? Number.MAX_SAFE_INTEGER : ia) - (ib === -1 ? Number.MAX_SAFE_INTEGER : ib);
          })
          .join('\n');
        const cssHash = createHash('sha256').update(deferredCss).digest('hex').slice(0, 8);
        const jsHash = createHash('sha256').update(loaderCode).digest('hex').slice(0, 8);
        const cssName = `full.${cssHash}.css`;
        const jsName = `css-late.${jsHash}.js`;
        await writeFile(join(outDir, ASSET_DIR, cssName), deferredCss);
        await writeFile(join(outDir, ASSET_DIR, jsName), loaderCode);

        for (const [file, { html, styleBlock, inline, prefix }] of rewritten) {
          const cssUrl = `${prefix}${ASSET_DIR}/${cssName}`;
          const jsUrl = `${prefix}${ASSET_DIR}/${jsName}`;
          const replacement =
            `<style>${inline}</style>` +
            `<link id="${LINK_ID}" rel="stylesheet" media="print" data-href="${cssUrl}">` +
            `<script defer fetchpriority="low" src="${jsUrl}"></script>` +
            `<noscript><link rel="stylesheet" href="${cssUrl}"></noscript>`;
          const nextHtml = html
            .replace(styleBlock, replacement)
            .replace(/<script>([\s\S]*?)<\/script>/g, (full, body) => {
              if (body.includes('.idle=') && body.includes('astro:idle')) return '';
              if (body.includes('customElements.define("astro-island"')) return '';
              return full;
            })
            .replace(/<\/body>/i, '<template hidden></body>');
          await writeFile(file, nextHtml);
        }

        try {
          const cfgPath = join(outDir, 'config.js');
          const rawCfg = await readFile(cfgPath, 'utf8');
          const minCfg = rawCfg
            .replace(/\/\*[\s\S]*?\*\//g, '')
            .replace(/^\s*\/\/.*$/gm, '')
            .replace(/\n\s*\n+/g, '\n')
            .trim() + '\n';
          await writeFile(cfgPath, minCfg);
        } catch { /* optional config.js minification */ }

        const kb = (n) => `${(n / 1024).toFixed(1)} KB`;
        const pages = rewritten.size;
        logger.info(`critical CSS: inlined ${kb(inlineBytes / pages)} per page, deferred ${kb(deferredCss.length)} to ${ASSET_DIR}/${cssName}`);
        logger.info(`deferred rules: ${deferred.size} of ${capture.keyCount ?? '?'} seen in the capture (${capture.capturedAt || 'undated'})`);
      },
    },
  };
}
