/**
 * PlantTreesPage.tsx — the public transparency page for cashback-driven tree
 * planting. Fully in English. Shows: aggregate stats, signed-in user's tree
 * count with rich share cards (Web Share, X/Twitter, Facebook, copy link),
 * weekly/monthly/all-time leaderboard, and every month's Polygon-settlement
 * transaction with a direct Polygonscan link for audit.
 *
 * Theme + icons: uses the site's kraft/paper tokens (.pt-* in fixes.css) and
 * the Lucide icon set via <Icon>. No emoji placeholders. NIM payers earn the
 * FULL cashback rate; USDT (Polygon) payers earn the configured share (default
 * 50%) OF the NIM rate — that distinction is stated explicitly so the page
 * never over-promises cashback.
 */
import { useEffect, useMemo, useRef, useState } from 'react';
import { Icon } from '../ui/Icon';
import { Identicon } from '../ui/Identicon';
import { ErrorState } from '../ui/uiKit';
import { canonicalIdenticonInput, resolveIdenticonUrl } from '../../lib/identicon';
import { AppRoot } from '../AppRoot';
import { api, getTrees, getMyTrees, getNimRate, cachedNimRate, getSiteConfig } from '../../lib/api';
import { siteName, siteURL } from '../../lib/config';
import { getAddress, isAuthed } from '../../lib/session';
import { Clipboard } from '../../lib/clipboard';
import { fmtNIM } from '../../lib/format';
import { useT } from '../../i18n';
import { asset, pagePath } from '../../lib/asset';

type LeaderRow = { rank: number; user: string; trees: number; planted?: number; orders: number };
type Settlement = {
  id: string; month_bucket: string; amount_usdt: number; amount_label?: string; amount_value?: string; amount_nim?: number; wallet_nim?: number; trees_planted: number;
  status?: 'paid'|'skipped'; proof_images?: { data: string; caption?: string }[];
  tx_hash?: string; polygonscan_url?: string; created_at?: string;
  from_address?: string; to_address?: string; note?: string;
};

// Environmental constants from OneTreePlanted / EPA averages.
const CO2_PER_TREE_KG_PER_YEAR = 22; // ~48 lbs CO2 absorbed per mature tree/year
const O2_PER_TREE_KG_PER_YEAR = 118; // ~260 lbs O2 produced per tree/year

const RANK_CLS = ['gold', 'silver', 'bronze'];

/** A tree count you can put on a share card: at most one decimal, no trailing
 *  ".0", and whole numbers for anything >= 10 so claims stay credible. */
function fmtTreesUser(n: number): string {
  if (!isFinite(n) || n <= 0) return '0';
  if (n >= 10) return String(Math.round(n));
  const v = Math.round(n * 10) / 10;
  return Number.isInteger(v) ? String(v) : v.toFixed(1);
}

const TREE_DONATION_BALANCE_CACHE_KEY = 'nimshop:tree-donation-balance:v1';
const TREE_DONATION_BALANCE_CACHE_TTL_MS = 60 * 1000;
const TREE_DONATION_BALANCE_STALE_MS = 5 * 60 * 1000;

type CachedTreeDonationBalance = {
  balance_nim: number;
  cached_at?: string;
  stale?: boolean;
  client_cached_at: number;
};

function readCachedTreeDonationBalance(): CachedTreeDonationBalance | null {
  try {
    const raw = sessionStorage.getItem(TREE_DONATION_BALANCE_CACHE_KEY);
    if (!raw) return null;
    const row = JSON.parse(raw) as Partial<CachedTreeDonationBalance>;
    if (!Number.isFinite(Number(row.balance_nim)) || !Number.isFinite(Number(row.client_cached_at))) return null;
    if (Date.now() - Number(row.client_cached_at) > TREE_DONATION_BALANCE_STALE_MS) return null;
    return { ...row, balance_nim: Number(row.balance_nim), client_cached_at: Number(row.client_cached_at) } as CachedTreeDonationBalance;
  } catch {
    return null;
  }
}

/**
 * A rounded-rectangle PATH — the caller decides whether to fill or stroke it.
 *
 * The logo fallback below called a `roundRect` that existed nowhere in the
 * bundle, so the moment the wordmark image failed to load the certificate
 * canvas threw `ReferenceError: roundRect is not defined` and the buyer got no
 * image at all. `CanvasRenderingContext2D.roundRect()` is deliberately NOT
 * used: it is newer than some of the WebViews this page runs in.
 */
export function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number): void {
  const rad = Math.max(0, Math.min(r, w / 2, h / 2));
  ctx.beginPath();
  ctx.moveTo(x + rad, y);
  ctx.lineTo(x + w - rad, y);
  ctx.arcTo(x + w, y, x + w, y + rad, rad);
  ctx.lineTo(x + w, y + h - rad);
  ctx.arcTo(x + w, y + h, x + w - rad, y + h, rad);
  ctx.lineTo(x + rad, y + h);
  ctx.arcTo(x, y + h, x, y + h - rad, rad);
  ctx.lineTo(x, y + rad);
  ctx.arcTo(x, y, x + rad, y, rad);
  ctx.closePath();
}

/** Renders a translated string whose **marked** parts render bold. */
function rich(value: string) {
  return value.split('**').map((part, i) => (i % 2 ? <strong key={i}>{part}</strong> : part));
}

export function PlantTreesPage() {
  return <AppRoot activeKey="plant-trees"><PlantTreesView /></AppRoot>;
}

/** Content-only view, rendered inside the persistent shell by the SPA router.
 *  PlantTreesPage wraps it in <AppRoot> for a full page load. */
export function PlantTreesView() {
  const { t } = useT();
  const [cfg, setCfg] = useState<any>(null);
  const [bucket, setBucket] = useState<'week' | 'month' | 'all'>('all');
  const [data, setData] = useState<any>(null);
  const [me, setMe] = useState<any>(null);
  const [loading, setLoading] = useState(true);
  // The public tree totals could not be read (API down / 5xx). Shown as an
  // error with retry instead of a page of invented zeros.
  const [loadFailed, setLoadFailed] = useState(false);
  const [reloadKey, setReloadKey] = useState(0);
  const [copied, setCopied] = useState(false);
  const [address, setAddress] = useState<string>('');
  const [settlePage, setSettlePage] = useState(0);
  const [leaderPage, setLeaderPage] = useState(0);
  const [shareImg, setShareImg] = useState<string | null>(null);
  // The PNG itself. Kept so Share can hand the File to the OS directly —
  // fetch()ing the blob:/data: URL back is blocked by the page CSP
  // (connect-src has no blob:/data:), which silently broke image sharing.
  const shareBlobRef = useRef<Blob | null>(null);
  const [treeAddressCopied, setTreeAddressCopied] = useState(false);
  const [, setNimUsdPrice] = useState<number>(() => Number(cachedNimRate()?.usd_per_nim) || 0);
  const [walletNim, setWalletNim] = useState<number | null>(() => readCachedTreeDonationBalance()?.balance_nim ?? null);
  const [walletBalanceStale, setWalletBalanceStale] = useState(false);

  const treeDonationAddress = String(cfg?.tree_donation_nim_address || '').trim();
  const copyTreeAddress = () => {
    if (!treeDonationAddress) return;
    if (Clipboard.copy(treeDonationAddress)) {
      setTreeAddressCopied(true);
      setTimeout(() => setTreeAddressCopied(false), 2200);
    }
  };

  useEffect(() => {
    if (isAuthed()) setAddress(getAddress() || '');
  }, []);

  // The wallet card must state the live conversion used by the $5 rollover
  // rule. This is best-effort and never blocks the transparency page.
  useEffect(() => {
    let alive = true;
    getNimRate()
      .then((rate) => {
        const next = Number(rate?.usd_per_nim) || 0;
        if (alive && next > 0) setNimUsdPrice(next);
      })
      .catch(() => {
        // Keep a usable session-cached price when the market feed is warming up.
      });
    return () => { alive = false; };
  }, []);

  // Read the public donation wallet balance through the same-origin API. The
  // browser cache keeps it visible across quick page visits; the server also
  // caches the chain read for 60 seconds and can serve a stale last-known value.
  useEffect(() => {
    let alive = true;
    const cached = readCachedTreeDonationBalance();
    if (cached) {
      setWalletNim(cached.balance_nim);
      setWalletBalanceStale(Boolean(cached.stale));
      if (Date.now() - cached.client_cached_at < TREE_DONATION_BALANCE_CACHE_TTL_MS) return () => { alive = false; };
    }

    // Through api(): on GitHub Pages the backend lives on API_BASE, so a bare
    // same-origin '/api/…' would hit github.io and 404.
    api('/trees/donation-balance')
      .then((row) => {
        const balance = Number(row?.balance_nim);
        if (!alive || row?.available !== true || !Number.isFinite(balance) || balance < 0) return;
        const next: CachedTreeDonationBalance = {
          balance_nim: balance,
          cached_at: row.cached_at,
          stale: Boolean(row.stale),
          client_cached_at: Date.now(),
        };
        try {
          sessionStorage.setItem(TREE_DONATION_BALANCE_CACHE_KEY, JSON.stringify(next));
        } catch {
          /* sessionStorage can be unavailable in privacy mode; keep memory value */
        }
        setWalletNim(balance);
        setWalletBalanceStale(Boolean(row.stale));
      })
      .catch(() => {
        // A cached balance, if present, remains visible; do not invent a zero.
      });

    return () => { alive = false; };
  }, []);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const [c, treesRes, m] = await Promise.all([
          getSiteConfig().catch(() => ({})) as any,
          getTrees(bucket) as any,
          getMyTrees().catch(() => null) as any,
        ]);
        if (!alive) return;
        setCfg(c);
        setData(treesRes);
        setMe(m);
        setLoadFailed(false);
      } catch {
        // Was an unhandled rejection (try/finally without catch) whenever
        // /api/trees failed; a cached `data` from an earlier bucket stays.
        if (alive) setLoadFailed(true);
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => { alive = false; };
  }, [bucket, reloadKey]);

  const totals = data?.totals || { total_trees: 0, total_usd: 0, funded_trees: 0, planted_trees: 0, pending_trees: 0, total_nim: 0, orders: 0 };
  // "Funded" = cashback trees earmarked at fulfilment; "planted"/settled = the
  // share of real monthly payouts already sent to OneTreePlanted on-chain.
  // OneTreePlanted puts trees in the ground on ITS planting schedule (normally
  // the month after a payout), so we never claim a tree stands the moment a
  // buyer checks out.
  const totalsFunded = Number(totals.funded_trees ?? totals.total_trees ?? 0);
  const totalsPlanted = Number(totals.planted_trees ?? 0);
  const leaders: LeaderRow[] = data?.leaderboard || [];
  // Leaderboard, 5 rows per page.
  const LEADER_PER_PAGE = 5;
  const leaderPages = Math.max(1, Math.ceil(leaders.length / LEADER_PER_PAGE));
  const leaderPageSafe = Math.min(leaderPage, leaderPages - 1);
  const visibleLeaders = leaders.slice(leaderPageSafe * LEADER_PER_PAGE, leaderPageSafe * LEADER_PER_PAGE + LEADER_PER_PAGE);
  const settlements: Settlement[] = data?.settlements || [];
  // Settlements, newest month first, 3 per page (most recent 3 months shown).
  const SETTLE_PER_PAGE = 3;
  const sortedSettlements = [...settlements].sort((a, b) =>
    String(b.month_bucket || '').localeCompare(String(a.month_bucket || ''))
  );
  const settlePages = Math.max(1, Math.ceil(sortedSettlements.length / SETTLE_PER_PAGE));
  const settlePageSafe = Math.min(settlePage, settlePages - 1);
  const visibleSettlements = sortedSettlements.slice(settlePageSafe * SETTLE_PER_PAGE, settlePageSafe * SETTLE_PER_PAGE + SETTLE_PER_PAGE);
  const treesPerUsd = Number(cfg?.trees_per_usd || data?.trees_per_usd || 1);
  // USDT payers get this fraction of the (full) NIM cashback rate, e.g. 0.5 = 50%.
  const usdtMult = Number(cfg?.usdt_cashback_multiplier ?? 0.5);
  const usdtPct = Math.round(usdtMult * 100);

  const myTrees = Number(me?.user_trees || 0); // funded (earmarked)
  const myPlanted = Number(me?.user_trees_planted || 0); // settled / sent to OTP
  const myOrders = Number(me?.user_orders || 0);
  const myCO2 = myTrees * CO2_PER_TREE_KG_PER_YEAR;
  const myO2 = myTrees * O2_PER_TREE_KG_PER_YEAR;

  // Locate the signed-in buyer inside the returned leaderboard (matched by a
  // clean, spaceless address/code), so we can pin "your standing" above it.
  const cleanUser = String(address || '').replace(/[\s-]/g, '').toUpperCase();
  const myLeaderIndex = cleanUser
    ? leaders.findIndex((r) => {
        const rc = String(r.user || '').replace(/[\s-]/g, '').toUpperCase();
        return rc === cleanUser || cleanUser.startsWith(rc);
      })
    : -1;
  const myLeaderRank = myLeaderIndex >= 0 ? leaders[myLeaderIndex].rank : null;

  const totalCO2 = totalsFunded * CO2_PER_TREE_KG_PER_YEAR;
  const totalO2 = totalsFunded * O2_PER_TREE_KG_PER_YEAR;

  const shareUrl = useMemo(() => {
    if (typeof window === 'undefined') return siteURL() + '/plant-trees';
    return window.location.origin + pagePath('/plant-trees');
  }, []);

  const shareText = useMemo(() => {
    const treeStr = fmtTreesUser(myTrees);
    const co2 = myCO2 >= 1000 ? (myCO2 / 1000).toFixed(1) + ' t' : Math.round(myCO2) + ' kg';
    return t('plantTrees.shareCaption', {
      count: myTrees,
      trees: treeStr,
      co2,
      site: siteName(),
      url: shareUrl,
    });
  }, [t, myTrees, myCO2, shareUrl]);

  const nativeShare = async () => {
    if (navigator.share) {
      try { await navigator.share({ title: t('plantTrees.shareTitle', { site: siteName() }), text: shareText, url: shareUrl }); return; } catch { /* cancelled */ }
    }
  };

  // Nimiq Pay's clipboard (boolean, mobile-compatible) — "Copied" only
  // shows after a real copy, and a failed copy is said out loud.
  const copyShare = () => {
    if (Clipboard.copy(shareText)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2200);
    }
  };

  const openTwitter = () => window.open(`https://twitter.com/intent/tweet?text=${encodeURIComponent(shareText)}`, '_blank', 'noopener,noreferrer');
  const openFacebook = () => window.open(`https://www.facebook.com/sharer/sharer.php?u=${encodeURIComponent(shareUrl)}&quote=${encodeURIComponent(shareText)}`, '_blank', 'noopener,noreferrer');

  // Always-available share for the page itself (no personal numbers required).
  const pageShareText = t('plantTrees.pageShareText', { site: siteName() });
  const shareThisPage = async () => {
    if (navigator.share) {
      try {
        await navigator.share({
          title: t('plantTrees.pageShareTitle', { site: siteName() }),
          text: pageShareText,
          url: shareUrl,
        });
        return;
      } catch { /* user cancelled */ }
    }
    if (Clipboard.copy(pageShareText + ' ' + shareUrl)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2200);
    }
  };

  // --- friendly human strings for the share card / image ---
  const co2Friendly = myCO2 >= 1000 ? (myCO2 / 1000).toFixed(1) + ' t CO₂' : Math.round(myCO2) + ' kg CO₂';
  const o2Friendly = myO2 >= 1000 ? (myO2 / 1000).toFixed(1) + ' t O₂' : Math.round(myO2) + ' kg O₂';
  const myTreeStr = fmtTreesUser(myTrees);

  // Draw a self-contained, post-ready receipt card (1080×1350, the 4:5 feed
  // format). Every string is MEASURED before it is drawn: it first shrinks to
  // fit its slot and, where the slot allows more than one line, wraps. Blocks
  // are laid out top-down from those measurements (no fixed x/y for text that
  // depends on the language or the size of the number), and the footer is
  // anchored to the bottom of the receipt. Fonts are awaited first, so the
  // first export never falls back to Georgia/system-ui.
  const makeShareImage = async (): Promise<Blob> => {
    const W = 1080, H = 1350;
    const canvas = document.createElement('canvas');
    canvas.width = W;
    canvas.height = H;
    const ctx = canvas.getContext('2d');
    if (!ctx) throw new Error('canvas unsupported');

    const font = 'NunitoLocal, Nunito, system-ui, sans-serif';
    const serif = 'FrauncesLocal, Georgia, serif';
    const kraft = '#E7DAC0';
    const paper = '#F6EFDC';
    const recess = '#EFE4C6';
    const ink = '#4E3D28';
    const inkDim = '#7C6A4E';
    const inkFaint = '#9B8763';
    const green = '#2F5540';
    const stamp = '#C7481D';
    const white = '#FFF6E8';
    const lang = (typeof document !== 'undefined' && (document.documentElement.getAttribute('data-lang') || document.documentElement.lang)) || 'en';
    const upper = (s: string) => { try { return s.toLocaleUpperCase(lang); } catch { return s.toUpperCase(); } };

    // Webfonts are lazy: a weight the page has not painted yet is not loaded,
    // and canvas silently draws with the fallback. Ask for every face we use.
    try {
      const fonts = (document as any).fonts;
      if (fonts?.load) {
        await Promise.race([
          Promise.all([
            fonts.load(`900 40px ${serif}`), fonts.load(`700 40px ${serif}`),
            fonts.load(`900 20px ${font}`), fonts.load(`800 20px ${font}`), fonts.load(`700 20px ${font}`),
          ]),
          new Promise((r) => setTimeout(r, 2500)),
        ]);
      }
    } catch { /* draw with whatever is available */ }

    const loadImg = (src: string): Promise<HTMLImageElement | null> =>
      new Promise((resolve) => {
        const image = new Image();
        image.onload = () => resolve(image);
        image.onerror = () => resolve(null);
        image.src = src;
      });

    const [logo, avatar] = await Promise.all([
      loadImg(asset('/img/brand-icon-96.png')),
      address ? resolveIdenticonUrl(address).then(loadImg).catch(() => null) : Promise.resolve(null),
    ]);

    const fillRound = (x: number, y: number, w: number, h: number, r: number, color: string) => {
      roundRect(ctx, x, y, w, h, r);
      ctx.fillStyle = color;
      ctx.fill();
    };
    const strokeRound = (x: number, y: number, w: number, h: number, r: number, color: string, width = 2) => {
      roundRect(ctx, x, y, w, h, r);
      ctx.strokeStyle = color;
      ctx.lineWidth = width;
      ctx.stroke();
    };
    const setFont = (size: number, weight: number, family: string) => { ctx.font = `${weight} ${size}px ${family}`; };
    const measure = (value: string, size: number, weight: number, family = font) => { setFont(size, weight, family); return ctx.measureText(value).width; };
    /** Largest size ≤ size (≥ min) at which `value` fits on one line in maxW. */
    const fitSize = (value: string, maxW: number, size: number, weight: number, family = font, min = Math.round(size * 0.6)) => {
      let s = size;
      while (s > min && measure(value, s, weight, family) > maxW) s -= 1;
      return s;
    };
    /** Greedy word wrap; a single over-long word is shrunk by the caller's fit. */
    const wrap = (value: string, maxW: number, size: number, weight: number, family = font): string[] => {
      setFont(size, weight, family);
      const words = String(value).split(/\s+/).filter(Boolean);
      const lines: string[] = [];
      let line = '';
      for (const w of words) {
        const next = line ? line + ' ' + w : w;
        if (!line || ctx.measureText(next).width <= maxW) line = next;
        else { lines.push(line); line = w; }
      }
      if (line) lines.push(line);
      return lines.length ? lines : [''];
    };
    /** Fit a paragraph into maxW × maxLines: shrink until it wraps into maxLines
     *  and no single line is wider than maxW. Returns the size and the lines. */
    const fitBlock = (value: string, maxW: number, size: number, weight: number, maxLines: number, family = font, min = Math.round(size * 0.62)) => {
      let s = size;
      for (;;) {
        const lines = wrap(value, maxW, s, weight, family);
        const widest = Math.max(...lines.map((l) => measure(l, s, weight, family)));
        if ((lines.length <= maxLines && widest <= maxW) || s <= min) {
          if (lines.length > maxLines) { // last resort: ellipsize the final line
            const kept = lines.slice(0, maxLines);
            let last = kept[maxLines - 1] + ' ' + lines.slice(maxLines).join(' ');
            while (last.length > 1 && measure(last + '…', s, weight, family) > maxW) last = last.slice(0, -1);
            kept[maxLines - 1] = last.trimEnd() + '…';
            return { size: s, lines: kept };
          }
          return { size: s, lines };
        }
        s -= 1;
      }
    };
    const text = (value: string, x: number, y: number, size: number, color: string, weight = 700, align: CanvasTextAlign = 'left', family = font) => {
      setFont(size, weight, family);
      ctx.fillStyle = color;
      ctx.textAlign = align;
      ctx.textBaseline = 'alphabetic';
      ctx.fillText(value, x, y);
    };
    /** Draw pre-wrapped lines; returns the y of the last baseline. */
    const lines = (ls: string[], x: number, y: number, size: number, lh: number, color: string, weight = 700, align: CanvasTextAlign = 'left', family = font) => {
      ls.forEach((l, i) => text(l, x, y + i * lh, size, color, weight, align, family));
      return y + (ls.length - 1) * lh;
    };
    const iconPaths: Record<string, string[]> = {
      tree: ['m17 14 3 3.3a1 1 0 0 1-.7 1.7H4.7a1 1 0 0 1-.7-1.7L7 14h-.3a1 1 0 0 1-.7-1.7L9 9h-.2A1 1 0 0 1 8 7.3L12 3l4 4.3a1 1 0 0 1-.8 1.7H15l3 3.3a1 1 0 0 1-.7 1.7H17Z', 'M12 22v-3'],
      cloud: ['M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z'],
      wind: ['M12.8 19.6A2 2 0 1 0 14 16H2', 'M17.5 8a2.5 2.5 0 1 1 2 4H2', 'M9.8 4.4A2 2 0 1 1 11 8H2'],
      leaf: ['M11 20a10 10 0 0010-10 25.9 25.9 0 00-1.04-7.281 1 1 0 00-1.755-.325C15.833 5.5 13 5.5 9.8 6.1A7 7 0 0011 20', 'M2 21a5 5 0 012.911-4.544C7.613 15.212 8.351 15.24 11 13'],
      // Was referenced for the "funded, not yet sent" status but never defined,
      // so that row rendered with an empty icon slot.
      clock: ['M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0Z', 'M12 6v6l4 2'],
      sprout: ['M7 20h10', 'M10 20c5.5-2.5.8-6.4 3-10', 'M9.5 9.4c1.1.8 1.8 2.2 2.3 3.7-2 .4-3.5.4-4.8-.3-1.2-.6-2.3-1.9-3-4.2 2.8-.5 4.4 0 5.5.8z', 'M14.1 6a7 7 0 0 0-1.1 4c1.9-.1 3.3-.6 4.3-1.4 1-1 1.6-2.3 1.7-4.6-2.7.1-4 1-4.9 2z'],
    };
    const drawIcon = (name: string, x: number, y: number, size: number, color: string, lineWidth = 2.2) => {
      ctx.save();
      ctx.translate(x, y);
      ctx.scale(size / 24, size / 24);
      ctx.strokeStyle = color;
      ctx.lineWidth = lineWidth;
      ctx.lineJoin = 'round';
      ctx.lineCap = 'round';
      for (const d of iconPaths[name] || []) ctx.stroke(new Path2D(d));
      ctx.restore();
    };
    const dashed = (y: number) => {
      ctx.setLineDash([10, 9]);
      ctx.strokeStyle = 'rgba(78,61,40,.42)';
      ctx.lineWidth = 2;
      ctx.beginPath(); ctx.moveTo(L, y); ctx.lineTo(R, y); ctx.stroke();
      ctx.setLineDash([]);
    };

    // Receipt geometry: content runs from L to R (896px wide).
    const L = 92, R = 988, CW = R - L;

    // Kraft background and the hard-edged paper receipt used everywhere else.
    ctx.fillStyle = kraft;
    ctx.fillRect(0, 0, W, H);
    ctx.strokeStyle = 'rgba(78,61,40,.06)';
    ctx.lineWidth = 1;
    for (let y = 0; y < H; y += 12) { ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(W, y); ctx.stroke(); }
    fillRound(62, 62, 972, 1242, 18, 'rgba(78,61,40,.16)');
    fillRound(54, 54, 972, 1242, 18, paper);
    ctx.save(); roundRect(ctx, 54, 54, 972, 1242, 18); ctx.clip();
    ctx.fillStyle = stamp; ctx.fillRect(54, 54, 972, 12);
    ctx.restore();
    strokeRound(54, 54, 972, 1242, 18, ink, 3);

    // ---- masthead: logo + site name | serial + route (right column measured)
    const serial = t('plantTrees.canvasSerial');
    const route = t('plantTrees.canvasCashbackTrees');
    const rightW = Math.min(300, Math.max(measure(serial, 14, 900), measure(route, 14, 900)));
    const serialSize = fitSize(serial, rightW, 14, 900, font, 11);
    const routeSize = fitSize(route, rightW, 14, 900, font, 11);
    text(serial, R, 112, serialSize, inkFaint, 900, 'right');
    text(route, R, 140, routeSize, green, 900, 'right');
    const brandX = logo ? 170 : L;
    if (logo) ctx.drawImage(logo, L, 94, 58, 58);
    const brandMaxW = R - rightW - 28 - brandX;
    // A hostname, not a word: plain uppercase (tr-locale would turn i → İ).
    const brandName = siteName().toUpperCase();
    text(brandName, brandX, 127, fitSize(brandName, brandMaxW, 29, 900, serif, 16), ink, 900, 'left', serif);
    const receipt = t('plantTrees.canvasImpactReceipt');
    text(receipt, brandX, 155, fitSize(receipt, brandMaxW, 16, 900, font, 11), inkFaint, 900);
    dashed(190);

    // ---- identity line (identicon on the right when connected)
    const idMaxW = (avatar ? 884 - 24 : R) - L;
    const yourCard = t('plantTrees.canvasYourCard');
    text(yourCard, L, 246, fitSize(yourCard, idMaxW, 18, 900, font, 12), inkFaint, 900);
    const sub = fitBlock(t('plantTrees.canvasCardSub'), idMaxW, 30, 800, 2, serif, 20);
    let y = lines(sub.lines, L, 283, sub.size, Math.round(sub.size * 1.15), ink, 800, 'left', serif);
    if (avatar) {
      fillRound(884, 214, 72, 72, 10, white);
      ctx.drawImage(avatar, 890, 220, 60, 60);
      strokeRound(884, 214, 72, 72, 10, ink, 2);
    }
    y = Math.max(y, 290);

    // ---- hero: number + "trees funded" lockup. Side by side when both fit at
    // a good size, otherwise the label block drops under the number.
    const heroX = L, heroW = CW, padX = 38;
    const innerW = heroW - padX * 2;
    const heroTop = y + 30;
    const contribution = t('plantTrees.canvasContribution');
    const funded = t('plantTrees.canvasTreesFunded');
    const ordersLine = t('plantTrees.donatingOrder', { count: myOrders });
    const labelIcon = 34, labelGap = 14, colGap = 44;
    const labelMaxW = 360;
    const fundedBlock = fitBlock(funded, labelMaxW - labelIcon - labelGap, 28, 900, 2, font, 20);
    const fundedW = Math.max(...fundedBlock.lines.map((l) => measure(l, fundedBlock.size, 900))) + labelIcon + labelGap;
    const ordersSize = fitSize(ordersLine, labelMaxW - labelIcon - labelGap, 20, 700, font, 15);
    const labelBlockW = Math.max(fundedW, measure(ordersLine, ordersSize, 700) + labelIcon + labelGap);
    let numSize = 164;
    const sideBySideRoom = innerW - labelBlockW - colGap;
    while (numSize > 112 && measure(myTreeStr, numSize, 900, serif) > sideBySideRoom) numSize -= 2;
    const sideBySide = measure(myTreeStr, numSize, 900, serif) <= sideBySideRoom;
    if (!sideBySide) numSize = fitSize(myTreeStr, innerW, 164, 900, serif, 72);
    const fundedLH = Math.round(fundedBlock.size * 1.12);
    const labelBlockH = fundedLH * fundedBlock.lines.length + 12 + ordersSize;
    const numTop = heroTop + 76;                  // top of the digits' box
    const numBase = numTop + Math.round(numSize * 0.74);
    const labelTop = sideBySide ? numBase - Math.round(numSize * 0.37) - Math.round(labelBlockH / 2) : numBase + 34;
    const destination = me.preference === 'trees' ? t('plantTrees.destTrees') : t('plantTrees.canvasWalletSwitch');
    const destBlock = fitBlock(destination, innerW - 36, 18, 700, 2, font, 14);
    const destLH = Math.round(destBlock.size * 1.3);
    const ruleY = (sideBySide ? numBase : labelTop + labelBlockH) + 38;
    const heroH = ruleY - heroTop + 22 + destLH * destBlock.lines.length + 18;

    // Pre-measure the status strip and the footer so leftover height can be
    // shared between the gaps instead of pooling above the footer.
    const status = myPlanted > 0
      ? t('plantTrees.canvasStatusSent', { trees: fmtTreesUser(myPlanted) })
      : myTrees > 0 ? t('plantTrees.statusFunded') : t('plantTrees.canvasStatusChoose');
    const stBlock = fitBlock(status, CW - 72 - 28, 19, 800, 2, font, 14);
    const stLH = Math.round(stBlock.size * 1.35);
    const stH = Math.max(72, 34 + stLH * stBlock.lines.length);
    const footBottom = 1214;
    const tag = fitBlock(t('plantTrees.canvasTagline'), CW, 18, 700, 2, font, 14);
    const tagLH = Math.round(tag.size * 1.35);
    const tagTop = footBottom - 58 - tagLH * (tag.lines.length - 1);
    const footRule = tagTop - 124;
    const tileH = 128;
    const projected = heroTop + heroH + 80 + tileH + 30 + stH + 36;
    const extra = Math.max(0, Math.min(34, (footRule - projected) / 3));
    fillRound(heroX, heroTop, heroW, heroH, 14, green);
    const cx = heroX + padX;
    text(contribution, cx, heroTop + 50, fitSize(contribution, innerW, 15, 900, font, 11), 'rgba(255,246,232,.72)', 900);
    text(myTreeStr, cx - 4, numBase, numSize, white, 900, 'left', serif);
    const labelX = sideBySide ? cx + measure(myTreeStr, numSize, 900, serif) + colGap : cx;
    drawIcon('tree', labelX, labelTop + Math.round((fundedLH - labelIcon) / 2), labelIcon, white, 1.8);
    const fx = labelX + labelIcon + labelGap;
    const fundedLast = lines(fundedBlock.lines, fx, labelTop + fundedBlock.size, fundedBlock.size, fundedLH, white, 900);
    text(ordersLine, fx, fundedLast + 12 + ordersSize + 4, ordersSize, 'rgba(255,246,232,.78)', 700);
    ctx.strokeStyle = 'rgba(255,246,232,.26)';
    ctx.lineWidth = 2;
    ctx.beginPath(); ctx.moveTo(cx, ruleY); ctx.lineTo(heroX + heroW - padX, ruleY); ctx.stroke();
    drawIcon('leaf', cx, ruleY + 20, 22, 'rgba(255,246,232,.8)', 1.7);
    lines(destBlock.lines, cx + 36, ruleY + 22 + destBlock.size, destBlock.size, destLH, 'rgba(255,246,232,.8)', 700);
    y = heroTop + heroH;

    // ---- footprint metrics (two equal tiles; value shrinks to its tile)
    const fpLabel = upper(t('plantTrees.footprint'));
    text(fpLabel, L, y + extra + 56, fitSize(fpLabel, CW, 16, 900, font, 12), inkFaint, 900);
    y += extra;
    const tileTop = y + 80, tileGap = 24, tileW = (CW - tileGap) / 2;
    const tile = (x: number, icon: string, label: string, value: string) => {
      fillRound(x, tileTop, tileW, tileH, 10, recess);
      drawIcon(icon, x + 30, tileTop + 39, 32, green, 1.8);
      const tx = x + 82, tw = tileW - 82 - 22;
      text(label, tx, tileTop + 44, fitSize(label, tw, 13, 900, font, 10), inkFaint, 900);
      text(value, tx, tileTop + 88, fitSize(value, tw, 30, 900, font, 18), green, 900);
    };
    tile(L, 'cloud', t('plantTrees.co2Potential'), `≈ ${co2Friendly}`);
    tile(L + tileW + tileGap, 'wind', t('plantTrees.o2Potential'), `≈ ${o2Friendly}`);
    y = tileTop + tileH;

    // ---- status strip (wraps to two lines instead of running off the paper)
    const stTop = y + 30 + extra;
    fillRound(L, stTop, CW, stH, 8, '#F3E2D2');
    ctx.fillStyle = stamp; ctx.fillRect(L, stTop, 6, stH);
    drawIcon(myPlanted > 0 ? 'leaf' : myTrees > 0 ? 'clock' : 'sprout', L + 32, stTop + stH / 2 - 11, 22, stamp, 1.8);
    const stTextTop = stTop + (stH - stLH * stBlock.lines.length) / 2 + stBlock.size * 0.98;
    lines(stBlock.lines, L + 72, stTextTop, stBlock.size, stLH, inkDim, 800);
    y = stTop + stH;

    // ---- footer, anchored to the bottom of the receipt
    text('onetreeplanted.org', L, footBottom, 16, inkFaint, 800);
    text('plant-trees', R, footBottom, 16, stamp, 900, 'right');
    lines(tag.lines, L, tagTop, tag.size, tagLH, inkDim, 700);
    const site = siteName();
    text(site, L, tagTop - 34, fitSize(site, CW, 32, 900, serif, 20), ink, 900, 'left', serif);
    const from = t('plantTrees.canvasReceiptFrom');
    text(from, L, tagTop - 78, fitSize(from, CW, 13, 900, font, 10), inkFaint, 900);
    dashed(Math.max(y + 36, footRule));

    return await new Promise<Blob>((resolve, reject) => {
      canvas.toBlob((b) => (b ? resolve(b) : reject(new Error('toBlob failed'))), 'image/png');
    });
  };

  // Blob URL instead of a multi-MB data: URL — faster to render, and the
  // download / share paths all work from the same object.
  const openShareImage = async () => {
    try {
      const blob = await makeShareImage();
      shareBlobRef.current = blob;
      setShareImg((prev) => {
        if (prev && prev.startsWith('blob:')) URL.revokeObjectURL(prev);
        return URL.createObjectURL(blob);
      });
    } catch (e) {
      console.error(e);
      setShareImg(null);
      copyShare();
    }
  };

  // <a download> covers desktop + Android Chrome. iOS Safari and in-app
  // WebViews (Nimiq Pay) ignore the download attribute and would just navigate
  // to the blob — there, hand the file to the share sheet ("Save image").
  const downloadShareImage = async (e: { preventDefault: () => void }) => {
    const blob = shareBlobRef.current;
    if (!blob || typeof navigator === 'undefined') return;
    const ua = navigator.userAgent || '';
    const noDownloadAttr = /iPad|iPhone|iPod/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1) || Boolean((window as any).nimiqPay);
    if (!noDownloadAttr) return; // let the anchor download normally
    const file = new File([blob], 'nimshop-impact-card.png', { type: 'image/png' });
    if (navigator.canShare && navigator.canShare({ files: [file] })) {
      e.preventDefault();
      try { await navigator.share({ files: [file] }); } catch { /* user cancelled */ }
    }
  };

  const closeShareImage = () => {
    shareBlobRef.current = null;
    setShareImg((prev) => {
      if (prev && prev.startsWith('blob:')) URL.revokeObjectURL(prev);
      return null;
    });
  };

  // While the preview is open: lock the page behind it (no scroll bleed, no
  // scrollbar gutter eating into the dialog on desktop) and close on Escape.
  useEffect(() => {
    if (!shareImg || typeof document === 'undefined') return;
    // Both: `body{overflow-x:hidden}` makes <body> the page's scroll container,
    // so locking only <html> left the page scrollable (and its scrollbar
    // narrowing the dialog by 15px on desktop).
    const html = document.documentElement, body = document.body;
    const prevHtml = html.style.overflow, prevBody = body.style.overflow;
    html.style.overflow = 'hidden';
    body.style.overflow = 'hidden';
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') closeShareImage(); };
    window.addEventListener('keydown', onKey);
    return () => { html.style.overflow = prevHtml; body.style.overflow = prevBody; window.removeEventListener('keydown', onKey); };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shareImg]);

  if (loading) {
    return (
      <div className="container pt-page">
        <div className="card" style={{ padding: 24, textAlign: 'center' }}><div className="spinner" /></div>
      </div>
    );
  }

  return (
    <div className="container pt-page">
      {/* ------------------------------------------------ hero — Option B ultra minimal;
           the pitch is the left column and the OneTreePlanted lockup is the
           right one (its own block on top on phones — see .pt-hero-side). */}
      <section className="pt-hero" style={{ position: 'relative', overflow: 'hidden', background: 'var(--surface-1)', border: '2px solid var(--line-strong)', boxShadow: '4px 4px 0 rgba(78,61,40,.18)' }}>
        <div className="pt-hero-main" style={{ textAlign: 'center', maxWidth: 640, margin: '0 auto', position: 'relative', zIndex: 1 }}>
          <div className="pt-eyebrow" style={{ justifyContent: 'center' }}><Icon name="leaf" size={15} /> {t('plantTrees.eyebrow')}</div>
          <h1 className="pt-title" style={{ textAlign: 'center' }}>{t('plantTrees.title')}</h1>
          <div style={{ fontFamily: 'var(--font-serif)', fontWeight: 900, fontSize: 'clamp(2.4rem, 2rem + 2.2vw, 3.2rem)', lineHeight: 1, color: 'var(--green-600)', marginTop: 10 }}>{formatTrees(totalsFunded)} <Icon name="tree" size={34} /></div>
          <div className="xs faint" style={{ textAlign: 'center', marginTop: 4 }}>
            {t('plantTrees.heroStats', {
              funded: formatTrees(totalsFunded),
              planted: formatTrees(totalsPlanted),
              orders: String(totals.orders || 0),
            })}
          </div>
          <div className="pt-chips" style={{ justifyContent: 'center', marginTop: 14 }}>
            <span className="chip pt-chip"><Icon name="nimiq" size={14} className="pt-chip-icon" />{t('plantTrees.chipNim')}</span>
            <span className="chip pt-chip"><Icon name="card" size={14} className="pt-chip-icon" />{t('plantTrees.chipUsdt', { pct: usdtPct })}</span>
            <span className="chip pt-chip"><Icon name="search" size={14} className="pt-chip-icon" />{t('plantTrees.chipChain')}</span>
          </div>
          <p className="pt-lead" style={{ textAlign: 'center', margin: '12px auto 0', maxWidth: 52 + 'ch' }}>{t('plantTrees.lead')}</p>
          <div className="pt-hero-actions" style={{ justifyContent: 'center', marginTop: 14 }}>
            <button className="btn btn-gold btn-sm" onClick={shareThisPage}>
              <Icon name="share" size={15} /> {t('plantTrees.sharePage')}
            </button>
          </div>
        </div>

        {/* Partner lockup — its own column filling the right-hand side on
            desktop; on phones the hero is one column and CSS moves this block
            to the TOP (order:-1) so the logo reads as its own block instead of
            trailing the share button. */}
        <aside className="pt-hero-side" aria-label={t('plantTrees.partner')}>
          <div className="pt-partner">
            <span className="pt-partner-cap">{t('plantTrees.partner')}</span>
            <img src={asset('/img/onetreeplanted.png')} alt="One Tree Planted" />
          </div>
        </aside>
      </section>

      {loadFailed && !data && (
        <section className="pt-card mt-2" role="alert">
          <ErrorState retry={() => { setLoading(true); setReloadKey((k) => k + 1); }} />
        </section>
      )}

      {treeDonationAddress && (
        <section className="pt-card mt-2" aria-label={t('plantTrees.donateWalletAria')} style={{ padding: 14, display: 'flex', flexWrap: 'wrap', gap: 12, alignItems: 'center', justifyContent: 'space-between' }}>
          <div className="pt-icon-inline" style={{ gap: 10 }}><Identicon address={treeDonationAddress} size={64} /><span><strong style={{ fontSize: 13 }}>{t('plantTrees.donateWallet')}</strong><br /><code className="small mono" style={{ fontSize: '14px', wordBreak: 'normal', overflowWrap: 'anywhere' }}>{treeDonationAddress}</code></span></div>
          <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
            <span className="small" style={{ fontWeight: 800 }}>{walletNim !== null ? <>{fmtNIM(walletNim, 0)} NIM</> : '…'}{walletBalanceStale ? t('plantTrees.lastKnown') : ''}</span>
            <button className="btn btn-sm btn-ghost" onClick={copyTreeAddress}><Icon name="copy" size={13} /> {treeAddressCopied ? t('actions.copied') : t('actions.copy')}</button>
          </div>
        </section>
      )}

      {/* ------------------------------------------------ global totals */}
      <div className="pt-stats">
        <Stat icon="tree" value={formatTrees(totalsFunded)} label={t('plantTrees.statTrees')} />
        <Stat icon="cloud" value={formatMass(totalCO2)} label={t('plantTrees.statCo2')} />
        <Stat icon="wind" value={formatMass(totalO2)} label={t('plantTrees.statO2')} />
        <Stat icon="gift" value={String(totals.orders || 0)} label={t('plantTrees.statOrders')} />
      </div>

      {/* ------------------------------------------------ personal impact */}
      {(me?.enabled ? (
        <section className="pt-card pt-impact" aria-label={t('plantTrees.impactAria')}>
          <div className="pt-impact-shell">
            <div className="pt-impact-bar">
              <div className="pt-impact-bar-brand">
                <span className="pt-impact-logo"><Icon name="leaf" size={21} /></span>
                <div className="pt-impact-bar-copy">
                  <div className="pt-impact-kicker">{t('plantTrees.kicker', { site: siteName() })}</div>
                  <h2 className="pt-impact-title">{t('plantTrees.impactTitle')}</h2>
                </div>
              </div>
              <span className={`pt-impact-stamp${myTrees > 0 ? '' : ' is-empty'}`}>
                <Icon name="tree" size={14} /> {myTrees > 0 ? t('plantTrees.stampFunded') : t('plantTrees.stampReady')}
              </span>
            </div>

            <div className="pt-impact-summary">
              <div className="pt-impact-total-copy">
                <div className="pt-impact-kicker">{t('plantTrees.myContribution')}</div>
                <div className="pt-impact-count-row">
                  <span className="pt-impact-count">{myTreeStr}</span>
                  <span className="pt-impact-count-label">
                    <Icon name="tree" size={28} />
                    <span>{t('plantTrees.treesFunded')}</span>
                  </span>
                </div>
                <div className="pt-impact-orders">
                  <span className="pt-impact-order-item"><Icon name="receipt" size={14} /> {t('plantTrees.donatingOrder', { count: myOrders })}</span>
                  <span className="pt-impact-order-destination">
                    {address ? <Identicon address={address} size={20} /> : <Icon name="sprout" size={15} />}
                    {me.preference === 'trees' ? t('plantTrees.destTrees') : t('plantTrees.destWallet')}
                  </span>
                </div>
              </div>
            </div>

            <div className="pt-impact-metrics-label">{t('plantTrees.footprint')}</div>
            <div className="pt-impact-metrics">
              <div className="pt-impact-metric">
                <span className="pt-impact-metric-icon"><Icon name="cloud" size={20} /></span>
                <span><small>{t('plantTrees.co2Potential')}</small><strong>≈ {formatMass(myCO2)}</strong></span>
              </div>
              <div className="pt-impact-metric">
                <span className="pt-impact-metric-icon"><Icon name="wind" size={20} /></span>
                <span><small>{t('plantTrees.o2Potential')}</small><strong>≈ {formatMass(myO2)}</strong></span>
              </div>
            </div>

            {myPlanted > 0 ? (
              <div className="pt-impact-status"><Icon name="leaf" size={17} /><span>{t('plantTrees.statusSent', { trees: fmtTreesUser(myPlanted) })}</span></div>
            ) : myTrees > 0 ? (
              <div className="pt-impact-status"><Icon name="clock" size={17} /><span>{t('plantTrees.statusFunded')}</span></div>
            ) : (
              <div className="pt-impact-status"><Icon name="sprout" size={17} /><span>{t('plantTrees.statusChoose')}</span></div>
            )}

            <div className="pt-impact-share-head">
              <div>
                <div className="pt-impact-metrics-label">{t('plantTrees.shareHead')}</div>
                <div className="pt-impact-share-copy">{t('plantTrees.shareHeadCopy')}</div>
              </div>
              <Icon name="share" size={22} />
            </div>
            <div className="pt-actions">
              <ShareBtn onClick={openShareImage} icon="download" label={t('plantTrees.downloadPng')} primary />
              <ShareBtn onClick={nativeShare} icon="share" label={t('plantTrees.share')} />
              <ShareBtn onClick={openTwitter} brand="x" label={t('plantTrees.postOnX')} />
              <ShareBtn onClick={openFacebook} brand="fb" label={t('plantTrees.facebook')} />
              <ShareBtn onClick={copyShare} icon="link" label={copied ? t('actions.copied') : t('plantTrees.copyLink')} />
            </div>
            {copied && (
              <div className="small pt-value pt-icon-inline pt-copied">
                <Icon name="check" size={15} /> {t('plantTrees.copiedNote')}
              </div>
            )}
          </div>
        </section>
      ) : (
        <section className="pt-card pt-impact" aria-label={t('plantTrees.impactAria')}>
          <div className="pt-impact-shell">
            <div className="pt-impact-bar">
              <div className="pt-impact-bar-brand">
                <span className="pt-impact-logo"><Icon name="leaf" size={21} /></span>
                <div className="pt-impact-bar-copy">
                  <div className="pt-impact-kicker">{t('plantTrees.kicker', { site: siteName() })}</div>
                  <h2 className="pt-impact-title">{t('plantTrees.impactTitle')}</h2>
                </div>
              </div>
              <span className="pt-impact-stamp is-empty"><Icon name="tree" size={14} /> {t('plantTrees.stampReady')}</span>
            </div>
            <div className="pt-impact-empty">
              <div className="pt-icon-inline" style={{ justifyContent: 'center', color: 'var(--green-600)', marginTop: 8 }}><Icon name="tree" size={32} /></div>
              <div style={{ textAlign: 'center' }}>
                <div className="strong" style={{ fontSize: '1.1rem' }}>{t('plantTrees.connectTitle')}</div>
                <div className="small muted" style={{ marginTop: 6, maxWidth: 420, marginInline: 'auto' }}>{t('plantTrees.connectBody')}</div>
              </div>
              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', justifyContent: 'center', marginTop: 4 }}>
                <span className="chip"><Icon name="tree" size={13} /> {t('plantTrees.emptyChipTrees')}</span>
                <span className="chip"><Icon name="cloud" size={13} /> {t('plantTrees.emptyChipCo2')}</span>
              </div>
            </div>
          </div>
        </section>
      ))}

      {/* ------------------------------------------------ share preview card */}
      {me?.enabled && myTrees > 0 && (
        <section className="pt-share-card">
          <div className="pt-share-top">
            <div className="pt-share-main">
              <div className="xs" style={{ opacity: 0.85, letterSpacing: 1.2, textTransform: 'uppercase', fontWeight: 700 }}>
                {t('plantTrees.myImpact', { site: siteName() })}
              </div>
              <div className="pt-impact-num">
                {fmtTreesUser(myTrees)} <Icon name="tree" size={34} />
              </div>
              <div className="pt-impact-sub">
                <Icon name="cloud" size={15} /> {t('plantTrees.shareCardSub', { co2: formatMass(myCO2) })}
              </div>
            </div>
            <div className="pt-share-tree"><Icon name="tree" size={60} /></div>
          </div>
          <div className="pt-share-foot">
            <span className="pt-share-brand">
              {address && <Identicon address={address} size={26} />}
              <span>{siteName()}/plant-trees</span>
            </span>
            <span className="pt-share-chip">
              <Icon name={me.preference === 'trees' ? 'sprout' : 'wallet'} size={13} />
              {me.preference === 'trees' ? t('plantTrees.chipPlantTrees') : t('plantTrees.chipMyWallet')} · <Icon name="nimiq" size={13} /> NIM /{' '}
              <Icon name="card" size={13} /> {t('plantTrees.usdtOnPolygon')}
            </span>
          </div>
        </section>
      )}

      {/* ------------------------------------------------ leaderboard */}
      <section className="pt-card">
        <div className="pt-card-head">
          <h2 className="pt-section-title"><Icon name="trophy" size={20} /> {t('plantTrees.leaderboard')}</h2>
          <div className="pt-seg">
            {(['week', 'month', 'all'] as const).map((b) => (
              <button key={b} className={bucket === b ? 'btn btn-sm btn-gold' : 'btn btn-sm btn-outline'} onClick={() => setBucket(b)}>
                {b === 'week' ? t('plantTrees.thisWeek') : b === 'month' ? t('plantTrees.thisMonth') : t('plantTrees.allTime')}
              </button>
            ))}
          </div>
        </div>
        {/* pinned: the signed-in buyer's own standing */}
        {me?.enabled && cleanUser && (
          <div className="pt-yourank">
            {address && <Identicon address={address} size={34} className="pt-ident-lg" />}
            <div className="pt-yourank-info">
              <div className="xs faint">{t('plantTrees.yourStanding')}</div>
              <div className="pt-yourank-line">
                {myLeaderRank ? (
                  rich(
                    t('plantTrees.youRank', {
                      rank: myLeaderRank,
                      trees: fmtTreesUser(myTrees),
                      orders: t('plantTrees.orders', { count: myOrders }),
                    }),
                  )
                ) : (
                  rich(
                    t('plantTrees.youFunded', {
                      trees: fmtTreesUser(myTrees),
                      orders: t('plantTrees.orders', { count: myOrders }),
                      outside: leaders.length > 0 ? t('plantTrees.outsideTop') : '',
                    }),
                  )
                )}
              </div>
            </div>
          </div>
        )}

        {leaders.length === 0 ? (
          <p className="small muted center" style={{ margin: 0 }}>{t('plantTrees.noDonations')}</p>
        ) : (
          <>
            <ol className="pt-leader">
              {visibleLeaders.map((row) => (
                <li key={row.user} className={row.rank === myLeaderRank ? 'pt-you-row' : ''}>
                  <span className={`pt-badge${row.rank <= 3 ? ' ' + RANK_CLS[row.rank - 1] : ''}`}>
                    {row.rank <= 3 && <Icon name="award" size={15} />}
                    {row.rank}
                  </span>
                  <Identicon address={row.user} size={28} className="pt-ident" />
                  <code className="pt-leader-code" title={row.user}>{shortAddress(row.user)}</code>
                  <span className="pt-leader-trees pt-icon-inline">{formatTrees(row.trees)} <Icon name="tree" size={14} /></span>
                  <span className="pt-leader-orders">
                    {t('plantTrees.orders', { count: row.orders })}
                    {Number(row.planted || 0) > 0 && (
                      <>{t('plantTrees.plantedSuffix', { trees: formatTrees(Number(row.planted || 0)) })}</>
                    )}
                  </span>
                  {row.rank === myLeaderRank && <span className="pt-you-badge"><Icon name="user" size={12} /> {t('plantTrees.you')}</span>}
                </li>
              ))}
            </ol>
            {leaderPages > 1 && (
              <div className="pt-pager">
                <button className="btn btn-sm btn-outline" disabled={leaderPageSafe === 0} onClick={() => setLeaderPage(leaderPageSafe - 1)}>
                  <Icon name="back" size={15} /> {t('actions.back')}
                </button>
                <span className="small muted">{leaderPageSafe + 1} / {leaderPages}</span>
                <button className="btn btn-sm btn-outline" disabled={leaderPageSafe >= leaderPages - 1} onClick={() => setLeaderPage(leaderPageSafe + 1)}>
                  {t('plantTrees.more')} <Icon name="chevron" size={15} />
                </button>
              </div>
            )}
          </>
        )}
      </section>

      {/* ------------------------------------------ monthly payouts (paid on Polygon) */}
      <section className="pt-card">
        <div className="pt-card-head" style={{ marginBottom: 6 }}>
          <h2 className="pt-section-title">
            {t('plantTrees.payoutsTitle')}
          </h2>
        </div>
        <p className="pt-sub" style={{ marginBottom: 12 }}>
          {t('plantTrees.payoutsBody')}
        </p>
        {sortedSettlements.length === 0 ? (
          <p className="small muted" style={{ margin: 0 }}>{t('plantTrees.noPayouts')}</p>
        ) : (
          <>
            <div className="pt-settlements">
              {visibleSettlements.map((s) => (
                <div key={s.id} className="pt-settlement">
                  <div className="pt-settlement-head">
                    <div className="pt-settlement-info">
                      <div className="strong">{s.month_bucket || '—'}</div>
                      {s.status === 'skipped' ? (
                        <div className="small" style={{ color: 'var(--amber)' }}>
                          {t('plantTrees.periodSkipped', {
                            nim: Number(s.amount_nim || 0).toFixed(2),
                            wallet: Number(s.wallet_nim || 0).toFixed(2),
                          })}
                        </div>
                      ) : (
                        <div className="small">
                          {t('plantTrees.payoutPaid', {
                            value: s.amount_value || Number(s.amount_usdt || 0).toFixed(2),
                            label: s.amount_label || 'USDT',
                            trees: formatTrees(s.trees_planted),
                          })}
                        </div>
                      )}
                      {s.note && <span className="xs muted">{s.note}</span>}
                      {!!s.proof_images?.length && <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>{s.proof_images.map((p, i) => <a key={i} href={p.data} target="_blank" rel="noopener noreferrer" className="small" style={{ display: 'inline-flex', flexDirection: 'column', gap: 3 }}><img src={p.data} alt={p.caption || t('plantTrees.proofAlt', { n: i + 1 })} style={{ width: 100, height: 70, objectFit: 'cover', borderRadius: 6, border: '1px solid var(--line-dash)' }} /><span>{p.caption || t('plantTrees.viewProof', { n: i + 1 })}</span></a>)}</div>}
                    </div>
                    {s.polygonscan_url ? (
                      <a href={s.polygonscan_url} target="_blank" rel="noopener noreferrer" className="btn btn-sm btn-outline">
                        {t('plantTrees.viewPolygonscan')}
                      </a>
                    ) : (
                      <span className="xs muted">{t('plantTrees.txPending')}</span>
                    )}
                  </div>
                  {s.tx_hash && <code className="pt-txhash">{s.tx_hash}</code>}
                </div>
              ))}
            </div>
            {settlePages > 1 && (
              <div className="pt-pager">
                <button className="btn btn-sm btn-outline" disabled={settlePageSafe === 0} onClick={() => setSettlePage(settlePageSafe - 1)}>
                  {t('plantTrees.newer')}
                </button>
                <span className="small muted">
                  {settlePageSafe + 1} / {settlePages}
                </span>
                <button className="btn btn-sm btn-outline" disabled={settlePageSafe >= settlePages - 1} onClick={() => setSettlePage(settlePageSafe + 1)}>
                  {t('plantTrees.older')}
                </button>
              </div>
            )}
          </>
        )}
        <div className="pt-footnote xs" style={{ marginTop: 12 }}>
          <span>
            {t('plantTrees.footnote', {
              trees: treesPerUsd.toFixed(2),
              chain: cfg?.polygon_chain_id || 137,
              months: SETTLE_PER_PAGE,
            })}
          </span>
        </div>
      </section>

      {/* ------------------------------------------------ footer links */}
      <div className="small center muted pt-icon-inline pt-foot-links" style={{ margin: '2px 0', justifyContent: 'center' }}>
        {t('plantTrees.learnMore')}{' '}
        <a href="https://onetreeplanted.org/" target="_blank" rel="noopener noreferrer" className="pt-icon-inline"><Icon name="external" size={13} /> onetreeplanted.org</a>
        {' · '}
        <a href="https://polygonscan.com/" target="_blank" rel="noopener noreferrer" className="pt-icon-inline"><Icon name="external" size={13} /> polygonscan.com</a>
      </div>

      {/* ------------------------------------------------ share-image preview */}
      {shareImg && (
        <div className="pt-overlay" role="dialog" aria-modal="true" aria-label={t('plantTrees.overlayHead')} onClick={closeShareImage}>
          <div className="pt-overlay-box" onClick={(e) => e.stopPropagation()}>
            <div className="pt-overlay-head">
              <div>
                <div className="strong">{t('plantTrees.overlayHead')}</div>
                <div className="xs muted">{t('plantTrees.shareHeadCopy')}</div>
              </div>
              <button className="btn btn-sm btn-ghost" onClick={closeShareImage} aria-label={t('actions.close')}>
                <Icon name="x" size={16} /> <span className="pt-overlay-close-label">{t('actions.close')}</span>
              </button>
            </div>
            <div className="pt-overlay-preview">
              <img src={shareImg} width={1080} height={1350} alt={t('plantTrees.shareImgAlt', { site: siteName() })} />
            </div>
            <div className="pt-caption">
              <div className="pt-caption-label"><Icon name="copy" size={14} /> {t('plantTrees.postCaption')}</div>
              <p className="pt-caption-text">{shareText}</p>
              <button
                className="btn btn-sm btn-ghost"
                onClick={() => {
                  if (Clipboard.copy(shareText)) {
                    setCopied(true);
                    setTimeout(() => setCopied(false), 2200);
                  }
                }}
              >
                <Icon name="copy" size={14} /> {copied ? t('plantTrees.copiedCheck') : t('plantTrees.copyCaption')}
              </button>
            </div>
            <div className="pt-overlay-actions">
              <a className="btn btn-gold" href={shareImg} download="nimshop-impact-card.png" onClick={downloadShareImage}>
                <Icon name="download" size={16} /> {t('plantTrees.downloadPng')}
              </a>
              <button
                className="btn btn-gold"
                onClick={async () => {
                  try {
                    const blob = shareBlobRef.current || (await makeShareImage());
                    const file = new File([blob], 'nimshop-impact-card.png', { type: 'image/png' });
                    // Post the image AND the caption together where the OS supports it.
                    if (navigator.canShare && navigator.canShare({ files: [file] })) {
                      await navigator.share({
                        files: [file],
                        title: t('plantTrees.myImpact', { site: siteName() }) + ' 🌳',
                        text: shareText,
                      });
                      return;
                    }
                    // Otherwise prefill a tweet and copy the caption so it's one paste away.
                    Clipboard.copy(shareText);
                    window.open('https://twitter.com/intent/tweet?text=' + encodeURIComponent(shareText), '_blank', 'noopener,noreferrer');
                  } catch {
                    Clipboard.copy(shareText);
                    window.open('https://twitter.com/intent/tweet?text=' + encodeURIComponent(shareText), '_blank', 'noopener,noreferrer');
                  }
                }}
              >
                <Icon name="share" size={16} /> {t('plantTrees.postImageCaption')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function ShareBtn({ onClick, icon, brand, label, primary }: { onClick: () => void; icon?: string; brand?: 'x' | 'fb'; label: string; primary?: boolean }) {
  // Brand glyphs (X / Facebook) have no Lucide equivalents, so they use the
  // platform's own mark; every other share action uses a Lucide icon.
  const glyph = brand === 'x' ? '𝕏' : brand === 'fb' ? 'f' : null;
  return (
    <button type="button" onClick={onClick} className={'btn btn-sm' + (primary ? ' btn-gold' : ' btn-outline')} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
      {icon ? <Icon name={icon as any} size={14} /> : <span style={{ fontWeight: 800, fontSize: '0.9rem' }}>{glyph}</span>}
      {label}
    </button>
  );
}

function Stat({ icon, label, value }: { icon: string; label: string; value: string }) {
  return (
    <div className="pt-stat">
      <span className="pt-stat-ico"><Icon name={icon as any} size={22} /></span>
      <span className="pt-stat-num">{value}</span>
      <span className="pt-stat-label">{label}</span>
    </div>
  );
}

/** Address grouped in blocks of four, e.g. NQ02 K0GD 3WJH DVqD. */
function shortAddress(addr: string): string {
  return canonicalIdenticonInput(addr);
}

function formatTrees(n: number): string {
  if (!isFinite(n) || n <= 0) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k';
  if (n < 1) return n.toFixed(2);
  if (n < 10) return n.toFixed(1);
  return String(Math.round(n));
}

function formatMass(kg: number): string {
  if (kg >= 1_000_000) return (kg / 1_000_000).toFixed(1) + ' kt';
  if (kg >= 1_000) return (kg / 1_000).toFixed(1) + ' t';
  return Math.round(kg) + ' kg';
}
