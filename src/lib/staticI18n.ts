/**
 * staticI18n.ts — translates the handful of strings that live in the STATIC
 * Astro shell instead of inside a React island: the skip link, the footer
 * credit, the GitHub link label and the whole static 404 page.
 *
 * Those pages ship as plain HTML (there is no React tree on /404), so they
 * cannot call useT(). Instead the markup opts in with data attributes and one
 * bundled script localises them:
 *
 *   data-i18n="ns.key"                  → element textContent
 *   data-i18n-attr="aria-label:ns.key"  → one or more attributes ("attr:key" pairs)
 *   <html data-title-key="pageTitle.x"> → document.title
 *
 * `{{site}}` is always supplied, so keys can embed the shop hostname.
 *
 * Re-applied whenever the language changes: the React language switcher calls
 * applyLang(), which writes <html data-lang>. We watch that attribute instead
 * of only subscribing to onLangChange(), because this script and the React
 * island are separate bundles — a MutationObserver keeps the static shell in
 * sync even if the two copies of the i18n module are not the same instance.
 * Two tabs stay in sync for the same reason (applyLang writes the attribute).
 */
import { getLang, loadDict, onDictSwap, onLangChange, t } from '../i18n';
import { siteName } from './config';

type Vars = Record<string, string>;

function vars(): Vars {
  return { site: siteName() };
}

/** Translate one key and apply it, never leaving a raw `ns.key` on screen. */
function applyText(el: Element) {
  const key = el.getAttribute('data-i18n');
  if (!key) return;
  const value = t(key, vars());
  if (value && value !== key) el.textContent = value;
}

function applyAttrs(el: Element) {
  const spec = el.getAttribute('data-i18n-attr');
  if (!spec) return;
  for (const pair of spec.split(/\s+/)) {
    const at = pair.indexOf(':');
    if (at < 1) continue;
    const attr = pair.slice(0, at);
    const key = pair.slice(at + 1);
    const value = t(key, vars());
    if (value && value !== key) el.setAttribute(attr, value);
  }
}

/** Localise every opted-in node under `root` (defaults to the document). */
export function applyStaticI18n(root: ParentNode = document) {
  root.querySelectorAll('[data-i18n]').forEach(applyText);
  root.querySelectorAll('[data-i18n-attr]').forEach(applyAttrs);
  const titleKey = document.documentElement.getAttribute('data-title-key');
  if (titleKey) document.title = t(titleKey, vars());
}

let started = false;

/** Called by the layout once, on the client. Safe to call repeatedly. */
export function initStaticI18n() {
  if (started || typeof document === 'undefined') return;
  started = true;
  applyStaticI18n();
  // A non-English dictionary is its own chunk: translate again the moment it
  // exists, even if this bundle ended up with its own copy of the i18n module
  // (the copy's listener would never fire for the app's load).
  void loadDict(getLang()).then(() => applyStaticI18n());
  onLangChange(() => applyStaticI18n());
  onDictSwap(() => applyStaticI18n());
  // The static shell is translated as soon as the page runs, but a non-English
  // dictionary now arrives as its own chunk — the first pass therefore renders
  // English. `data-i18n-ready` is written by the i18n module exactly when a
  // dictionary is applied, so watching it re-translates the shell the moment
  // the strings exist (data-lang alone is written before paint, so it can
  // already hold the final value by the time this observer attaches).
  try {
    const snapshot = () => [
      document.documentElement.getAttribute('data-lang'),
      document.documentElement.getAttribute('data-i18n-ready'),
    ].join('|');
    let last = snapshot();
    new MutationObserver(() => {
      const now = snapshot();
      if (now === last) return;
      last = now;
      applyStaticI18n();
    }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-lang', 'data-i18n-ready'] });
  } catch {
    /* no MutationObserver (very old browser) — the onLangChange path still works */
  }
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initStaticI18n, { once: true });
  } else {
    initStaticI18n();
  }
}
