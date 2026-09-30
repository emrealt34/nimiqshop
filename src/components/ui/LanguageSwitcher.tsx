/**
 * LanguageSwitcher.tsx — six-language dropdown with hex flags.
 *
 *   • Renders a globe button in the top bar. Click/tap opens a popover listing
 *     all six supported languages, each with its national hex-flag and the
 *     native name (the way speakers expect it to read — "Deutsch", not "German").
 *   • Selecting a language calls setLang() from the i18n context, which:
 *       - re-renders every translated string instantly (static bundles, no fetch),
 *       - writes the choice to localStorage and a SameSite `nimshop-lang` cookie
 *         so the backend reads it on every subsequent API request (this is how
 *         gift emails end up in the buyer's language).
 *
 * The switcher uses the same FlagHex primitive as country-chips so the flags
 * clip to the same hexagon as the rest of the UI.
 *
 * STYLING — ROOT FIX 2026-09-20
 * -----------------------------
 * Every layout/colour property used to live in an inline `style={{…}}` object
 * whose background was `var(--card-bg, #fffdf7)`. `--card-bg` is not a token
 * this theme defines, so the literal fallback won and the popover painted
 * WHITE in dark mode, with `color: inherit` resolving to --ink (#e8e9ec):
 * white text on a white card. The popover is now styled by `.lang-menu` /
 * `.lang-toggle` in app.css from theme tokens only (they have a
 * [data-theme="dark"] counterpart), so it follows the theme like every other
 * popover (.acct-menu, .country-pop). Do not reintroduce colour literals here.
 */
import { useEffect, useRef, useState } from 'react';
import { Icon } from './Icon';
import { FlagHex } from './FlagHex';
import { useT, type LangCode } from '../../i18n';

export function LanguageSwitcher() {
  const { lang, setLang, t, langs } = useT();
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [autoPicked, setAutoPicked] = useState(false);

  useEffect(() => {
    // Remember whether the current language was auto-detected. If the user
    // has explicitly chosen before, the storage key will be present.
    try {
      setAutoPicked(!localStorage.getItem('nimshop.lang'));
    } catch { /* no-op */ }
  }, []);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent | TouchEvent) => {
      if (!wrapRef.current || !wrapRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('touchstart', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('touchstart', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const current = langs.find((l) => l.code === lang) || langs[0];

  return (
    <div ref={wrapRef} className="lang-wrap">
      <button
        type="button"
        className="lang-toggle"
        /* No aria-label: the sr-only text below is both the visible (a11y-tree)
           label and the accessible name, so the two can never disagree. */
        aria-haspopup="menu"
        aria-expanded={open}
        title={t('lang.label')}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name="globe" size={18} />
        <FlagHex country={current.flag} size={16} />
        <span className="sr-only">{t('lang.choose')}: {current.label}</span>
      </button>
      {open && (
        <div className="lang-menu open" role="menu" aria-label={t('lang.label')}>
          {langs.map((l) => {
            const selected = l.code === lang;
            return (
              <button
                key={l.code}
                role="menuitemradio"
                type="button"
                aria-checked={selected}
                lang={l.code}
                onClick={() => {
                  setLang(l.code as LangCode);
                  setOpen(false);
                  setAutoPicked(false);
                }}
                aria-current={selected ? 'true' : undefined}
                title={l.english}
              >
                <FlagHex country={l.flag} size={20} />
                <span className="lang-name">
                  <span className="lang-native strong">{l.label}</span>
                  {autoPicked && selected && (
                    <span className="lang-auto">{t('lang.autoDetected')}</span>
                  )}
                </span>
                {selected && <Icon name="check" size={16} className="lang-check" />}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

export default LanguageSwitcher;
