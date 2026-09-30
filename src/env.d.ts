/// <reference path="../.astro/types.d.ts" />

/**
 * Vite asset imports. `import url from '../assets/fonts/x.woff2'` returns the
 * built URL — content-hashed and base-prefixed. Base.astro uses it for the
 * font preloads so the <link> and the bundled @font-face can never disagree
 * (and so a changed font file gets a new URL instead of a stale cache entry).
 */
declare module '*.woff2' {
  const src: string;
  export default src;
}
