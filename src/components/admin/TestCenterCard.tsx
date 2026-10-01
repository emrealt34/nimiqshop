/**
 * TestCenterCard.tsx — the operator's end-to-end sandbox: buy a REAL
 * catalog product on the SIMULATED supplier, fake-pay it through the REAL
 * state machine (broadcast → confirm → deliver), and watch the same
 * pipeline a live order rides: activity feed, admin order views, and the
 * gift email through Mailtrap.
 *
 * Safety (backend-enforced, mirrored here for the operator): EVERYTHING
 * runs — the cashback rate engine and queue, tree contributions, the wallet
 * memo policy pipeline — but every PAYMENT is simulated: the purchase, the
 * cashback payout (TESTTX-… hash, no signing/RPC) and the memo broadcast.
 * The real Cryptorefills supplier is never touched. The only real side
 * effect is the gift email to the address you type.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { Icon } from '../ui/Icon';
import { useToast } from '../AppProviders';
import {
  adminTestPurchase,
  adminTestPay,
  adminTestQuote,
  listGiftCards,
  getProduct,
} from '../../lib/api';
import { flattenBrands, type Product } from '../../lib/catalog';
import { countryName } from '../../lib/format';
import { orderedCountries, type CountryRow } from '../../lib/countries';

const COUNTRY_OPTIONS: CountryRow[] = (() => {
  const { popular, rest } = orderedCountries();
  return [...popular, ...rest];
})();

type Pkg = { denomination: string; range: boolean };

type LogLine = { at: string; text: string; tone: 'info' | 'ok' | 'err' };

function nowStamp() {
  return new Date().toLocaleTimeString([], { hour12: false });
}

export function TestCenterCard() {
  const { toast } = useToast();

  // picker state
  const [country, setCountry] = useState('US');
  const [products, setProducts] = useState<Product[]>([]);
  const [loadingProducts, setLoadingProducts] = useState(false);
  const [productId, setProductId] = useState('');
  const [pkgs, setPkgs] = useState<Pkg[]>([]);
  const [denomination, setDenomination] = useState('');
  const [rangeValue, setRangeValue] = useState('');
  const [quantity, setQuantity] = useState(1);

  // order state
  const [email, setEmail] = useState('');
  const [giftMessage, setGiftMessage] = useState('');
  const [anonymous, setAnonymous] = useState(false);
  const [paymentMethod, setPaymentMethod] = useState('nimiq_pay');
  const [cashbackDestination, setCashbackDestination] = useState('cashback');

  // run state
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [quote, setQuote] = useState<any | null>(null);
  const [log, setLog] = useState<LogLine[]>([]);
  const pollRef = useRef<number | null>(null);

  const say = useCallback((text: string, tone: LogLine['tone'] = 'info') => {
    setLog((l) => [...l.slice(-40), { at: nowStamp(), text, tone }]);
  }, []);

  useEffect(() => {
    let alive = true;
    setLoadingProducts(true);
    setProductId('');
    setPkgs([]);
    setDenomination('');
    listGiftCards(country)
      .then((data: any) => {
        if (!alive) return;
        const list = flattenBrands(data, country).filter((p) => p.in_stock !== false);
        setProducts(list);
        say(`catalog: ${list.length} products in ${country}`);
      })
      .catch((e: Error) => alive && say(`catalog load failed: ${e.message}`, 'err'))
      .finally(() => alive && setLoadingProducts(false));
    return () => {
      alive = false;
    };
  }, [country, say]);

  useEffect(
    () => () => {
      if (pollRef.current) window.clearInterval(pollRef.current);
    },
    []
  );

  const pickProduct = async (id: string) => {
    setProductId(id);
    setPkgs([]);
    setDenomination('');
    setRangeValue('');
    if (!id) return;
    try {
      const data: any = await getProduct(id, country);
      // /catalog/products/:id keeps the legacy Family[] response shape;
      // single-family callers may receive { products: [...] }. Normalize both
      // shapes before filling the denomination select.
      const families: any[] = Array.isArray(data) ? data : data?.products ? [data] : [];
      const rows: any[] = families.flatMap((family: any) => Array.isArray(family?.products) ? family.products : []);
      const out: Pkg[] = [];
      for (const p of rows) {
        if (p.range) {
          out.push({ denomination: `range (${p.range.min}–${p.range.max} ${p.range.currency || ''})`, range: true });
          continue;
        }
        const label = p.denomination || p.localized_denomination || p.product_id || '';
        if (label) out.push({ denomination: label, range: false });
      }
      setPkgs(out);
      if (out.length === 1 && !out[0].range) setDenomination(out[0].denomination);
    } catch (e) {
      say(`product detail failed: ${(e as Error).message}`, 'err');
    }
  };

  const isRange = pkgs.find((p) => p.denomination === denomination)?.range === true;

  const createOrder = async () => {
    setErr('');
    setQuote(null);
    setBusy(true);
    try {
      if (!productId) throw new Error('Pick a product first.');
      if (!denomination) throw new Error('Pick a denomination first.');
      if (isRange && (!(Number(rangeValue) > 0))) throw new Error('Enter a positive amount for a range product.');
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim())) throw new Error('Enter a valid recipient email (delivery + gift note address).');
      const req: Record<string, unknown> = {
        product_id: productId,
        country,
        quantity,
        email: email.trim(),
        payment_method: paymentMethod,
        cashback_destination: cashbackDestination,
        anonymous,
      };
      if (isRange) {
        req.denomination = 'range';
        req.product_value = Number(rangeValue);
      } else {
        req.denomination = denomination;
      }
      if (giftMessage.trim()) req.gift_message = giftMessage.trim();
      say(`creating test order: ${productId} · ${isRange ? rangeValue + ' (range)' : denomination} · ${paymentMethod}`);
      const r = await adminTestPurchase(req);
      setQuote(r);
      say(`order created: ${r.quote_id} — status ${r.status}`, 'ok');
      say(
        `pay exactly: ${r.coin_amount} ${r.coin} (${r.network}) → ${
          String(r.wallet_address || '').length > 24
            ? String(r.wallet_address).slice(0, 12) + '…' + String(r.wallet_address).slice(-8)
            : r.wallet_address
        }`
      );
      toast('Test order created (simulated supplier)', 'success');
    } catch (e) {
      setErr((e as Error).message || 'Create failed');
      say(`create failed: ${(e as Error).message}`, 'err');
    } finally {
      setBusy(false);
    }
  };

  // Poll the async settlement markers once the order is fulfilled: the gift
  // email dispatch AND the cashback row walking the real queue
  // (queued → sending → broadcast → paid, simulated TESTTX payout).
  const watchSettlement = (quoteId: string) => {
    if (pollRef.current) window.clearInterval(pollRef.current);
    const started = Date.now();
    let prevCashback = '';
    pollRef.current = window.setInterval(async () => {
      try {
        const q = await adminTestQuote(quoteId);
        setQuote((cur: any) => (cur ? { ...cur, ...q } : q));
        const cbStatus = String(q.cashback?.status || '');
        if (cbStatus && cbStatus !== prevCashback) {
          prevCashback = cbStatus;
          const amount = q.cashback.amount_nim ? ` (${q.cashback.amount_nim} NIM)` : '';
          say(`cashback: ${cbStatus}${amount}${q.cashback.tx_hash ? ' · ' + q.cashback.tx_hash : ''}`, cbStatus === 'paid' ? 'ok' : 'info');
        }
        const giftDone = !!q.gift_notified_at || q.gift_channel !== 'email';
        const cbDone = !q.cashback || ['paid', 'skipped', 'failed'].includes(String(q.cashback.status));
        if (giftDone && cbDone) {
          if (q.gift_notified_at) say(`gift email dispatched via Mailtrap ✓ (${new Date(q.gift_notified_at).toLocaleTimeString()})`, 'ok');
          if (pollRef.current) window.clearInterval(pollRef.current);
        } else if (Date.now() - started > 60000) {
          say('settlement markers not all seen in 60s — check Mailtrap logs / backend log', 'err');
          if (pollRef.current) window.clearInterval(pollRef.current);
        }
      } catch {
        /* transient poll failure — keep trying until the timeout */
      }
    }, 1500);
  };

  const pay = async (action: string) => {
    if (!quote?.quote_id) return;
    setErr('');
    setBusy(true);
    try {
      say(`test-pay (${action})…`);
      const r = await adminTestPay({ quote_id: quote.quote_id, action });
      setQuote((cur: any) => ({ ...cur, ...r }));
      say(`applied: ${(r.applied || []).join(' → ')} — status ${r.status}`, r.status === 'fulfilled' ? 'ok' : 'info');
      if (r.status === 'fulfilled') {
        const code = Array.isArray(r.fulfillment) && r.fulfillment[0]?.code ? r.fulfillment[0].code : '';
        if (code) say(`simulated delivery code: ${code}`, 'ok');
        if (r.gift_channel === 'email' || r.cashback) watchSettlement(r.quote_id);
        else say('no gift note attached — no email will be sent', 'info');
        toast('Test order fulfilled', 'success');
      }
    } catch (e) {
      setErr((e as Error).message || 'test-pay failed');
      say(`test-pay failed: ${(e as Error).message}`, 'err');
    } finally {
      setBusy(false);
    }
  };

  const statusChip = (s?: string) => {
    // Text-only state tokens: --green/--stamp as text on the chip surface are
    // 4.85:1 / 3.79:1 — the latter fails AA (this is the admin status chip).
    const tone =
      s === 'fulfilled' ? 'var(--text-ok, #2F5540)' : s === 'awaiting_payment' ? 'var(--text-alert, #A83A16)' : 'var(--ink, #333)';
    return (
      <span className="chip" style={{ fontWeight: 800, color: tone, borderColor: tone }}>
        {s || '—'}
      </span>
    );
  };

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="shopping-bag" size={14} />
        <span>Test center — buy & fake-pay like a customer</span>
      </div>
      <div className="small muted mb-2">
        Real catalog, real checkout pipeline, real state machine, real Mailtrap email — and the cashback engine
        and wallet memo all RUN through the same pipeline. Only the payments are simulated: the purchase,
        the cashback payout (<span className="mono">TESTTX-…</span>) and the memo broadcast. The only real side effect
        is the gift email to the address below.
      </div>

      <div className="row" style={{ gap: '10px', flexWrap: 'wrap' }}>
        <div className="field" style={{ minWidth: 130 }}>
          <label>Country</label>
          <select className="input" value={country} onChange={(e) => setCountry(e.target.value)}>
            {COUNTRY_OPTIONS.map(([code, name]) => (
              <option key={code} value={code}>
                {code} — {name}
              </option>
            ))}
          </select>
        </div>
        <div className="field" style={{ flex: 1, minWidth: 220 }}>
          <label>Product {loadingProducts ? '(loading…)' : `(${products.length})`}</label>
          <select className="input" value={productId} onChange={(e) => pickProduct(e.target.value)}>
            <option value="">— pick a product —</option>
            {products.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name} ({countryName(p.country) || p.country})
              </option>
            ))}
          </select>
        </div>
        <div className="field" style={{ minWidth: 170 }}>
          <label>Denomination</label>
          <select className="input" value={denomination} onChange={(e) => setDenomination(e.target.value)} disabled={!pkgs.length}>
            <option value="">— pick —</option>
            {pkgs.map((p) => (
              <option key={p.denomination} value={p.denomination}>
                {p.denomination}
              </option>
            ))}
          </select>
        </div>
        {isRange ? (
          <div className="field" style={{ minWidth: 120 }}>
            <label>Amount</label>
            <input className="input" type="number" min={1} placeholder="e.g. 25" value={rangeValue} onChange={(e) => setRangeValue(e.target.value)} />
          </div>
        ) : null}
        <div className="field" style={{ minWidth: 90 }}>
          <label>Qty</label>
          <input className="input" type="number" min={1} max={3} value={quantity} onChange={(e) => setQuantity(Math.max(1, Math.min(3, Number(e.target.value) || 1)))} />
        </div>
      </div>

      <div className="row" style={{ gap: '10px', flexWrap: 'wrap', marginTop: '8px' }}>
        <div className="field" style={{ flex: 1, minWidth: 220 }}>
          <label>Recipient email (delivery + gift note)</label>
          <input className="input" type="email" placeholder="friend@example.com" autoComplete="off" value={email} onChange={(e) => setEmail(e.target.value)} />
        </div>
        <div className="field" style={{ flex: 1, minWidth: 220 }}>
          <label>Gift message (optional)</label>
          <input className="input" type="text" placeholder="Happy birthday! 🎂" value={giftMessage} onChange={(e) => setGiftMessage(e.target.value)} />
        </div>
      </div>

      <div className="row" style={{ gap: '14px', flexWrap: 'wrap', marginTop: '8px', alignItems: 'center' }}>
        <div className="field" style={{ margin: 0 }}>
          <label>Payment rail</label>
          <div className="row" style={{ gap: '8px' }}>
            {[
              { id: 'nimiq_pay', label: '⚡ Pay with NIM · BTC Lightning' },
              { id: 'usdt_polygon', label: '💵 USDT (Polygon)' },
            ].map((m) => (
              <label
                key={m.id}
                style={{ display: 'inline-flex', alignItems: 'center', gap: '6px', padding: '8px 12px', border: '2px solid var(--line-strong)', borderRadius: 'var(--r-s)', cursor: 'pointer', fontWeight: 800, fontSize: '0.85rem', background: paymentMethod === m.id ? 'var(--surface-2, #f3ecd9)' : 'var(--surface-1)' }}
              >
                <input type="radio" name="tc-method" value={m.id} checked={paymentMethod === m.id} onChange={() => setPaymentMethod(m.id)} style={{ accentColor: 'var(--stamp)' }} />
                {m.label}
              </label>
            ))}
          </div>
        </div>
        <div className="field" style={{ margin: 0, minWidth: 140 }}>
          <label>Cashback destination</label>
          <select className="input" value={cashbackDestination} onChange={(e) => setCashbackDestination(e.target.value)}>
            <option value="cashback">Buyer wallet</option>
            <option value="burn">Burn 🔥</option>
          </select>
        </div>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: '6px', fontWeight: 800, fontSize: '0.85rem', cursor: 'pointer', marginTop: '14px' }}>
          <input type="checkbox" checked={anonymous} onChange={(e) => setAnonymous(e.target.checked)} style={{ accentColor: 'var(--stamp)' }} />
          Anonymous purchase
        </label>
      </div>

      {err ? (
        <div className="alert error mt-1" style={{ marginBottom: 0 }}>
          <Icon name="alert" size={16} />
          <div className="small">{err}</div>
        </div>
      ) : null}

      <button className="btn btn-gold btn-block btn-lg mt-2" disabled={busy || !productId} onClick={createOrder}>
        <Icon name="shopping-bag" size={16} />
        <span className="btn-label">{busy ? 'Working…' : 'Create test order'}</span>
      </button>

      {quote ? (
        <div className="mt-2" style={{ border: '1px solid var(--line-strong)', borderRadius: 'var(--r-s)', padding: '14px 16px', background: 'var(--surface-1)' }}>
          <div className="row between" style={{ flexWrap: 'wrap', gap: '8px' }}>
            <div className="strong" style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
              {statusChip(quote.status)} <span className="xs faint">{quote.quote_id}</span>
            </div>
            <div className="xs faint">simulated supplier · {quote.supplier_order_id}</div>
          </div>
          <div className="kv mt-1 small">
            <div>
              <b>Pay:</b> {quote.coin_amount} {quote.coin} · {quote.network}
            </div>
            <div style={{ wordBreak: 'break-all' }}>
              <b>To:</b> {quote.wallet_address || quote.lightning_invoice || '—'}
            </div>
            {quote.estimated_nim ? (
              <div>
                <b>≈ NIM:</b> {quote.estimated_nim}
              </div>
            ) : null}
            {quote.payment_expires_at ? (
              <div>
                <b>Expires:</b> {new Date(quote.payment_expires_at).toLocaleTimeString()}
              </div>
            ) : null}
            {quote.cashback ? (
              <div>
                <b>Cashback:</b> {quote.cashback.status}
                {quote.cashback.amount_nim ? ` · ${quote.cashback.amount_nim} NIM` : ''}
                {quote.cashback.destination === 'burn' ? ' · 🔥 burn' : ''}
                {quote.cashback.tx_hash ? ` · ${String(quote.cashback.tx_hash).slice(0, 16)}` : ''}
                {quote.cashback.skip_reason ? ` (${quote.cashback.skip_reason})` : ''}
                {quote.cashback.test_mode && quote.cashback.status === 'paid' ? ' · simulated payout ✓' : ''}
              </div>
            ) : null}
            {quote.gift_notified_at ? (
              <div style={{ color: 'var(--green-ink, #1a7f37)' }}>
                <b>Gift email:</b> sent {new Date(quote.gift_notified_at).toLocaleTimeString()} ✓
              </div>
            ) : quote.gift_channel === 'email' ? (
              <div className="muted">Gift email: pending (fires on fulfillment)…</div>
            ) : (
              <div className="muted">No gift note attached.</div>
            )}
          </div>

          <div className="row mt-2" style={{ gap: '8px', flexWrap: 'wrap' }}>
            <button className="btn btn-gold" disabled={busy} onClick={() => pay('auto')}>
              <Icon name="zap" size={14} />
              <span className="btn-label">🚀 Auto-pay (simulate full payment)</span>
            </button>
            <button className="btn btn-ghost btn-sm" disabled={busy} onClick={() => pay('broadcast')}>
              1 · Broadcast
            </button>
            <button className="btn btn-ghost btn-sm" disabled={busy} onClick={() => pay('confirm')}>
              2 · Confirm
            </button>
            <button className="btn btn-ghost btn-sm" disabled={busy} onClick={() => pay('deliver')}>
              3 · Deliver
            </button>
          </div>
          {Array.isArray(quote.fulfillment) && quote.fulfillment[0]?.code ? (
            <div className="mt-2 small" style={{ fontFamily: 'ui-monospace, monospace', background: 'var(--surface-2, #f6f0e0)', padding: '8px 12px', borderRadius: 'var(--r-s)' }}>
              delivery code: <b>{quote.fulfillment[0].code}</b> (pin {quote.fulfillment[0].pin || '—'})
            </div>
          ) : null}
        </div>
      ) : null}

      {log.length ? (
        <div className="mt-2" style={{ background: 'var(--console-bg, #2f2a24)', color: 'var(--console-ink, #e9dec3)', borderRadius: 'var(--r-s)', padding: '10px 14px', fontFamily: 'ui-monospace, monospace', fontSize: '0.78rem', lineHeight: 1.7, maxHeight: 220, overflow: 'auto' }}>
          {log.map((l, i) => (
            <div key={i} style={{ color: l.tone === 'ok' ? 'var(--console-ok, #7bd88f)' : l.tone === 'err' ? 'var(--console-err, #ff8f6b)' : undefined }}>
              <span className="faint">[{l.at}]</span> {l.text}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}
