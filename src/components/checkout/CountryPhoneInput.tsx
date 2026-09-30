/**
 * CountryPhoneInput — the number field for anything that is delivered to a
 * PHONE NUMBER (mobile top-up), locked to the product's own country.
 *
 * Why it exists: the buyer used to face an empty box with the placeholder
 * "+90 555 123 45 67 (or 0555 123 45 67)" and had to know that a top-up sold
 * for Türkiye must be a Turkish number. Two things were wrong with that —
 * typing "+90" by hand is busywork the system can do, and nothing stopped
 * "+49…" from being typed and accepted by the format check (only the supplier's
 * dry-run caught it, after the buyer had already been shown a number as valid).
 *
 * So the field is SPLIT: the country, its flag and its dial code are supplied
 * by the system and are not editable, and the buyer types the national digits.
 * The country stops being a validation rule and becomes a structural fact —
 * there is no place in the field to put another country. A pasted foreign
 * number ("0049…", "+49…" into the digits box) is still possible and is refused
 * out loud by lib/phoneCountry, never re-prefixed into a number that belongs
 * to nobody.
 *
 * Value contract, so the steps keep calling collectPhoneValue the same way:
 * `value`/`onChange` carry the NATIONAL DIGITS for a country we know and the
 * RAW TEXT for a country we do not (then this is just the old free-form box —
 * we never guess a dial code). A value that cannot be locked to the country is
 * also carried through as typed, so a submit that reads it back sees a broken
 * number instead of the last good one.
 */
import { useState } from 'react';
import { countryLabel, flagEmoji, parseCountryPhone, phoneCountry } from '../../lib/phoneCountry';
import { useT } from '../../i18n';

export type CountryPhoneInputProps = {
  id: string;
  /** The PRODUCT's country — that is what locks the number. */
  country?: string;
  value: string;
  onChange: (v: string) => void;
  /** Extra sentence under the box; the label itself belongs to the step. */
  hint?: React.ReactNode;
  autoFocus?: boolean;
  disabled?: boolean;
  /** Marks the box invalid without moving anything (the step shows the text). */
  invalid?: boolean;
  /** Enter continues the step, as it did in the plain input. */
  onEnter?: () => void;
  'aria-label'?: string;
};

export function CountryPhoneInput({
  id,
  country,
  value,
  onChange,
  hint,
  autoFocus,
  disabled,
  invalid,
  onEnter,
  ...rest
}: CountryPhoneInputProps) {
  const { t } = useT();
  const c = phoneCountry(country);
  const [raw, setRaw] = useState(value);
  // Render-time sync: when the step resets the value (a cleared gift block, a
  // prefilled number), the box follows it instead of keeping stale digits.
  const [synced, setSynced] = useState(value);
  if (value !== synced) {
    setSynced(value);
    setRaw(value);
  }

  const box: React.CSSProperties = {
    display: 'flex',
    alignItems: 'stretch',
    border: `1.5px ${invalid ? 'solid' : 'dashed'} var(--line-dash)`,
    borderRadius: 'var(--r-m)',
    background: 'var(--surface-1)',
    overflow: 'hidden',
  };
  const prefix: React.CSSProperties = {
    display: 'inline-flex',
    alignItems: 'center',
    gap: '6px',
    padding: '0 10px',
    borderRight: '1.5px dashed var(--line-dash)',
    background: 'var(--surface-0)',
    fontWeight: 700,
    whiteSpace: 'nowrap',
    userSelect: 'none',
  };
  const bare: React.CSSProperties = {
    flex: '1 1 auto',
    minWidth: 0,
    border: 'none',
    outline: 'none',
    background: 'transparent',
    borderRadius: 0,
  };

  const enterKey = (e: React.KeyboardEvent) => {
    if (e.key !== 'Enter' || !onEnter) return;
    e.preventDefault();
    onEnter();
  };

  const apply = (text: string) => {
    setRaw(text);
    if (!c) {
      setSynced(text);
      onChange(text);
      return;
    }
    const next = parseCountryPhone(text, c.code);
    // Unparseable input is carried through as typed on purpose: swallowing it
    // would leave the step holding the PREVIOUS number, and a submit would
    // top up a number the buyer can no longer see in the box.
    const carry = next.error || !text ? text : next.national;
    setSynced(carry);
    onChange(carry);
  };

  // No table entry for this country: the old free-form box, no flag, no lock.
  if (!c) {
    return (
      <div style={{ display: 'grid', gap: '4px' }}>
        <input
          id={id}
          className="input"
          type="tel"
          inputMode="tel"
          placeholder="+1 555 123 4567"
          autoComplete="tel"
          autoFocus={autoFocus}
          disabled={disabled}
          aria-label={rest['aria-label'] || t('phone.intlAria')}
          aria-invalid={invalid || undefined}
          value={raw}
          onChange={(e) => apply(e.target.value)}
          onKeyDown={enterKey}
          data-country="unknown"
        />
        {hint ? <div className="small muted">{hint}</div> : null}
      </div>
    );
  }

  const parsed = parseCountryPhone(raw, c.code);
  const flag = flagEmoji(c.code);

  return (
    <div style={{ display: 'grid', gap: '4px' }} data-country={c.code}>
      <div style={box}>
        {/* Not an input: the buyer cannot delete or edit the prefix, so a wrong
            country cannot be produced by accident or by an autofill. */}
        <span style={prefix} title={t('phone.titleSet', { country: countryLabel(c.code), dial: c.dial })}>
          {flag ? (
            <span aria-hidden="true" style={{ fontSize: '1.05rem', lineHeight: 1 }}>
              {flag}
            </span>
          ) : null}
          <span className="mono">+{c.dial}</span>
        </span>
        <input
          id={id}
          className="input"
          style={bare}
          type="tel"
          inputMode="numeric"
          placeholder={exampleOf(c.code, c.national[0])}
          autoComplete="tel-national"
          autoFocus={autoFocus}
          disabled={disabled}
          aria-label={rest['aria-label'] || t('phone.ariaNumber', { country: countryLabel(c.code), dial: c.dial })}
          aria-invalid={invalid || undefined}
          aria-describedby={`${id}-hint`}
          value={raw}
          onChange={(e) => apply(e.target.value)}
          onKeyDown={enterKey}
        />
      </div>
      <div className="small muted" id={`${id}-hint`}>
        {hint ?? `${flag ? `${flag} ` : ''}${t('phone.hintNumber', { country: countryLabel(c.code), dial: c.dial })}`}
      </div>
      {parsed.error ? (
        <div className="note-box warn" role="alert" style={{ marginTop: 0, display: 'flex', gap: '6px', alignItems: 'flex-start' }}>
          <span aria-hidden="true">⚠️</span>
          <span className="small">{parsed.error}</span>
        </div>
      ) : parsed.warning ? (
        <div className="small muted" style={{ display: 'flex', gap: '6px', alignItems: 'flex-start' }}>
          <span aria-hidden="true">ℹ️</span>
          <span>{parsed.warning}</span>
        </div>
      ) : null}
    </div>
  );
}

/** A length-shaped placeholder: "10 digits" must be visible without the buyer
 *  reading one concrete number as an example to copy. */
function exampleOf(code: string, len: number): string {
  if (code === 'TR') return '555 123 45 67';
  if (code === 'US' || code === 'CA') return '555 123 4567';
  if (code === 'DE') return '151 2345678';
  if (code === 'GB') return '7400 123456';
  const n = Math.max(len || 0, 7);
  const size = Math.ceil(n / 3);
  const groups = '5'.repeat(n).match(new RegExp(`.{1,${size}}`, 'g')) || [];
  return groups.join(' ');
}
