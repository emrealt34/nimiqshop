/**
 * dict-preload-map.mjs — replace the `__DICT_PRELOAD_MAP__` placeholder in
 * every built page with a JSON map {lang: dictionary-chunk-URL}, so the
 * pre-paint language bootstrap in src/layouts/Base.astro can attach
 * <link rel="modulepreload"> for the visitor's language while the HTML is
 * still parsing.
 *
 * WHY
 *
 * The shop is a static build: the server-rendered HTML is always English,
 * and the five non-English dictionaries travel as lazy chunks that React
 * only imports AFTER hydration. Without a preload the visitor's language
 * therefore lands one full round trip late — the page paints English,
 * hydrates, fetches the dictionary, and then visibly flips. Starting the
 * fetch from the pre-paint script means the chunk is usually in the module
 * cache before the entry bundle even finishes booting, which collapses most
 * of that flip.
 *
 * WHY DERIVE THE URLS FROM THE BUILD OUTPUT
 *
 * The chunk filenames are content-hashed (`_assets/tr-BsD2fa41.js`), so no
 * source file can know them at authoring time. Importing the locale modules
 * with `?url` to "get the URL" is a trap: Vite emits that as a SECOND chunk,
 * and preloading a file the runtime never imports would download every
 * dictionary twice. Scanning the real dist output for a string that only
 * exists in each dictionary identifies the exact chunk the dynamic
 * `import('./locales/<lang>')` will request — the same "describe the bytes
 * you actually shipped" discipline csp-inline-hashes.mjs follows.
 *
 * ORDERING
 *
 * This runs in `astro:build:done` and MUST be registered before
 * cspInlineHashes(): it edits the bytes of an inline <script>, and the CSP
 * integration hashes those scripts afterwards. Hashing first would ship a
 * policy that no longer matches, and the browser would block the language
 * bootstrap entirely.
 *
 * FAILURE POLICY
 *
 * Missing marker, ambiguous marker or a surviving placeholder all throw.
 * A silent no-op here would restore exactly the bug this integration fixes
 * (English flash on every non-English load) with nothing in the build log to
 * point at it.
 */
import { readFile, writeFile, readdir, stat } from 'node:fs/promises';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

export const DICT_MAP_PLACEHOLDER = '__DICT_PRELOAD_MAP__';

/** As it actually appears in the built HTML: Base.astro passes the token
 *  through `define:vars`, which serialises it as a JS string literal —
 *  `"__DICT_PRELOAD_MAP__"` with the quotes. Substituting the quoted form
 *  with a raw JSON object literal turns
 *    const dictPreloadMap = "__DICT_PRELOAD_MAP__";
 *  into
 *    const dictPreloadMap = {"tr":"/_assets/tr-x.js",…};
 *  which is exactly the object the bootstrap script expects. */
const QUOTED_PLACEHOLDER = `"${DICT_MAP_PLACEHOLDER}"`;

/** Non-English languages whose dictionaries ship as their own chunk
 *  (keep in sync with LOADERS in src/i18n/index.tsx). */
const CHUNKED_LANGS = ['es', 'de', 'fr', 'pt', 'tr'];

/** Keys tried, in order, when extracting a marker string from a locale file. */
const MARKER_KEYS = ['heroTitle1', 'heroLede', 'defaultDescription'];

const HERE = dirname(fileURLToPath(import.meta.url));

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

/**
 * A substring of one locale's translations that (a) is long enough to be
 * unique and (b) contains no quote or backslash characters — the minifier
 * re-escapes those inside string literals, so a marker containing them could
 * fail to match the built bytes even though the translation is in there.
 */
async function markerFor(lang) {
  const src = await readFile(join(HERE, '..', 'src', 'i18n', 'locales', `${lang}.ts`), 'utf8');
  for (const key of MARKER_KEYS) {
    const m = src.match(new RegExp(`${key}:\\s*'((?:[^'\\\\]|\\\\.)*)'`));
    if (!m) continue;
    const value = m[1].replace(/\\'/g, "'").replace(/\\\\/g, '\\');
    const runs = value.split(/['"\\]/).map((s) => s.trim()).filter((s) => s.length >= 12);
    if (runs.length) return runs.sort((a, b) => b.length - a.length)[0];
  }
  throw new Error(
    `dict-preload-map: no usable marker string in src/i18n/locales/${lang}.ts ` +
    `(tried ${MARKER_KEYS.join(', ')}); the preload map cannot be built.`,
  );
}

export default function dictPreloadMap() {
  let outDir;
  let assetsDir = '_assets';
  let base = '/';
  return {
    name: 'nimshop-dict-preload-map',
    hooks: {
      'astro:config:done'({ config }) {
        outDir = resolveOutDir(config);
        assetsDir = config?.build?.assets || '_assets';
        base = config?.base || '/';
      },
      async 'astro:build:done'({ logger }) {
        // ---- 1. Find the real chunk for every lazy dictionary -------------
        const jsDir = join(outDir, assetsDir);
        let jsFiles = [];
        try {
          for await (const file of walk(jsDir)) {
            if (file.endsWith('.js')) jsFiles.push(file);
          }
        } catch {
          throw new Error(`dict-preload-map: asset directory ${jsDir} not found — did the build run?`);
        }
        const contents = new Map();
        for (const file of jsFiles) contents.set(file, await readFile(file, 'utf8'));

        const map = {};
        for (const lang of CHUNKED_LANGS) {
          const marker = await markerFor(lang);
          const hits = jsFiles.filter((f) => contents.get(f).includes(marker));
          if (hits.length === 0) {
            throw new Error(
              `dict-preload-map: no built chunk under ${assetsDir}/ contains the ${lang} ` +
              `dictionary marker "${marker.slice(0, 40)}…". Either the lazy import in ` +
              'src/i18n/index.tsx changed shape, or the dictionary stopped shipping as its ' +
              'own chunk — in which case this integration (and the placeholder in ' +
              'src/layouts/Base.astro) must be removed together.',
            );
          }
          let chosen = hits[0];
          if (hits.length > 1) {
            // Prefer the chunk Vite named after the module (`tr-<hash>.js`);
            // anything else is genuinely ambiguous.
            const named = hits.filter((f) => f.endsWith('.js') && dirname(f) === jsDir && /(^|\/)[a-z]{2}-[^/]+\.js$/.test(f) && f.slice(jsDir.length + 1).startsWith(`${lang}-`));
            if (named.length !== 1) {
              throw new Error(
                `dict-preload-map: marker for ${lang} matched ${hits.length} chunks ` +
                `(${hits.map((f) => f.slice(jsDir.length + 1)).join(', ')}); refusing to guess.`,
              );
            }
            chosen = named[0];
          }
          const file = chosen.slice(jsDir.length + 1);
          map[lang] = `${base.replace(/\/+$/, '')}/${assetsDir}/${file}`;
        }
        logger.info(`dict-preload-map: ${Object.keys(map).map((l) => `${l}→${map[l]}`).join(' ')}`);

        // ---- 2. Patch every built page ------------------------------------
        // The JSON replaces the quoted placeholder wholesale, so the result
        // is a valid JS object literal inside `const dictPreloadMap = …;`.
        // JSON only uses double quotes, and the token it replaces was itself
        // a double-quoted string, so no escaping games are needed.
        const json = JSON.stringify(map);
        let patched = 0;
        for await (const file of walk(outDir)) {
          if (!file.endsWith('.html')) continue;
          const html = await readFile(file, 'utf8');
          if (!html.includes(QUOTED_PLACEHOLDER)) continue;
          await writeFile(file, html.split(QUOTED_PLACEHOLDER).join(json), 'utf8');
          patched++;
        }
        if (patched === 0) {
          throw new Error(
            `dict-preload-map: no built page contained the ${QUOTED_PLACEHOLDER} placeholder. ` +
            'src/layouts/Base.astro must emit it (via define:vars) in the pre-paint language ' +
            'script, or this integration must be removed together with that script.',
          );
        }

        // ---- 3. Verify nothing was left unsubstituted ----------------------
        for await (const file of walk(outDir)) {
          if (!file.endsWith('.html')) continue;
          const html = await readFile(file, 'utf8');
          if (html.includes(DICT_MAP_PLACEHOLDER)) {
            throw new Error(`dict-preload-map: placeholder survived in ${file}`);
          }
        }
        logger.info(`dict-preload-map: patched ${patched} page(s)`);
      },
    },
  };
}
