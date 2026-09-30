/**
 * UsdtPayBlock.tsx — the USDT-on-Polygon rail, rendered from the EXACT same
 * template as LightningPayBlock (the port of the old shop's pay block the
 * operator prefers): status line → rail pills → explainer → primary action →
 * copy action → QR toggle → QR frame → warning note. Same classes, same order,
 * same button stack, same QR chrome. The ONLY differences are the words and
 * the QR payload. There is deliberately no rail-specific CSS and no
 * rail-specific chrome — one payment UI, two rails.
 *
 * The stablecoin rail is USDT on Polygon (contract 0xc2132…8e8F, 6 decimals).
 *
 * Inside Nimiq Pay the provider injected by the app (window.ethereum,
 * EIP-1193) lets the buyer pay in ONE tap: connect → switch to Polygon →
 * ERC-20 transfer of the exact amount to the order's one-time address. Keys
 * never leave the wallet; every step is confirmed in a native dialog. Outside
 * Nimiq Pay there is no provider, so the block falls back to the wallet
 * handoff (deep link / copy / QR) exactly as before.
 */
import { useEffect, useMemo, useState } from 'react';
import { Icon } from '../ui/Icon';
import { Identicon } from '../ui/Identicon';
import { QR } from './QR';
import { Clipboard } from '../../lib/clipboard';
import { coinAmountOf } from '../../lib/deliveryCopy';
import { useToast } from '../AppProviders';
import { useT } from '../../i18n';
import { siteName } from '../../lib/config';
import { PayNote, RailPills } from './payRailKit';
import { inNimiqPay } from '../../lib/miniapp';
import { asset } from '../../lib/asset';
import {
  UsdtCancelledError,
  connectEvmAccount,
  ensurePolygonChain,
  getEvmProvider,
  readUsdtBalance,
  sendUsdtTransfer,
} from '../../lib/usdt';

/** Same slot, size and shape as the NIM block's hexagon icon — a real token
 * mark, never an invented glyph. SVG scales crisply at pill/hero sizes. */
function PolygonIcon({ size = 18 }: { size?: number }) {
  return (
    <img
      src={asset("/img/polygon.png")}
      alt="Polygon"
      width={size}
      height={size}
      style={{ verticalAlign: 'middle', borderRadius: 3, display: 'inline-block', background: 'var(--white-card)' }}
    />
  );
}

/** No payment side effect on mount, remount or status refresh — a wallet
 * connection or transfer only ever happens on an explicit user gesture,
 * exactly like the NIM block. Provider detection is hydration-safe: it runs
 * after mount so the server HTML and the first client render agree. */
export function UsdtPayBlock({ quote, expired, onLaunchRequested, avatarAddress }: {
  quote: any; expired: boolean; onLaunchRequested?: () => void; avatarAddress?: string;
}) {
  const { toast } = useToast();
  const { t } = useT();
  const [showQR, setShowQR] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [evm, setEvm] = useState<unknown | null>(null);
  const [noEvmInPay, setNoEvmInPay] = useState(false);
  const [account, setAccount] = useState('');
  const [balance, setBalance] = useState<string | null>(null);
  const [txHash, setTxHash] = useState('');

  const amount = coinAmountOf(quote);
  const address = String(quote?.wallet_address ?? '');
  const coin = 'USDT';

  useEffect(() => {
    const p = getEvmProvider();
    setEvm(p);
    // Computed AFTER mount (never during render) so hydration can't mismatch.
    setNoEvmInPay(inNimiqPay() && !p);
  }, []);

  const canPayInApp = !!evm && !!amount && !!address;

  // EVM deep link for the browser fallback; the QR encodes the bare address.
  const payUri = useMemo(() => (address ? `ethereum:${address}@137` : ''), [address]);

  /** The in-Nimiq-Pay flow: connect → Polygon → exact USDT transfer. */
  async function payInApp() {
    if (busy || expired || !canPayInApp) return;
    setErr('');
    setBusy(true);
    try {
      const p = getEvmProvider();
      if (!p) throw new Error(t('checkout.usdtpNotConnected'));
      const acct = account || (await connectEvmAccount(p));
      setAccount(acct);
      await ensurePolygonChain(p);
      // Best-effort balance read: a failed read must not block the attempt,
      // but a SUCCESSFUL read below the order amount saves a doomed tx.
      const bal = await readUsdtBalance(p, acct);
      setBalance(bal);
      if (bal !== null && amount && Number(bal) < Number(amount)) {
        throw new Error(
          t('checkout.usdtpNotEnough', { bal: String(bal), amount: String(amount) })
        );
      }
      const hash = await sendUsdtTransfer(p, acct, address, amount);
      setTxHash(hash);
      toast(t('checkout.usdtpSent'), 'success');
    } catch (e) {
      if (e instanceof UsdtCancelledError) setErr(e.message);
      else setErr((e as Error).message || t('checkout.usdtpSendFailed'));
    } finally {
      setBusy(false);
    }
  }

  /** Browser fallback: open an external Polygon wallet on the payment address. */
  const handoff = async (action: 'pay' | 'copy' | 'qr') => {
    if (busy || expired || !address) return;
    setBusy(true);
    try {
      if (action === 'qr') { setShowQR(true); return; }
      if (action === 'copy') {
        // Same Nimiq Pay clipboard as the Lightning rail — one copy behaviour
        // site-wide, with a truthful success/failure answer.
        const ok = Clipboard.copy(address);
        if (!ok) throw new Error(t('checkout.usdtpCopyIncomplete'));
        toast(t('checkout.usdtpAddressCopied'), 'info');
        return;
      }
      onLaunchRequested?.();
      if (payUri) window.location.href = payUri;
    } catch (err) {
      toast((err as Error).message || t('checkout.usdtpWalletNotOpened'), 'warn');
    } finally { setBusy(false); }
  };

  const disabled = busy || expired || !address;
  const amountLabel = amount ? `${amount} ${coin}` : '';
  return (
    <div aria-busy={busy}>
      <div className="xs faint mt-1" role="status">{t('checkout.usdtpOneInvoice', { site: siteName() })}</div>

      <RailPills
        pills={[
          { bg: '#26A17B', img: asset('/img/usdt.png'), label: t('checkout.usdtpPayWith', { coin }) },
          { bg: '#8247E5', img: asset('/img/polygon.png'), label: t('checkout.usdtpPolygonNetwork') },
        ]}
      />

      <p className="small muted mt-2" style={{ textAlign: 'center' }}>
        {t('checkout.usdtpSendExactlyPre')}
        <strong>{amountLabel || t('checkout.usdtpUsdtAmountBelow')}</strong>
        {t('checkout.usdtpSendExactlyPost')} {evm ? t('checkout.usdtpTapNote') : t('checkout.usdtpAutoNote')}
      </p>

      {canPayInApp && (
        <button type="button" className="btn btn-gold btn-block btn-lg mt-2" disabled={disabled} onClick={payInApp}>
          <PolygonIcon />{' '}
          <span className="btn-label">
            {busy
            ? t('checkout.usdtpConfirmInPay')
            : txHash
              ? t('checkout.usdtpSendAgain')
              : amount
                ? t('checkout.usdtpPayExact', { amount, coin })
                : t('checkout.usdtpPayInPay')}
          </span>
        </button>
      )}
      {(account || balance !== null) && (
        <div className="xs faint mt-1" style={{ textAlign: 'center' }}>
          {account ? <>{t('checkout.usdtpPayingWith')} <span className="mono">{account.slice(0, 8)}…{account.slice(-6)}</span></> : null}
          {balance !== null ? <> · USDT balance {balance}</> : null}
        </div>
      )}
      {txHash && (
        <div className="alert success mt-1">
          <div className="small">
            <Icon name="check" size={14} /> {t('checkout.usdtpTransferSent')} <span className="mono">{txHash.slice(0, 14)}…</span>
            . Confirmation usually takes ~30 seconds to a few minutes; this page updates itself.
          </div>
        </div>
      )}

      {/* Browser / legacy fallback — identical to the old handoff. */}
      <button
        type="button"
        className={canPayInApp ? 'btn btn-outline btn-block mt-1' : 'btn btn-gold btn-block btn-lg mt-2'}
        disabled={disabled}
        onClick={() => handoff('pay')}
      >
        <PolygonIcon /> <span className="btn-label">{busy && !canPayInApp ? t('checkout.verifying') : expired ? t('checkout.usdtpExpiredRefresh') : t('checkout.usdtpOpenWallet')}</span>
      </button>
      <button className="btn btn-outline btn-block mt-1" disabled={disabled} onClick={() => handoff('copy')}><Icon name="copy" size={16} /> {t('checkout.usdtpCopyAddress')}</button>
      <button className="btn btn-outline btn-block mt-1" disabled={disabled} onClick={() => handoff('qr')}>{t('checkout.usdtpVerifyShowQr')}</button>
      {showQR && !!address && (
        <div className="pay-qr mt-3">
          <div className="pay-qr-frame"><div style={{ position: 'relative', display: 'inline-block', lineHeight: 0 }}>
            <QR text={address} size={29} center={asset("/img/nimiq-hexagon.png?v=128")} />
            {avatarAddress && <span className="qr-ava" aria-hidden="true"><Identicon address={avatarAddress} /></span>}
          </div></div>
          <div className="xs faint mt-1">{t('checkout.usdtpScanOnce', { coin })}</div>
          <button className="btn btn-ghost mt-1" onClick={() => setShowQR(false)}>{t('checkout.usdtpHideQr')}</button>
        </div>
      )}

      {noEvmInPay && (
        <div className="xs faint mt-1" style={{ textAlign: 'center' }}>
          {t('checkout.usdtpNoEvm')}
        </div>
      )}

      {err && <div className="alert error mt-1"><div className="small">{err}</div></div>}

      <PayNote>
        {t('checkout.usdtpPayNote')}
      </PayNote>
    </div>
  );
}
