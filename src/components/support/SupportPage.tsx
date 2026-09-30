/**
 * SupportPage.tsx — Help & FAQ.
 *
 * The ticket inbox is retired: purchases are fulfilled and delivered by
 * CryptoRefills (the merchant of record), so every code, replacement and refund
 * question goes to the address the gift card was emailed to. This page states
 * that plainly, answers the common cases and leaves one contact email for
 * everything else. No API calls, no writes.
 *
 * The copy lives in src/i18n/locales/*.ts under `support.faq.*` — like every
 * other customer-facing screen. It used to be hard-coded English here, on the
 * theory that quoting the supplier's policy verbatim mattered more than
 * translating it; that is wrong for a shop whose buyers read Turkish, German,
 * Spanish, French or Portuguese. A mistranslated policy is a bug, but so is a
 * policy the customer cannot read.
 *
 * Bodies use a deliberately tiny inline syntax so translators can keep the
 * emphasis and the links without touching JSX:
 *   **bold**            → <strong>
 *   [label]({{href}})   → <a href>  (href interpolated by t(): pagePath()s)
 */
import React from 'react';
import { Icon } from '../ui/Icon';
import { AppRoot } from '../AppRoot';
import { AlertBox, CopyButton } from '../ui/uiKit';
import { pagePath } from '../../lib/asset';
import { useT } from '../../i18n';

const CONTACT_EMAIL = 'support@nimiqbase.com';

/** FAQ body keys, in the order they appear on the page. */
const FAQ_KEYS = ['q1', 'q2', 'q3', 'q4', 'q5', 'q6', 'q7'] as const;

/** Render the tiny inline syntax above to React nodes. */
function rich(text: string): React.ReactNode {
  const out: React.ReactNode[] = [];
  const re = /\*\*([^*]+)\*\*|\[([^\]]+)\]\(([^)]+)\)/g;
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index));
    if (m[1] !== undefined) out.push(<strong key={i++}>{m[1]}</strong>);
    else out.push(<a key={i++} href={m[3]}>{m[2]}</a>);
    last = re.lastIndex;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function FaqItem({ qKey, index }: { qKey: string; index: number }) {
  const { t } = useT();
  const q = t(`support.faq.${qKey}` as never);
  return (
    <details className="card faq-item" style={{ padding: 0, overflow: 'hidden' }}>
      <summary
        className="row"
        style={{
          gap: 10, alignItems: 'center', cursor: 'pointer', listStyle: 'none',
          padding: '14px 16px', fontWeight: 600, fontSize: '0.98rem',
        }}
      >
        <span className="faq-num" style={{ flex: 'none', width: 22, height: 22, borderRadius: '50%', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', fontSize: 12, background: 'rgba(127,127,127,.14)' }}>{index + 1}</span>
        <span style={{ flex: 1 }}>{q}</span>
        <Icon name="chevron" size={16} />
      </summary>
      <div style={{ padding: '0 16px 14px 48px', lineHeight: 1.55 }} className="small">
        {rich(t(`support.faq.a${index + 1}` as never, {
          track: pagePath('/track'),
          orders: pagePath('/orders'),
          cashback: pagePath('/cashback'),
        }))}
      </div>
    </details>
  );
}

export function SupportView() {
  const { t } = useT();
  return (
    <div className="container" style={{ maxWidth: 760 }}>
      <div style={{ margin: '6px 0 2px' }}>
        <h1 style={{ display: 'flex', alignItems: 'center', gap: 10, margin: 0 }}>
          <Icon name="headset" size={24} /> {t('support.faq.title')}
        </h1>
        <p className="lede" style={{ margin: '6px 0 14px' }}>{t('support.faq.lede')}</p>
      </div>

      <AlertBox type="warn">
        <strong>{t('support.faq.policyTitle')}</strong> {rich(t('support.faq.policyBody'))}
      </AlertBox>

      <div className="mt-2" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {FAQ_KEYS.map((k, i) => <FaqItem key={k} qKey={k} index={i} />)}
      </div>

      <div className="card mt-2">
        <div className="card-title"><Icon name="mail" size={16} /> {t('support.faq.contactTitle')}</div>
        <div className="small" style={{ lineHeight: 1.55 }}>{t('support.faq.contactBody')}</div>
        <div className="row mt-2" style={{ gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <a className="btn btn-gold btn-sm" href={`mailto:${CONTACT_EMAIL}`}>
            <Icon name="send" size={14} /> {CONTACT_EMAIL}
          </a>
          <CopyButton getText={CONTACT_EMAIL} label="" />
        </div>
      </div>

      <p className="xs faint mt-2" style={{ textAlign: 'center' }}>{t('support.faq.noInbox')}</p>
    </div>
  );
}

export function SupportPage() {
  return (
    <AppRoot activeKey="support">
      <SupportView />
    </AppRoot>
  );
}
