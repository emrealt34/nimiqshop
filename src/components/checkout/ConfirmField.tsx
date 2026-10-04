/**
 * ConfirmField.tsx — "type it twice" for a value that a typo would destroy.
 *
 * The delivery email and the top-up number are the two fields where a single
 * wrong character has no recovery path: the code is mailed to an inbox the
 * buyer cannot read, or the credit is pushed onto a stranger's line. So both
 * are asked for a second time, right under the first entry, and the checkout
 * refuses to continue while the two disagree.
 *
 * The component is deliberately dumb: it renders a label, the confirm input
 * and (optionally) one error line — all validation and comparison live in the
 * caller, next to the value it guards. Styling matches the checkout's existing
 * inputs one-for-one so the second field reads as a confirmation of the first,
 * not as a second question.
 */
import type { ReactNode } from 'react';

export function ConfirmField({
  id,
  label,
  value,
  onChange,
  placeholder,
  type = 'text',
  inputMode,
  autoComplete = 'off',
  invalid = false,
  error = '',
  hint,
  onEnter,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  type?: string;
  inputMode?: 'text' | 'email' | 'tel' | 'numeric';
  autoComplete?: string;
  invalid?: boolean;
  error?: string;
  hint?: ReactNode;
  onEnter?: () => void;
}) {
  return (
    <div className="field" style={{ marginTop: 8 }}>
      <label htmlFor={id} style={{ fontSize: 12, fontWeight: 800, color: 'var(--ink-dim)' }}>
        {label}
      </label>
      <input
        id={id}
        className="input"
        type={type}
        inputMode={inputMode}
        autoComplete={autoComplete}
        spellCheck={false}
        autoCapitalize="none"
        placeholder={placeholder}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && onEnter) onEnter();
        }}
        onPaste={(e) => e.preventDefault()}
        style={{
          padding: '12px 14px',
          border: invalid ? '2px solid var(--stamp)' : '2px solid var(--line-strong)',
          borderRadius: 8,
          background: 'var(--surface-1)',
          width: '100%',
        }}
      />
      {hint && !error && (
        <div className="small muted" style={{ marginTop: 4 }}>
          {hint}
        </div>
      )}
      {error && (
        <div className="small" style={{ color: 'var(--stamp)', marginTop: 4 }}>
          {error}
        </div>
      )}
    </div>
  );
}
