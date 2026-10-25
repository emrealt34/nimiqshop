/**
 * LightningPayBlock.tsx — the Nimiq Pay Lightning payment block (ported from
 * ui.js lightningPayBlock). One system, three situations:
 *   1. INSIDE Nimiq Pay: Mini App SDK payLightningInvoice() (NIM or USDT swap).
 *   2. MOBILE browser: try the `lightning:` URI, toast "Nimiq Pay not found"
 *      (QR hint + store links) if nothing opened.
 *   3. DESKTOP: no lightning handler — copy + the same toast.
 * Plus copy + QR (open by default).
 *
 * Renders the SHARED pay-now skeleton from payRailKit (status line → rail
 * pills → explainer → primary action → copy action → QR toggle → QR frame
 * → warning note), same sections, same
 * order — only the words and the QR payload are rail-specific.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { Icon } from '../ui/Icon';
import { QR } from './QR';
import { Clipboard } from '../../lib/clipboard';
import { useToast } from '../AppProviders';
import { useNimiqPayInstallSheet } from '../ui/NimiqPayInstallDialog';
import { useT } from '../../i18n';
import { useInNimiqPay, detectMobilePlatform } from '../../lib/miniapp';
import { launchLightningUri, isQuotePayable, quoteBolt11 } from '../../lib/pay';
import { payLightningInvoice, isTerminalOutcome } from '../../lib/nimiqPay';
import { saveLightningFunding } from '../../lib/api';
import { ApiError, authorizePaymentLaunch, getQuote, friendlyApiMessage, cachedNimRate } from '../../lib/api';
import { copyPaymentRequest } from '../../lib/paymentHandoff';
import { nimAmountFor } from '../../lib/nim';
import { siteName } from '../../lib/config';
import { asset } from '../../lib/asset';



/**
 * "Nimiq Pay not found" is a POPUP, not a toast.
 *
 * Owner (2026-10-06): "bi yerde toast olarak 'Nimiq Pay bulunamadı … Google
 * Play Store' diyor … o toast değil popup olacaktı, anladın mı, var olan popup
 * göreceksin zaten". The toast it replaced (owner, 2026-10-04) tried to squeeze
 * two store buttons into a 3.5-second strip: it disappeared while the buyer was
 * still reading which store to tap. The shop's existing Nimiq Pay popup — the
 * same one the home page and "open in Nimiq Pay" open — now carries this case
 * too, with the reason line above the store buttons, and it stays until the
 * buyer closes it or taps a store.
 */
export function useNimiqPayMissingDialog() {
  const openInstall = useNimiqPayInstallSheet();
  return useCallback(
    () => openInstall({ messageKey: 'checkout.nimiqPayNotFound' }),
    [openInstall]
  );
}

export function LightningPayBlock({ invoice, uri, quoteId, onLaunch, hidePayButton }: {
  invoice: string; uri: string; quoteId: string; onLaunch?: () => void; compact?: boolean;
  /** The pay screen puts the Nimiq Pay button inside its hero card (owner,
   *  2026-10-04); then the block's own copy would be a duplicate. */
  hidePayButton?: boolean;
}) {
  const { toast } = useToast();
  const { t } = useT();
  const insidePay = useInNimiqPay();
  const notifyMissing = useNimiqPayMissingDialog();
  const [allowed, setAllowed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [payLocked, setPayLocked] = useState(false);
  const [, setAmountNim] = useState(0);
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

  /**
   * COPY IS LOCAL; THE STAMP IS NOT (owner, 2026-10-25: "nimiq pay ödeme
   * talebini kopyala … çalışmıyor").
   *
   * The copy used to share the pay button's handler, which awaits the
   * server-side `payment-launch` verification first. Two things were wrong
   * with that:
   *
   *  • `document.execCommand('copy')` only works while the click's user
   *    activation is alive. After an awaited network round trip (Chromium:
   *    ~5 s; WebKit: the next task) it is gone, so Clipboard.copy returned
   *    false and the buyer got "Kopyalama tamamlanmadı" — most often exactly
   *    when the supplier was slow.
   *  • A 4xx from that POST (expired session, order held, supplier
   *    unreachable) threw BEFORE the copy ran, so the shop refused to hand
   *    over the invoice in the very situation where it has to be copied by
   *    hand.
   *
   * Now the clipboard is written first, synchronously, and the audit stamp
   * (`payment_launch` → ClaimPaymentHandoff, audit only on the backend) is
   * fired afterwards, fire-and-forget, with its failure swallowed. Copying is
   * never gated on the server, on the 5-second status poll, or on a pay
   * handoff already being in flight: the invoice is public, single-use data
   * that is already printed on the buyer's screen.
   */
  const copyRequest = useCallback(() => {
    const outcome = copyPaymentRequest(invoice, {
      copy: (text) => Clipboard.copy(text),
      stamp: () => {
        onLaunch?.();
        return authorizePaymentLaunch(quoteId);
      },
    });
    if (outcome === 'copied') {
      toast(t('checkout.lpInvoiceCopied'), 'info');
      return;
    }
    // 'failed' (the browser refused the clipboard) and 'empty' (no invoice on
    // this screen — refresh the order) are both a real "nothing was copied".
    toast(t('checkout.lpCopyIncomplete'), 'warn');
  }, [invoice, quoteId, onLaunch, toast, t]);

  // Simple flow: one clear Pay button. A single supplier Lightning invoice can
  // only ever be paid once, so re-opening the same invoice is always safe and
  // never a duplicate charge — no "already opened" barrier of any kind.
  const handoff = async () => {
    if (flight.current) return;
    // Owner (2026-10-05): a click must NEVER be silent. Pending/unverifiable
    // status and an already-submitted payment answer with a toast instead of
    // a dead disabled button.
    if (payLocked) {
      toast(t('orderPage.nimiqPay.submitted'), 'info');
      return;
    }
    if (!allowed) {
      toast(t(msgKey === 'checkout.lpCannotVerify' ? 'checkout.lpCannotVerify' : 'checkout.lpPending'), 'warn');
      return;
    }
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
      if (insidePay) {
        const res = await payLightningInvoice(invoice);
        if (isTerminalOutcome(res)) {
          if ('hash' in res || 'swapId' in res) void saveLightningFunding(quoteId, res.hash || '', res.swapId).catch(() => {});
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
        if (res.status === 'updateRequired') {
          toast(t('wallet.updatePayToast'), 'warn');
          return;
        }
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
      // Owner (2026-10-05): outside Nimiq Pay the button must DO something.
      // Desktop has no wallet to launch — the POPUP (QR-below hint + Nimiq Pay
      // download links) is the hand-off there, shown at once; mobile tries the
      // lightning: URI first and opens the popup when nothing opened.
      if (!detectMobilePlatform()) {
        notifyMissing();
        return;
      }
      launchLightningUri(uri, notifyMissing);
    } catch (err) {
      toast(friendlyApiMessage(err, t('checkout.lpWalletNotOpened')), 'warn');
    } finally { flight.current = false; setBusy(false); }
  };
  return (
    <div aria-busy={busy}>
      <div className="xs faint mt-1" role="status">{t(msgKey, { site: siteName() })}</div>

      {!hidePayButton && (
        <button type="button" className="btn btn-gold btn-block btn-lg mt-2" disabled={busy} onClick={handoff}>
          <span className="btn-label">{busy ? t('checkout.verifying') : payLocked ? t('orderPage.nimiqPay.submitted') : insidePay ? t('orderPage.nimiqPay.idle') : t('checkout.flowPayWithNim')}</span>
        </button>
      )}
      {/* Deliberately NOT disabled by `busy`: copying is a local clipboard
          write, so a pay handoff still verifying against the supplier (up to
          30 s) must never freeze the one control that hands the buyer their
          invoice. */}
      <button type="button" className="btn btn-outline btn-block mt-1" onClick={copyRequest}><Icon name="copy" size={16} /> {t('checkout.lpCopyRequest')}</button>
      {/* The QR is part of the card, not a reveal: owner removed the
          "Verify & show QR" button and the hide control — a Lightning invoice
          is public data, and one less step between the buyer and the pay. */}
      {allowed && (
        <div className="pay-qr mt-3">
          {/* Centre mark = the shop's own brand tile — never the payer's
              identicon (owner, 2026-10-05). */}
          <div className="pay-qr-frame">
            <QR text={invoice} size={29} center={asset("/img/brand-icon-96.png")} />
          </div>
          <div className="xs faint mt-1">{t('checkout.lpScanOnce')}</div>
        </div>
      )}
    </div>
  );
}
