/**
 * csp-inline-hashes.mjs — replace the `__CSP_SCRIPT_HASHES__` placeholder in
 * `script-src` with real SHA-256 hashes of the inline scripts this build
 * actually emitted, in every built page AND in dist/_headers.
 *
 * WHY
 *
 * `script-src 'unsafe-inline'` switches off the one CSP directive that matters
 * most. With it present, an attacker who manages to inject a `<script>` block
 * or an inline event handler anywhere in the page can execute it, and the CSP
 * does not object — the policy is effectively "allow all inline script". That
 * is the exact payload an XSS needs, so keeping it means the CSP is protecting
 * against exfiltration and framing but not against execution.
 *
 * The usual alternatives are nonce-based (`script-src 'nonce-…'`, one random
 * value per response) and hash-based (`script-src 'sha256-…'`). Nonces are
 * impossible here: `output: 'static'` means the HTML is built once and then
 * served unchanged by Cloudflare Pages and cached at the edge, so there is no
 * per-request moment at which to mint a nonce. Hashes are the correct fit —
 * the inline scripts in this project are fixed at build time, so their hashes
 * are too.
 *
 * WHY DERIVE THEM FROM THE BUILD OUTPUT
 *
 * A hand-maintained list of hashes is a trap. It is invisible when it is wrong:
 * edit one character of an inline script and the hash no longer matches, the
 * browser refuses to run it, and the symptom is "the theme flashes" or "scroll
 * position is lost" — nothing that fails a build or an obvious test. Deriving
 * the hashes from `dist/**\/*.html` after the build means the policy always
 * describes the bytes that were actually shipped, including any inline script
 * Astro or an integration injects that nobody remembered to add by hand.
 *
 * WHY IT ALSO PATCHES dist/_headers
 *
 * The CSP is delivered twice on purpose: as an HTTP header (Cloudflare Pages
 * reads `_headers`) and as a `<meta>` tag (fallback for hosts with no header
 * support). Both files carry the same placeholder, so both must be patched
 * with the same hash list — patching only the HTML leaves the HTTP header
 * shipping the literal token `__CSP_SCRIPT_HASHES__`, which the browser
 * reports as "invalid source … it will be ignored" and then blocks every
 * inline script, even though the page looks correctly patched.
 *
 * The build fails loudly if the placeholder survives anywhere, because that
 * means the substitution did not reach every file and the shipped site would
 * block its own theme, viewport and scroll scripts.
 *
 * WHAT IS DELIBERATELY NOT CHANGED
 *
 * `style-src 'unsafe-inline'` stays. Two reasons, one practical and one about
 * actual risk. Practically: the React components in this project use inline
 * `style={{…}}` attributes in roughly seven hundred places, and CSP treats an
 * inline style attribute exactly like an inline `<style>` block, so removing
 * the allowance would require rewriting every one of them — a large, purely
 * cosmetic refactor with a real chance of breaking layout. About risk: a style
 * cannot execute script. What inline style enables is UI redress — overlaying
 * or repositioning content — which is a genuine but much lower-severity class
 * than code execution, and which `frame-ancestors 'none'` plus the existing
 * `script-src` tightening already constrain. Removing script execution while
 * keeping style is the standard, honest trade, and it is recorded here so the
 * next reader does not mistake it for an oversight.
 */
import { readFile, writeFile, readdir, stat } from 'node:fs/promises';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  CSP_HASH_PLACEHOLDER,
  collectInlineScriptHashes,
  hasPlaceholder,
  substituteInlineScriptHashes,
  scriptSrcHashes,
} from '../scripts/csp-inline.mjs';

/** Re-exported so the constant has one definition for the whole repo. */
export { CSP_HASH_PLACEHOLDER };

async function* walk(dir) {
  for (const entry of await readdir(dir)) {
    const full = join(dir, entry);
    const st = await stat(full);
    if (st.isDirectory()) yield* walk(full);
    else yield full;
  }
}

/**
 * Resolve Astro's configured `outDir` to a real filesystem path.
 *
 * `config.outDir` is a file:// URL, and the obvious `new URL(outDir).pathname`
 * is wrong on Windows: it yields `/C:/Users/…/dist`, which only survives
 * because Node happens to normalise it. `fileURLToPath` is the supported
 * conversion and produces `C:\Users\…\dist` directly, so a drive-letter path
 * cannot silently change which directory gets scanned.
 */
function resolveOutDir(config) {
  const raw = config?.outDir;
  if (!raw) return join(process.cwd(), 'dist');
  try {
    return fileURLToPath(raw);
  } catch {
    return fileURLToPath(new URL(raw, `file://${process.cwd()}/`));
  }
}

export default function cspInlineHashes() {
  let outDir;
  return {
    name: 'nimshop-csp-inline-hashes',
    hooks: {
      'astro:config:done'({ config }) {
        outDir = resolveOutDir(config);
      },
      async 'astro:build:done'({ logger }) {
        const htmlFiles = [];
        for await (const file of walk(outDir)) {
          if (file.endsWith('.html')) htmlFiles.push(file);
        }
        if (htmlFiles.length === 0) {
          throw new Error(`csp-inline-hashes: no HTML found under ${outDir}`);
        }

        // ---- 1. Collect the hashes from what was actually emitted ----------
        const hashes = new Set();
        let pagesWithPlaceholder = 0;
        for (const file of htmlFiles) {
          const html = await readFile(file, 'utf8');
          if (hasPlaceholder(html)) pagesWithPlaceholder++;
          for (const h of collectInlineScriptHashes(html)) hashes.add(h);
        }

        if (pagesWithPlaceholder === 0) {
          // The placeholder is emitted by src/layouts/Base.astro. If no page
          // contains it, somebody removed it, and the shipped CSP would list
          // 'self' plus hub.nimiq.com and nothing else — blocking every inline
          // script on every page while the build reported success. Fail here
          // instead.
          throw new Error(
            'csp-inline-hashes: no built page contained the ' + CSP_HASH_PLACEHOLDER +
            ' placeholder. src/layouts/Base.astro must emit it in script-src, or this ' +
            'integration must be removed together with the inline scripts it covers.'
          );
        }

        if (hashes.size === 0) {
          // Either every inline script vanished or the regex stopped matching.
          // Both would ship a policy that blocks nothing or blocks everything,
          // so refuse to guess.
          throw new Error(
            'csp-inline-hashes: found no inline <script> to hash. ' +
            'If that is genuinely expected, remove this integration; otherwise the ' +
            'extraction regex has stopped matching and the emitted CSP would be wrong.'
          );
        }

        const hashList = [...hashes].sort();

        // ---- 2. Patch every built page ------------------------------------
        let patchedPages = 0;
        for (const file of htmlFiles) {
          const html = await readFile(file, 'utf8');
          if (!hasPlaceholder(html)) continue;
          await writeFile(file, substituteInlineScriptHashes(html, hashList), 'utf8');
          patchedPages++;
        }

        // ---- 3. Patch dist/_headers with the same list --------------------
        //
        // The header is what Cloudflare Pages and nginx send; the meta tag is
        // the fallback for hosts with no header support. Two different policies
        // would be worse than one weak one, because whichever is stricter wins
        // per directive and the result is impossible to reason about — so the
        // same hash list is written to both.
        //
        // Only the placeholder is substituted here, never the whole line: the
        // rest of the policy (including `frame-ancestors`, which has no effect
        // in a <meta> tag and is therefore absent from the HTML policy) is
        // authored by hand in public/_headers and must survive untouched.
        const headersPath = join(outDir, '_headers');
        let headersPatched = false;
        try {
          const headers = await readFile(headersPath, 'utf8');
          if (hasPlaceholder(headers)) {
            const next = substituteInlineScriptHashes(headers, hashList);
            if (!/Content-Security-Policy:/im.test(next)) {
              logger.warn('csp-inline-hashes: no Content-Security-Policy line in _headers; left unchanged');
            } else {
              await writeFile(headersPath, next, 'utf8');
              headersPatched = true;
            }
          } else if (!scriptSrcHashes(headers).length) {
            logger.warn(
              'csp-inline-hashes: _headers has no ' + CSP_HASH_PLACEHOLDER +
              ' and no sha256 sources; the HTTP-header CSP and the <meta> CSP may drift apart'
            );
          }
        } catch {
          logger.warn('csp-inline-hashes: _headers not present, skipped mirroring');
        }

        // ---- 4. Verify: the two deliveries must agree ---------------------
        //
        // A page whose hashes differ from the header's hashes is the worst
        // outcome — one delivery blocks what the other allows, and the symptom
        // depends on the host. Compare them instead of assuming.
        try {
          const [sampleHtml, headers] = await Promise.all([
            readFile(htmlFiles[0], 'utf8'),
            readFile(headersPath, 'utf8'),
          ]);
          const meta = sampleHtml.match(/<meta[^>]*http-equiv=["']?Content-Security-Policy["']?[^>]*>/i);
          const metaPolicy = meta
            ? (meta[0].match(/\bcontent\s*=\s*"([^"]*)"|\bcontent\s*=\s*'([^']*)'/i) ?? [])[1] ?? ''
            : '';
          const headerPolicy = (headers.match(/^[\t ]*Content-Security-Policy:[\t ]*(.*)$/im) ?? [])[1] ?? '';
          if (metaPolicy && headerPolicy) {
            const a = scriptSrcHashes(metaPolicy).join(' ');
            const b = scriptSrcHashes(headerPolicy).join(' ');
            if (a !== b) {
              logger.warn(
                'csp-inline-hashes: script-src hashes differ between the <meta> CSP and _headers; ' +
                'whichever the host enforces will win — rebuild and re-check both files'
              );
            }
          }
        } catch { /* _headers optional; nothing to compare against */ }

        // ---- 5. Belt and braces: nothing may ship with the placeholder ----
        for await (const file of walk(outDir)) {
          if (!file.endsWith('.html') && !file.endsWith('_headers') && !file.endsWith('.js')) continue;
          const body = await readFile(file, 'utf8');
          if (hasPlaceholder(body)) {
            throw new Error(
              `csp-inline-hashes: placeholder survived in ${relative(outDir, file)}; ` +
              'that file would ship a CSP with a literal __CSP_SCRIPT_HASHES__ token and block every inline script'
            );
          }
        }

        logger.info(
          `csp-inline-hashes: ${hashList.length} inline script hash(es) applied to ${patchedPages} page(s)` +
          (headersPatched ? '; _headers mirrored' : '')
        );
      },
    },
  };
}
