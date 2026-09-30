// csp-inline.mjs — the single source of truth for how `script-src` inline
// hashes are computed and substituted.
//
// WHY THIS FILE EXISTS
//
// Three different parts of the build need the exact same operation:
//
//   1. integrations/csp-inline-hashes.mjs — substitutes the hashes into
//      dist/**/*.html and dist/_headers right after `astro build`.
//   2. scripts/build-all.mjs              — must RECOGNISE a dist/ that was
//      built without that substitution, so it can rebuild it instead of
//      serving it as "up to date".
//   3. scripts/static-server.mjs          — is the last line of defence: if a
//      stale dist/ still reaches the preview server, it repairs the page on
//      the fly rather than shipping a policy that blocks every inline script.
//
// The hash is sha256 over the EXACT bytes between <script> and </script>. A
// second, subtly different implementation (trimming the body, using a
// different regex, treating `type="application/ld+json"` as executable) would
// produce a different base64 string, the browser would reject the script, and
// the failure would look like "the theme stopped working" rather than "two
// hash functions disagree". One implementation removes that whole class of
// bug — and it is why this file is imported rather than copied.
//
// The four hashes this produces for the current Base.astro are stable: they
// are the theme bootstrap, the Nimiq-Pay viewport fix, the scroll-stability
// handler and the JSON/config guard. Change any of them in the layout and
// these values change with them, automatically.

import { createHash } from 'node:crypto';

/** Substituted in src/layouts/Base.astro and public/_headers after the build. */
export const CSP_HASH_PLACEHOLDER = '__CSP_SCRIPT_HASHES__';

/** Script `type` values that are data, not code. CSP does not gate them and
 *  hashing them would add noise to the policy for no benefit. */
const NON_EXECUTABLE_TYPES = new Set([
  'application/ld+json',
  'application/json',
  'text/template',
  'text/plain',
]);

const SCRIPT_RE = /<script\b([^>]*)>([\s\S]*?)<\/script[^>]*>/gi;

/**
 * True when an inline <script …> tag is real code that CSP will gate.
 * External scripts (`src=`) are covered by 'self' and must not be hashed.
 */
export function isExecutableInlineScript(attrs) {
  if (/\bsrc\s*=/i.test(attrs)) return false;
  const m = attrs.match(/\btype\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))/i);
  if (m) {
    const type = (m[2] ?? m[3] ?? m[4] ?? '').trim().toLowerCase();
    if (type && NON_EXECUTABLE_TYPES.has(type)) return false;
    // A module or classic script type is still executable.
  }
  return true;
}

/** CSP hashes the exact bytes between the tags, so the body is never trimmed. */
export function hashInlineScript(body) {
  return "'sha256-" + createHash('sha256').update(body, 'utf8').digest('base64') + "'";
}

/**
 * Every distinct sha256 hash of every executable inline script in `html`,
 * sorted so the output is byte-stable between builds (a stable policy means
 * a redeploy does not churn the _headers file for no reason).
 */
export function collectInlineScriptHashes(html) {
  const hashes = new Set();
  SCRIPT_RE.lastIndex = 0;
  let m;
  while ((m = SCRIPT_RE.exec(html)) !== null) {
    if (!isExecutableInlineScript(m[1])) continue;
    hashes.add(hashInlineScript(m[2]));
  }
  return [...hashes].sort();
}

export function hasPlaceholder(text) {
  return text.includes(CSP_HASH_PLACEHOLDER);
}

/**
 * Replace the placeholder in a policy/HTML document with `hashes`.
 * Returns the input untouched when there is no placeholder, so callers can
 * run this over every file without a pre-check.
 */
export function substituteInlineScriptHashes(text, hashes) {
  if (!hasPlaceholder(text)) return text;
  return text.split(CSP_HASH_PLACEHOLDER).join(hashes.join(' '));
}

/** Read the script-src source list back out of a policy, for verification. */
export function readScriptSrc(policy) {
  const m = policy.match(/\bscript-src\s+([^;]*)/i);
  return m ? m[1].trim() : '';
}

export function scriptSrcHashes(policy) {
  return (readScriptSrc(policy).match(/'sha256-[A-Za-z0-9+/=]+'/g) ?? []).sort();
}
