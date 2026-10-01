import { defineConfig } from 'astro/config';
import react from '@astrojs/react';
import { proxyApi, proxyOptionsFromEnv } from './scripts/proxy.mjs';
import { servedConfigSource } from './scripts/public-url.mjs';
import cspInlineHashes from './integrations/csp-inline-hashes.mjs';
import dictPreloadMap from './integrations/dict-preload-map.mjs';
import criticalCss from './integrations/critical-css.mjs';

// Static output (SSG). No server. The only "server" concerns are the same
// ones the original static build had: the backend API lives elsewhere and is
// configured via public/config.js (API_BASE), so this site is pure static.
export default defineConfig({
  output: 'static',
  base: process.env.PUBLIC_BASE || '/',
  // react() renders the app; dictPreloadMap() and cspInlineHashes() run after
  // the build: the first replaces the __DICT_PRELOAD_MAP__ placeholder in
  // every page with the real {lang: dictionary-chunk-URL} map (so the
  // pre-paint script can modulepreload the visitor's language), the second
  // replaces the script-src placeholder with real SHA-256 hashes of the
  // inline scripts that were actually emitted. ORDER MATTERS: the map lands
  // INSIDE an inline script, so it must be substituted before that script's
  // bytes are hashed — cspInlineHashes() must stay last. Both must be
  // present, or their placeholders ship as-is.
  integrations: [react(), criticalCss(), dictPreloadMap(), cspInlineHashes()],
  site: 'https://shop.nimiqbase.com',
  // The floating Astro dev-toolbar badge overlays checkout buttons on phone
  // widths (the shop is used inside Nimiq Pay on mobile). It is a dev-only
  // badge; the shop's own devtools panel is unrelated and unaffected.
  devToolbar: { enabled: false },
  server: {
    // Accept the sandbox proxy / preview host so the live preview loads.
    allowedHosts: true,
  },
  // Keep URLs clean — the original used .html extension for the multi-page
  // SPA; Astro's file-based routing already produces /orders, /order, etc.
  // Query params (id, country) are read client-side in React, exactly as the
  // original read location.search.
  build: {
    // 'always' — the one stylesheet is inlined into every page's <head>.
    // Measured on the deployed site (mobile, 4G/4x CPU): the external CSS cost
    // a full round trip plus 34 KB of the critical path, and Lighthouse priced
    // the render block at ~490 ms. Inlining trades ~24 KB of brotli on each
    // HTML response (the HTML is not cached across pages) for removing the
    // request entirely: no blocking link, one fewer chain, first paint waits
    // only for the HTML. Ship-side this is ~10 KB compressed per page because
    // the CSS compresses far better inside the document than standalone.
    inlineStylesheets: 'always',
    assets: '_assets',
  },
  vite: {
    plugins: [{
      name: 'nimshop-private-api-proxy',
      configureServer(server) {
        const options = proxyOptionsFromEnv();
        server.middlewares.use((req,res,next) => {
          if ((req.url || '').startsWith('/api/')) return proxyApi(req,res,options);
          if ((req.url || '').split('?')[0] === '/_health') { res.setHeader('Content-Type','application/json'); return res.end('{"ok":true}'); }
          // Same rule as scripts/static-server.mjs: the launcher-generated
          // config (public URL → API_BASE '/api') wins over public/config.js,
          // so `astro dev` behind a tunnel never calls the production API.
          if ((req.url || '').split('?')[0] === '/config.js') {
            const generated = servedConfigSource();
            if (generated) {
              res.setHeader('Content-Type','text/javascript; charset=utf-8');
              res.setHeader('Cache-Control','no-store, no-cache, must-revalidate');
              return res.end(generated);
            }
          }
          next();
        });
      },
    }],
    server: {
      // Accept ANY host in the dev server (sandbox proxy, cloudflare tunnel,
      // etc.) instead of Vite's default localhost-only allowlist.
      allowedHosts: true,
    },
    preview: {
      allowedHosts: true,
    },
    // NOTE: do NOT exclude 'react' from optimizeDeps — doing so makes the
    // dev server throw "The entry point 'react' cannot be marked as external"
    // on every HMR update. React is a normal dependency and must be optimized.
  },
});
