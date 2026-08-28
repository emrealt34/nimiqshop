/*
 * verify-in-browser.js — paste this into the console while viewing the deployed
 * nim.shop site to prove it matches the open-source build.
 *
 * This snippet is the cryptographically CLEAN verifier: it carries the
 * PUBLISHED_ROOT hash locally (off the server), so a compromised server cannot
 * fool it. After you run `generate.mjs`, copy the printed root hash into
 * PUBLISHED_ROOT below and publish this snippet (or a bookmarklet made from it).
 *
 *   1. node tools/integrity/generate.mjs        -> prints the root hash
 *   2. paste root hash into PUBLISHED_ROOT
 *   3. visit https://shop.example.com and run this in the console
 */
(async () => {
  const PUBLISHED_ROOT = '__PASTE_ROOT_HASH_HERE__'; // <-- out-of-band anchor

  const sha256Hex = async (buf) => {
    const h = await crypto.subtle.digest('SHA-256', buf);
    return [...new Uint8Array(h)].map((b) => b.toString(16).padStart(2, '0')).join('');
  };
  const fetchBytes = async (p) => {
    const r = await fetch(p, { cache: 'no-store' });
    if (!r.ok) throw new Error('HTTP ' + r.status + ' ' + p);
    return new Uint8Array(await r.arrayBuffer());
  };

  let manifest;
  try {
    manifest = JSON.parse(new TextDecoder().decode(await fetchBytes('/integrity.json')));
  } catch (e) {
    console.error('%c✗ cannot read /integrity.json', 'color:#ff5b5b', e.message);
    return;
  }

  const canon = manifest.files.map((e) => `${e.path}\n${e.sha256}\n`).join('');
  const root = await sha256Hex(new TextEncoder().encode(canon));

  let problems = 0;
  for (const { path, sha256: expected } of manifest.files) {
    let actual;
    try { actual = await sha256Hex(await fetchBytes('/' + path)); }
    catch (e) { console.log('%c✗ FETCH FAIL  ' + path, 'color:#ff5b5b', e.message); problems++; continue; }
    if (actual === expected) console.log('%c✓ OK        ' + path, 'color:#1fbf75');
    else { console.log('%c✗ MISMATCH  ' + path, 'color:#ff5b5b'); problems++; }
  }

  console.log('\nroot hash: ' + root);
  if (PUBLISHED_ROOT && PUBLISHED_ROOT !== '__PASTE_ROOT_HASH_HERE__') {
    if (root === PUBLISHED_ROOT.toLowerCase())
      console.log('%c✓ FULLY VERIFIED — live site matches the published build.', 'color:#1fbf75;font-weight:bold');
    else {
      console.log('%c✗ ROOT MISMATCH — published ' + PUBLISHED_ROOT, 'color:#ff5b5b;font-weight:bold');
      problems++;
    }
  } else {
    console.log('%c(all files match the manifest — paste the published root to anchor authenticity)',
                'color:#e2a62b');
  }
  if (problems) console.log('%c' + problems + ' problem(s).', 'color:#ff5b5b;font-weight:bold');
})();
