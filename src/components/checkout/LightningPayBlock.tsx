/**
 * LightningPayBlock.tsx — the Nimiq Pay Lightning payment block (ported from
 * ui.js lightningPayBlock). One system, three situations:
 *   1. INSIDE Nimiq Pay: Mini App SDK payLightningInvoice() (NIM or USDT swap).
 *   2. MOBILE browser: try the `lightning:` URI, show missing-app dialog if
 *      nothing opened.
 *   3. DESKTOP: no lightning handler — copy + show missing-app dialog.
 * Plus copy + QR (open by default).
 *
 * Renders the SHARED pay-now skeleton from payRailKit (status line → rail
 * pills → explainer → primary action → copy action → QR toggle → QR frame
 * → warning note), exactly like UsdtPayBlock: same sections, same
 * order — only the words and the QR payload are rail-specific.
 */
import { useEffect, useRef, useState } from 'react';
import { Icon } from '../ui/Icon';
import { Identicon } from '../ui/Identicon';
import { QR } from './QR';
import { fmtNIM } from '../../lib/format';
import { Clipboard } from '../../lib/clipboard';
import { useToast } from '../AppProviders';
import { useT } from '../../i18n';
import { useInNimiqPay, detectMobilePlatform, NIMIQ_PAY_IOS_URL, NIMIQ_PAY_ANDROID_URL } from '../../lib/miniapp';
import { launchLightningUri, isQuotePayable, quoteBolt11 } from '../../lib/pay';
import { payLightningInvoice, isTerminalOutcome } from '../../lib/nimiqPay';
import { ApiError, authorizePaymentLaunch, getQuote, friendlyApiMessage, cachedNimRate } from '../../lib/api';
import { nimAmountFor } from '../../lib/nim';
import { siteName } from '../../lib/config';
import { PayNote, RailPills } from './payRailKit';
import { asset } from '../../lib/asset';


function NimIcon({ size = 18 }: { size?: number }) {
  return (
    <img
      src={asset("/img/nimiq-hexagon.png?v=40")}
      alt="NIM"
      width={size}
      height={size}
      style={{ verticalAlign: 'middle', borderRadius: '3px', display: 'inline-block' }}
    />
  );
}

function MissingDialog({ onClose }: { invoice: string; onClose: () => void }) {
  const { t } = useT();
  return (
    <div className="overlay open" role="alertdialog" aria-modal="true" aria-label={t('checkout.lpNotDetected')} onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
      <div className="sheet" style={{ maxWidth: 460 }}>
        <div className="sheet-head" style={{ justifyContent: 'flex-end' }}>
          <h3 style={{ position: 'absolute', left: 0, right: 0, margin: 0, textAlign: 'center', whiteSpace: 'nowrap', pointerEvents: 'none' }}>
            {t('checkout.lpNotDetected')}
          </h3>
          <button className="sheet-close" aria-label={t('actions.close')} onClick={onClose} style={{ position: 'relative', zIndex: 1 }}>
            <Icon name="x" size={18} />
          </button>
        </div>
        <div className="sheet-body">
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', padding: '6px 0 0' }}>
            <div className="lock-ico">
              <Icon name="nimiq" size={34} />
            </div>
          </div>
          <p className="small muted center" style={{ margin: '10px 0 14px' }}>
            {t('checkout.lpInstallHint')}
          </p>
          <div className="row" style={{ gap: '8px', flexWrap: 'wrap' }}>
            <a className="btn btn-outline" style={{ flex: '1' }} href={NIMIQ_PAY_IOS_URL} target="_blank" rel="noopener noreferrer">
              <span className="btn-label">{t('checkout.lpAppStore')}</span>
            </a>
            <a className="btn btn-outline" style={{ flex: '1' }} href={NIMIQ_PAY_ANDROID_URL} target="_blank" rel="noopener noreferrer">
              <span className="btn-label">{t('checkout.lpGooglePlay')}</span>
            </a>
          </div>
        </div>
      </div>
    </div>
  );
}

/** No payment side effect on mount, remount or status refresh. Every handoff
 * requires a user gesture AND a persisted backend claim after a supplier GET. */
export function LightningPayBlock({ invoice, uri, quoteId, onLaunch, avatarAddress }: {
  invoice: string; uri: string; quoteId: string; onLaunch?: () => void; avatarAddress?: string; compact?: boolean;
}) {
  const { toast } = useToast();
  const { t } = useT();
  const insidePay = useInNimiqPay();
  const [missing, setMissing] = useState(false);
  const [allowed, setAllowed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [payLocked, setPayLocked] = useState(false);
  const [amountNim, setAmountNim] = useState(0);
  // Keep the KEY in state, translate at render: a stored string would freeze
  // in the language the poll last ran in (language switch looked stale).
  const [msgKey, setMsgKey] = useState<'checkout.lpSupplierInvoice' | 'checkout.lpPending' | 'checkout.lpCannotVerify'>('checkout.lpSupplierInvoice');
  const flight = useRef(false);

  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout>;
    const check = async () => {
      try {
        const result = await getQuote(quoteId); const q = result.quote || result;
        if (!alive) return;
        const canPay = isQuotePayable(q) && quoteBolt11(q).toLowerCase() === invoice.toLowerCase();
        setAllowed(canPay);
        setAmountNim(nimAmountFor(q, cachedNimRate()));
        setMsgKey(canPay ? 'checkout.lpSupplierInvoice' : 'checkout.lpPending');
      } catch {
        if (alive) { setAllowed(false); setMsgKey('checkout.lpCannotVerify'); }
      } finally { if (alive) timer = setTimeout(check, 5000); }
    };
    check();
    return () => { alive = false; clearTimeout(timer); };
  }, [quoteId, invoice, t]);

  // Simple flow: one clear Pay button. A single supplier Lightning invoice can
  // only ever be paid once, so re-opening the same invoice is always safe and
  // never a duplicate charge — no "already opened" barrier of any kind.
  const handoff = async (action: 'pay' | 'copy') => {
    if (flight.current || !allowed || (action === 'pay' && payLocked)) return;
    flight.current = true; setBusy(true);
    try {
      // The server re-verifies the supplier state before a wallet handoff
      // (payment_launch.go: fresh supplier GET, details-unchanged and payable
      // checks). That round trip is the ONLY network step between the buyer
      // and their wallet, and it used to decide whether they could pay at all:
      // a slow tunnel or a slow supplier surfaced as "site unreachable" and the
      // invoice in their hand — still valid, still single-use — could not be
      // opened. So the rule is now:
      //   • the server REFUSED (4xx: not payable, held for review, test order,
      //     invoice/amount changed) → block, exactly as before;
      //   • the server could not be reached (network error, timeout, 5xx) →
      //     open the wallet anyway and say so, then retry the verification in
      //     the background so the audit stamp still lands.
      // The button is already gated on the 5-second status poll (`allowed`), so
      // a definitive "no longer payable" from that poll disables it regardless.
      let verifyUnavailable = false;
      try {
        const result = await authorizePaymentLaunch(quoteId);
        const q = result.quote || result;
        if (!isQuotePayable(q) || quoteBolt11(q).toLowerCase() !== invoice.toLowerCase()) {
          throw new ApiError(409, t('checkout.lpNoLongerPayable'));
        }
      } catch (err) {
        const refused = err instanceof ApiError && err.status >= 400 && err.status < 500;
        if (refused) throw err;
        verifyUnavailable = true;
        void authorizePaymentLaunch(quoteId).catch(() => {});
      }
      onLaunch?.();
      if (verifyUnavailable) toast(t('checkout.lpVerifySlow'), 'warn');
      if (action === 'pay' && insidePay) {
        const res = await payLightningInvoice(invoice);
        if (isTerminalOutcome(res)) {
          setPayLocked(true);
          toast(
            res.status === 'submitted'
              ? t('orderPage.nimiqPay.submitted')
              : res.status === 'duplicate'
                ? t('orderPage.nimiqPay.duplicate')
                : t('orderPage.nimiqPay.unknown'),
            res.status === 'submitted' ? 'success' : 'warn',
          );
          return;
        }
        if (res.status === 'declined') {
          toast(res.message, 'info');
          return;
        }
        // A failure the WALLET reported is the wallet's, not ours. It must
        // never be reworded as "our site is unreachable" (that is exactly what
        // a Nimiq Pay -32000 NETWORK_ERROR became before): say what happened,
        // say that nothing was charged, and — when the wallet's own words are
        // short enough to be useful — quote them so support can see them.
        if (res.status === 'unavailable') {
          // payLightningInvoice only answers this when we are NOT inside Nimiq
          // Pay after all — the wallet's own words are useless to the buyer.
          toast(t('checkout.lpWalletNotOpened'), 'info');
          return;
        }
        if (res.status === 'noProvider') {
          const detail = String(res.message || '').trim();
          const shown = detail && detail.length <= 120 && !/https?:\/\//.test(detail) ? ` (${detail})` : '';
          toast(t('checkout.walletNoProvider') + shown, 'warn');
          return;
        }
        if (res.status === 'network' || res.status === 'error' || res.status === 'invalid') {
          const base = res.status === 'network'
            ? t('checkout.walletNetwork')
            : res.status === 'invalid'
              ? t('checkout.walletInvalid')
              : t('checkout.walletFail');
          const detail = String(res.message || '').trim();
          const shown = detail && detail.length <= 120 && !/https?:\/\//.test(detail) ? ` (${detail})` : '';
          toast(base + shown, 'warn');
          return;
        }
        throw new Error('message' in res ? res.message : t('checkout.lpWalletNotOpened'));
      }
      if (action === 'copy') {
        const ok = Clipboard.copy(invoice);
        if (!ok) throw new Error(t('checkout.lpCopyIncomplete'));
        toast(t('checkout.lpInvoiceCopied'), 'info');
        return;
      }
      // Desktop has no wallet to launch: the always-visible QR below IS the
      // hand-off there, so there is nothing else to do.
      if (!detectMobilePlatform()) return;
      launchLightningUri(uri, () => setMissing(true));
    } catch (err) {
      toast(friendlyApiMessage(err, t('checkout.lpWalletNotOpened')), 'warn');
    } finally { flight.current = false; setBusy(false); }
  };
  const disabled = busy || !allowed;
  const amountLabel = amountNim > 0 ? `≈ ${fmtNIM(Math.round(amountNim), 0)} NIM` : '';
  return (
    <div aria-busy={busy}>
      <div className="xs faint mt-1" role="status">{t(msgKey, { site: siteName() })}</div>

      <RailPills
        pills={[
          { bg: '#0582CA', img: asset('/img/nimiq-hexagon.png?v=40'), label: t('checkout.lpPayWithNimOrUsdt') },
          { bg: '#0E6BA8', img: asset('/img/btc-lightning.png'), label: t('checkout.lpBtcNetwork') },
        ]}
      />

      <p className="small muted mt-2" style={{ textAlign: 'center' }}>
        {t('checkout.lpPayExactlyPre')}
        <strong>{amountLabel || t('checkout.lpNimAmountBelow')}</strong>
        {t('checkout.lpPayExactlyPost')}
      </p>

      {/* This card is the NIM rail, but inside Nimiq Pay the asset stays the
          wallet's call: the same invoice can also be settled from a USDT
          balance, and Nimiq Pay shows the swap amount and fees before the
          buyer approves. Said here so nobody thinks they picked the wrong
          card. */}
      {insidePay && (
        <p className="small muted mt-1" style={{ textAlign: 'center' }}>
          {t('checkout.lpAssetChoice')}
        </p>
      )}

      <button type="button" className="btn btn-gold btn-block btn-lg mt-2" disabled={disabled || payLocked} onClick={() => handoff('pay')}>
        <NimIcon /> <span className="btn-label">{busy ? t('checkout.verifying') : payLocked ? t('orderPage.nimiqPay.submitted') : insidePay ? t('orderPage.nimiqPay.idle') : t('checkout.flowPayWithNim')}</span>
      </button>
      <button className="btn btn-outline btn-block mt-1" disabled={disabled} onClick={() => handoff('copy')}><Icon name="copy" size={16} /> {t('checkout.lpCopyRequest')}</button>
      {/* The QR is part of the card, not a reveal: owner removed the
          "Verify & show QR" button and the hide control — a Lightning invoice
          is public data, and one less step between the buyer and the pay. */}
      {allowed && (
        <div className="pay-qr mt-3">
          <div className="pay-qr-frame"><div style={{ position: 'relative', display: 'inline-block', lineHeight: 0 }}>
            <QR text={invoice} size={29} center={asset("/img/nimiq-hexagon.png?v=40")} />
            {avatarAddress && <span className="qr-ava" aria-hidden="true"><Identicon address={avatarAddress} /></span>}
          </div></div>
          <div className="xs faint mt-1">{t('checkout.lpScanOnce')}</div>
        </div>
      )}

      <PayNote>
        {t('checkout.lpPayNote')}
      </PayNote>

      {missing && <MissingDialog invoice={invoice} onClose={() => setMissing(false)} />}
    </div>
  );
}
