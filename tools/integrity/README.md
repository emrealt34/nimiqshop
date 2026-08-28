# nim.shop — build-integrity (the "hash verification")

Prove that the files running on the live server are **byte-for-byte identical**
to the open-source build, using SHA-256 hashes.

```
  PUBLISHED root hash   ← published out-of-band (git tag / GitHub release / README)
         │  authenticates
         ▼
  integrity.json        ← the manifest, served from the live site
         │  authenticates
         ▼
  every deployed file   ← fetched & hashed, compared to the manifest
```

## The bootstrap rule (read this)

`integrity.json` is itself served from the live server. A server that an
attacker fully controls could serve a tampered file **and** a tampered manifest
to match. That is why the chain is anchored by **one root hash published
out-of-band** — in a signed git tag, on the GitHub release page, or committed to
the README. Anyone verifying compares the live root to that published value.
Nothing the server can do forges it.

So:

- **Root hash published out-of-band + all files match** → cryptographically
  proven identical to the open-source build. ✓
- **All files match, but root not checked** → the manifest is internally
  consistent, but you have not proven it is *the authentic* manifest.

## Files

| File | What it does |
|------|--------------|
| `generate.mjs` | Walks a target dir, hashes every file, writes its manifest, prints the root hash. `generate.mjs frontend` / `backend` / `all`. |
| `verify.mjs` | Node CLI: fetches the live FRONTEND site and proves it matches. Use in CI. |
| `verify-backend.mjs` | Node CLI: fetches the live backend's `/api/integrity` and proves the running binary + embedded source match. |
| `build-reproducible.sh` | Builds the Go backend deterministically and prints the binary SHA-256 to publish. |
| `verify-in-browser.js` | Paste-into-console snippet with the published root baked in. Zero-trust, off-server. |
| `../../frontend/verify.html` | Optional pretty dashboard at `/verify.html` for non-technical visitors. |
| `../../frontend/integrity.json` | The frontend manifest. Deploy it alongside the site. |
| `../../backend/internal/integrity/source.integrity.json` | The backend source manifest, **embedded into the binary** at build time. |

## Workflow

### 1. Generate + publish the anchor

```bash
node tools/integrity/generate.mjs
# → writes frontend/integrity.json (29 files)
# → PUBLISH THIS ROOT HASH:
#   7e1cd89b441c6bc4e7e3c6ee1d3561616fc141b5df3418e4f158454925daa0db
```

Commit `integrity.json`. Put the root hash in a **signed git tag** and the
GitHub release:

```bash
git tag -s v2026.08.24 -m "build root: 7e1cd89b441c6bc4e7e3c6ee1d3561616fc141b5df3418e4f158454925daa0db"
git push --tags
```

The manifest is **reproducible**: the root depends only on `(path, sha256)`
pairs, not timestamps or hosts — two people building the same source get the
same root.

### 2. Deploy

Deploy `frontend/` including `integrity.json` (and optionally `verify.html`).

### 3. Anyone can verify

**Auditor / CI (Node):**
```bash
node tools/integrity/verify.mjs https://shop.example.com \
  7e1cd89b441c6bc4e7e3c6ee1d3561616fc141b5df3418e4f158454925daa0db
# exit 0 = matches the published build, exit 1 = does not
```

**Any visitor (browser console):** bake the root into
`verify-in-browser.js` (`PUBLISHED_ROOT = ...`), then paste it into the console
on the live site. Because the root travels with the script and not the server,
a compromised server cannot fool it.

**Non-technical visitor:** open `https://shop.example.com/verify.html`, then
eyeball the displayed root against the published one (or deep-link
`/verify.html?root=7e1cd8…`).

## What is excluded from the manifest

- `integrity.json` — it cannot hash itself (circular).
- `dev/` — the mock server, explicitly "NEVER deploy".
- `README.md`, `verify.html` — docs/optional. Edit the `IGNORE_*` sets in
  `generate.mjs` if your deployment actually serves them.

## Relationship to the existing vendor pinning

`SECURITY_ANALYSIS.md §5.1` already SHA-256-pins the vendored third-party
files. This toolset generalizes that idea to **the entire bundle** and adds the
out-of-band root anchor, so a deployed copy as a whole can be proven identical
to the open-source one — not just its dependencies.

## Adding it to your release checklist

After every code change:

```bash
node tools/integrity/generate.mjs     # new integrity.json + new root
git add frontend/integrity.json
git commit -m "update build integrity manifest"
git tag -s v<date> -m "build root: <new root>"   # publish the anchor
```

Re-deploy, then `verify.mjs` against the live URL in CI to assert the deployed
build equals the tagged one.

## Backend verification (reproducible binary)

The frontend scheme proves the *static files* are unmodified. The backend is a
compiled binary, so the equivalent guarantee needs one extra link: **reproducible
builds**. Two independent builds from the same source + same flags must produce
byte-identical binaries, hence an identical SHA-256.

The running backend hashes its own executable at startup and serves it, together
with the source manifest embedded at build time, at `GET /api/integrity`:

```json
{ "binary_sha256": "ff941298…", "source_manifest": { "rootHash": "84ed6e0a…", … } }
```

Trust chain for the backend:

```
published source root  ──authenticates──▶  embedded source_manifest.rootHash
published binary hash  ──authenticates──▶  binary_sha256 (reported by the server)
anyone running build-reproducible.sh from the source
                       ──links──▶  the binary is built from that source
```

### Build + publish

```bash
./tools/integrity/build-reproducible.sh
# → binary SHA-256:  ff941298259a2d5183423f845628db778c9dfad2bd1bad1c687a80f9e5e863c2
```

Publish BOTH the source root (`generate.mjs backend`) and the binary hash in a
signed git tag / release. Reproducibility is verified by building twice and
checking the hash is identical — the embedded manifest has no timestamp/host, so
the bytes are deterministic.

### Verify a deployed backend

```bash
node tools/integrity/verify-backend.mjs https://shop.example.com \
  84ed6e0a3ad454f64d610ad859b9e4f4d529ea592ae28956e0a8ce344bf1a5e4 \
  ff941298259a2d5183423f845628db778c9dfad2bd1bad1c687a80f9e5e863c2
# → FULLY VERIFIED — running backend matches the published build.
```

This proves the running server is the open-source build: the embedded source
matches the published source root, and the running binary matches the published
reproducible binary. A tampered binary, a tampered source file, or a server
running an unverified build all fail the check.
