/**
 * vite.config.js — root Vite config.
 *
 * THE host-allowlist fix: every Cloudflare quick tunnel has a NEW random
 * `.trycloudflare.com` host each run, and Vite's dev/preview server rejects
 * any Host header that isn't localhost. Setting `allowedHosts: true` tells
 * Vite to accept ANY Host header, so the preview proxy / cloudflared tunnel
 * always works — no per-host config needed.
 */
import { defineConfig } from 'vite';

export default defineConfig({
  server: {
    host: true,
    allowedHosts: true,
  },
  preview: {
    host: true,
    allowedHosts: true,
  },
});
