/**
 * AdminTestCenterPanel.tsx — operator sandbox for the real order email.
 *
 * Buys one or more REAL catalogue products through the same checkout pipeline
 * a shopper uses (validation, live supplier price, catalogue rules, quote,
 * payment attach), then drives each test order to "fulfilled" through the same
 * state machine the tracker uses. That fires the genuine order / gift email
 * (Mailtrap) to the address you type. No money moves and the supplier is never
 * asked to fulfil anything: the payment and the delivery are simulated.
 */
import { useState } from 'react';
import { Icon } from '../ui/Icon';
import { AlertBox } from '../ui/uiKit';
import { useToast } from '../AppProviders';
import {
  adminTestPurchase,
  adminTestPay,
  adminTestQuote,
  getProduct,
  searchProducts,
} from '../../lib/api';
import { productMoney, positiveAmount } from '../../lib/productMoney';

type Pkg = { key: string; denomination: string; label: string; value: number; currency: string };
type RangeOpt = { min: number; max: number; step: number; currency: string };

type Picked = { family: string; name: string; kind: string; country: string };

type Detail = { packages: Pkg[]; range: RangeOpt | null; phone: boolean };

type QueueItem = {
  id: number;
  family: string;
  name: string;
  country: string;
  denomination: string; // exact supplier label, or "range"
  productValue: number; // only for range products
  quantity: number;
  label: string;
};

type RunRow = {
  id: number;
  label: string;
  state: 'running' | 'done' | 'failed';
  quoteId?: string;
  status?: string;
  email?: 'sent' | 'not_sent';
  emailAt?: string;
  cashback?: string;
  error?: string;
};

// The only payment rail: Nimiq Pay (BTC Lightning). USDT-on-Polygon was removed.
const PAYMENT_METHOD = 'nimiq_pay';

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

/** Turns the catalogue payload (a family object or an array of families) into
 *  the packages and range a test purchase can select. by_account products are
 *  skipped, exactly like the storefront does. */
function normalizeDetail(raw: any): Detail {
  const families: any[] = Array.isArray(raw) ? raw : raw?.products ? [raw] : [];
  const packages: Pkg[] = [];
  let range: RangeOpt | null = null;
  const channels = new Set<string>();
  for (const fam of families) {
    for (const p of fam?.products || []) {
      const dt = String(p?.delivery_type || fam?.delivery_type || '').toLowerCase();
      if (dt === 'by_account') continue;
      if (dt) channels.add(dt);
      if (p?.range) {
        range = {
          min: Number(p.range.min) || 0,
          max: Number(p.range.max) || 0,
          step: Number(p.range.step_size || p.range.step) || 1,
          currency: String(p.range.currency || 'USD').toUpperCase(),
        };
        continue;
      }
      const denomination = String(p?.denomination || p?.localized_denomination || p?.product_id || '').trim();
      if (!denomination) continue;
      const money = productMoney(p);
      packages.push({
        key: `${p?.product_id || denomination}`,
        denomination,
        label: String(p?.localized_denomination || denomination),
        value: money.value,
        currency: money.currency || String(fam?.currency || 'USD').toUpperCase(),
      });
    }
  }
  const phone = channels.size > 0 && Array.from(channels).every((c) => c === 'by_phone');
  return { packages, range, phone };
}

function describeError(e: unknown): string {
  return (e as any)?.message || 'Request failed';
}

export function AdminTestCenterPanel() {
  const { toast } = useToast();
  const [country, setCountry] = useState('US');
  const [query, setQuery] = useState('');
  const [searching, setSearching] = useState(false);
  const [results, setResults] = useState<Array<{ family: string; kind: string; category: string }>>([]);
  const [picked, setPicked] = useState<Picked | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [chosenKey, setChosenKey] = useState('');
  const [rangeValue, setRangeValue] = useState('');
  const [quantity, setQuantity] = useState(1);

  const [email, setEmail] = useState('');
  const [phone, setPhone] = useState('');
  const [giftMessage, setGiftMessage] = useState('');

  const [queue, setQueue] = useState<QueueItem[]>([]);
  const [running, setRunning] = useState(false);
  const [rows, setRows] = useState<RunRow[]>([]);
  const [err, setErr] = useState('');
  const [nextId, setNextId] = useState(1);

  const countryCode = country.trim().toUpperCase();

  const doSearch = async () => {
    setErr('');
    const q = query.trim();
    if (!q) {
      setErr('Type a product name to search (e.g. Amazon).');
      return;
    }
    if (!/^[A-Z]{2}$/.test(countryCode)) {
      setErr('Country must be a 2-letter code (e.g. US, TR).');
      return;
    }
    setSearching(true);
    try {
      const list = await searchProducts(q, countryCode);
      const rowsOut = (Array.isArray(list) ? list : []).slice(0, 30).map((r: any) => ({
        family: String(r.family || r.name || ''),
        kind: String(r.kind || ''),
        category: String(r.category || ''),
      })).filter((r) => r.family);
      setResults(rowsOut);
      if (!rowsOut.length) setErr(`No products matched "${q}" in ${countryCode}.`);
    } catch (e) {
      setErr(describeError(e));
    } finally {
      setSearching(false);
    }
  };

  const pickProduct = async (family: string, kind: string) => {
    setErr('');
    setPicked({ family, name: family, kind, country: countryCode });
    setDetail(null);
    setChosenKey('');
    setRangeValue('');
    setLoadingDetail(true);
    try {
      const raw = await getProduct(family, countryCode, { force: true });
      const d = normalizeDetail(raw);
      if (!d.packages.length && !d.range) {
        setErr(`${family} has no purchasable denominations in ${countryCode}.`);
        setDetail(null);
        return;
      }
      setDetail(d);
      if (d.packages.length) setChosenKey(d.packages[0].key);
      else if (d.range) setRangeValue(String(d.range.min));
    } catch (e) {
      setErr(describeError(e));
    } finally {
      setLoadingDetail(false);
    }
  };

  const selectedPackage = detail?.packages.find((p) => p.key === chosenKey) || null;

  /** The selection as the test-purchase endpoint expects it, or an error. */
  const currentSelection = (): { denomination: string; productValue: number; label: string; currency: string } | string => {
    if (!picked || !detail) return 'Pick a product first.';
    if (selectedPackage) {
      return {
        denomination: selectedPackage.denomination,
        productValue: 0,
        label: `${picked.name} · ${selectedPackage.label}`,
        currency: selectedPackage.currency,
      };
    }
    if (detail.range) {
      const v = positiveAmount(rangeValue);
      if (!v) return 'Enter a positive amount for this range product.';
      if (v < detail.range.min || v > detail.range.max) {
        return `Amount must be between ${detail.range.min} and ${detail.range.max} ${detail.range.currency}.`;
      }
      return {
        denomination: 'range',
        productValue: v,
        label: `${picked.name} · ${v} ${detail.range.currency}`,
        currency: detail.range.currency,
      };
    }
    return 'Pick a denomination.';
  };

  const addToQueue = () => {
    setErr('');
    const sel = currentSelection();
    if (typeof sel === 'string') {
      setErr(sel);
      return;
    }
    const q = Math.max(1, Math.min(5, Math.floor(quantity) || 1));
    setQueue((cur) => [
      ...cur,
      {
        id: nextId,
        family: picked!.family,
        name: picked!.name,
        country: picked!.country,
        denomination: sel.denomination,
        productValue: sel.productValue,
        quantity: q,
        label: sel.label + (q > 1 ? ` × ${q}` : ''),
      },
    ]);
    setNextId((n) => n + 1);
  };

  const removeFromQueue = (id: number) => setQueue((cur) => cur.filter((x) => x.id !== id));

  const needsPhone = detail?.phone ?? false;
  const emailOk = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim());

  const runQueue = async () => {
    setErr('');
    if (!queue.length) {
      setErr('Add at least one product to the queue.');
      return;
    }
    // Contact check only: a top-up is delivered to a phone, everything else to
    // an email. The backend enforces the exact rule per product and reports it.
    if (!emailOk && !phone.trim()) {
      setErr('Enter a recipient email (or a phone number for top-ups).');
      return;
    }
    setRunning(true);
    setRows([]);
    for (const item of queue) {
      const rowId = item.id;
      setRows((cur) => [...cur, { id: rowId, label: item.label, state: 'running' }]);
      const patch = (p: Partial<RunRow>) => setRows((cur) => cur.map((r) => (r.id === rowId ? { ...r, ...p } : r)));
      try {
        const buy = await adminTestPurchase({
          product_id: item.family,
          country: item.country,
          denomination: item.denomination,
          product_value: item.productValue || undefined,
          quantity: item.quantity,
          email: email.trim(),
          phone_number: phone.trim() || undefined,
          gift_message: giftMessage.trim() || undefined,
          payment_method: PAYMENT_METHOD,
          cashback_destination: 'cashback',
        });
        const quoteId = String(buy.quote_id || buy.id || '');
        if (!quoteId) throw new Error('The test order was created without an id.');
        patch({ quoteId });

        // The payment ladder ends in the fulfilment hook, which sends the mail
        // in the background; poll the quote until the mail is marked sent.
        let view: any = await adminTestPay(quoteId, 'auto');
        for (let i = 0; i < 12 && !view?.gift_notified; i++) {
          await sleep(1000);
          view = await adminTestQuote(quoteId);
        }
        const sent = view?.gift_notified === true;
        const sentAt = sent && view?.gift_notified_at ? String(view.gift_notified_at) : '';
        const cb = view?.cashback;
        patch({
          state: 'done',
          status: String(view?.status || ''),
          email: sent ? 'sent' : 'not_sent',
          emailAt: sentAt,
          cashback: cb ? `${cb.status}${cb.amount_nim ? ` · ${cb.amount_nim} NIM` : ''}` : '',
        });
      } catch (e) {
        patch({ state: 'failed', error: describeError(e) });
      }
    }
    setRunning(false);
    toast('Test purchases finished — check the results below.', 'success');
  };

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="package" size={14} />
        <span>Test purchases (real products, real order email)</span>
      </div>
      <div className="small muted mb-2">
        Buys a real catalogue product through the same checkout a shopper uses, then completes it so the genuine order email is sent
        to the address you enter. No money moves and nothing is fulfilled by the supplier. The mail goes out through the configured
        Mailtrap transport (sandbox or live, see the Mailtrap status above).
      </div>

      <div className="field">
        <label>Country (2-letter code)</label>
        <input className="input" type="text" maxLength={2} value={country} onChange={(e) => setCountry(e.target.value)} style={{ maxWidth: 120 }} />
      </div>

      <div className="field">
        <label>Search a product</label>
        <div className="row" style={{ gap: 8 }}>
          <input
            className="input"
            type="text"
            placeholder="e.g. Amazon, Steam, Netflix"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') doSearch(); }}
          />
          <button className="btn" type="button" disabled={searching} onClick={doSearch}>
            <Icon name="search" size={14} />
            <span className="btn-label">{searching ? 'Searching…' : 'Search'}</span>
          </button>
        </div>
      </div>

      {results.length ? (
        <div className="field">
          <label>Results</label>
          <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
            {results.map((r) => (
              <button
                key={r.family + r.kind}
                type="button"
                className={'chip' + (picked?.family === r.family ? ' is-active' : '')}
                onClick={() => pickProduct(r.family, r.kind)}
                style={{ cursor: 'pointer' }}
              >
                <span className="strong">{r.family}</span>
                {r.kind ? <span className="xs"> · {r.kind}</span> : null}
              </button>
            ))}
          </div>
        </div>
      ) : null}

      {picked ? (
        <div className="field">
          <label>Denomination for {picked.name} ({picked.country})</label>
          {loadingDetail ? <div className="small muted">Loading denominations…</div> : null}
          {detail && detail.packages.length ? (
            <select className="input" value={chosenKey} onChange={(e) => setChosenKey(e.target.value)}>
              {detail.packages.map((p) => (
                <option key={p.key} value={p.key}>
                  {p.label}{p.value ? ` (${p.value} ${p.currency})` : ''}
                </option>
              ))}
            </select>
          ) : null}
          {detail && detail.range && !detail.packages.length ? (
            <div className="row" style={{ gap: 8, alignItems: 'center' }}>
              <input
                className="input"
                type="number"
                min={detail.range.min}
                max={detail.range.max}
                step={detail.range.step}
                value={rangeValue}
                onChange={(e) => setRangeValue(e.target.value)}
                style={{ maxWidth: 180 }}
              />
              <span className="small muted">{detail.range.currency} · {detail.range.min} – {detail.range.max}</span>
            </div>
          ) : null}
          {detail && detail.packages.length && detail.range ? (
            <div className="small muted">This product also has a free-amount range; pick a denomination above, or buy it as a range from the storefront.</div>
          ) : null}
          <div className="row" style={{ gap: 8, marginTop: 8, alignItems: 'center' }}>
            <span className="small">Qty</span>
            <input className="input" type="number" min={1} max={5} value={quantity} onChange={(e) => setQuantity(Number(e.target.value))} style={{ maxWidth: 90 }} />
            <button className="btn" type="button" disabled={!detail} onClick={addToQueue}>
              <Icon name="plus" size={14} />
              <span className="btn-label">Add to test queue</span>
            </button>
          </div>
        </div>
      ) : null}

      <div className="field">
        <label>Test queue ({queue.length})</label>
        {queue.length ? (
          <ul className="small" style={{ margin: 0, paddingLeft: 18 }}>
            {queue.map((q) => (
              <li key={q.id} style={{ marginBottom: 4 }}>
                {q.label} <span className="xs faint">· {q.country}</span>{' '}
                <button type="button" className="btn" style={{ padding: '2px 8px', marginLeft: 6 }} onClick={() => removeFromQueue(q.id)} disabled={running}>
                  Remove
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <div className="small muted">Nothing queued. Search a product, choose a denomination, then add it.</div>
        )}
      </div>

      <div className="field">
        <label>Recipient email {needsPhone ? '(optional for top-ups)' : ''}</label>
        <input className="input" type="email" placeholder="you@example.com" autoComplete="off" value={email} onChange={(e) => setEmail(e.target.value)} />
      </div>
      <div className="field">
        <label>Phone number {needsPhone ? '(required for top-ups)' : '(optional, E.164)'}</label>
        <input className="input" type="tel" placeholder="+905551234567" autoComplete="off" value={phone} onChange={(e) => setPhone(e.target.value)} />
      </div>
      <div className="field">
        <label>Gift message (optional, makes it a gift email)</label>
        <input className="input" type="text" placeholder="Happy birthday!" value={giftMessage} onChange={(e) => setGiftMessage(e.target.value)} />
      </div>
      <div className="field">
        <label>Payment rail</label>
        <div className="small muted">Nimiq Pay (BTC Lightning)</div>
      </div>

      {err ? <AlertBox type="error">{err}</AlertBox> : null}

      <button className="btn btn-gold btn-block btn-lg mt-2" type="button" disabled={running || !queue.length} onClick={runQueue}>
        <Icon name="send" size={16} />
        <span className="btn-label">{running ? 'Running test purchases…' : `Run ${queue.length || ''} test purchase${queue.length === 1 ? '' : 's'}`}</span>
      </button>

      {rows.length ? (
        <div className="mt-2">
          <div className="card-title" style={{ fontSize: '0.95rem' }}>Results</div>
          <table className="table small" style={{ width: '100%' }}>
            <thead>
              <tr>
                <th align="left">Product</th>
                <th align="left">Order</th>
                <th align="left">Status</th>
                <th align="left">Email</th>
                <th align="left">Cashback</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id}>
                  <td>{r.label}</td>
                  <td className="xs">{r.quoteId ? r.quoteId.slice(0, 10) : '—'}</td>
                  <td>{r.state === 'running' ? 'working…' : r.state === 'failed' ? 'failed' : r.status || '—'}</td>
                  <td>
                    {r.email === 'sent'
                      ? `sent ${r.emailAt ? new Date(r.emailAt).toLocaleTimeString() : ''}`.trim()
                      : r.email === 'not_sent'
                        ? 'NOT sent — mail transport is off or failed; check the server log for "mailtrap:"'
                        : '—'}
                  </td>
                  <td>{r.cashback || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.filter((r) => r.error).map((r) => (
            <AlertBox key={'e' + r.id} type="error">{r.label}: {r.error}</AlertBox>
          ))}
        </div>
      ) : null}
    </div>
  );
}
