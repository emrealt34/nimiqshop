/**
 * CashbackImpactSection.tsx — public Cashback stats,
 * personal shareable Cashback receipt card (1080×1350 PNG export + Web Share /
 * X / Facebook / Copy link), and weekly/monthly/all-time Cashback
 * Leaderboard on the /cashback page.
 */
import { useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { Icon } from '../ui/Icon';
import { Identicon } from '../ui/Identicon';
import { canonicalIdenticonInput, resolveIdenticonUrl } from '../../lib/identicon';
import { getCashbackLeaderboard } from '../../lib/api';
import { siteName, siteURL } from '../../lib/config';
import { getAddress, isAuthed } from '../../lib/session';
import { Clipboard } from '../../lib/clipboard';
import { useT } from '../../i18n';
import { asset, pagePath } from '../../lib/asset';


type LeaderRow = {
  rank: number;
  user: string;
  total_nim: number;
  burned_nim?: number;
  wallet_nim?: number;
  orders: number;
};

const RANK_CLS = ['gold', 'silver', 'bronze'];

function fmtNimUser(n: number): string {
  if (!isFinite(n) || n <= 0) return '0';
  if (n >= 10) return Math.round(n).toLocaleString('en-US');
  const v = Math.round(n * 10) / 10;
  return Number.isInteger(v) ? String(v) : v.toFixed(1);
}

function formatNimCompact(n: number): string {
  if (!isFinite(n) || n <= 0) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 10_000) return (n / 1_000).toFixed(1) + 'k';
  if (n >= 10) return Math.round(n).toLocaleString('en-US');
  if (n < 1) return n.toFixed(2);
  return n.toFixed(1);
}

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

function rich(value: string) {
  return value.split('**').map((part, i) => (i % 2 ? <strong key={i}>{part}</strong> : part));
}

export type MyCashbackTotalsProp = {
  paid_nim?: number;
  paid_count?: number;
  pending_nim?: number;
  pending_count?: number;
  earned_nim?: number;
  burned_nim?: number;
  burned_count?: number;
  wallet_nim?: number;
  wallet_count?: number;
  orders?: number;
  preference?: string;
} | null;

export function CashbackImpactSection({
  authed,
  myTotals,
  between,
}: {
  authed: boolean;
  myTotals: MyCashbackTotalsProp;
  /**
   * Rendered BETWEEN the personal card and the leaderboard — the Cashback page
   * passes its compact "your cashback right now" strip here, so the page order
   * (card → current rate → board) lives in ONE place (owner, 2026-10-05).
   */
  between?: ReactNode;

}) {
  const { t } = useT();
  const [bucket, setBucket] = useState<'week' | 'month' | 'all'>('all');
  const [data, setData] = useState<any>(null);
  const [copied, setCopied] = useState(false);
  const [address, setAddress] = useState<string>('');
  const [leaderPage, setLeaderPage] = useState(0);
  const [shareImg, setShareImg] = useState<string | null>(null);
  const shareBlobRef = useRef<Blob | null>(null);

  useEffect(() => {
    if (isAuthed()) setAddress(getAddress() || '');
    else setAddress('');
  }, [authed]);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const lbRes = await getCashbackLeaderboard(bucket).catch(() => null);
        if (!alive) return;
        if (lbRes) setData(lbRes);
      } catch {
        /* best-effort */
      }
    })();
    return () => { alive = false; };
  }, [bucket]);

  const totals = data?.totals || {
    total_nim: 0,
    paid_nim: 0,
    pending_nim: 0,
    burned_nim: 0,
    wallet_nim: 0,
    orders: 0,
  };
  const globalEarned = Number(totals.total_nim || 0);
  const globalBurned = Number(totals.burned_nim || 0);
  const globalWallet = Number(totals.wallet_nim ?? Math.max(0, globalEarned - globalBurned));
  const globalOrders = Number(totals.orders || 0);

  const leaders: LeaderRow[] = data?.leaderboard || [];
  const LEADER_PER_PAGE = 5;
  const leaderPages = Math.max(1, Math.ceil(leaders.length / LEADER_PER_PAGE));
  const leaderPageSafe = Math.min(leaderPage, leaderPages - 1);
  const visibleLeaders = leaders.slice(leaderPageSafe * LEADER_PER_PAGE, leaderPageSafe * LEADER_PER_PAGE + LEADER_PER_PAGE);

  const myPaid = Number(myTotals?.paid_nim || 0);
  const myPending = Number(myTotals?.pending_nim || 0);
  const myEarned = Number(myTotals?.earned_nim ?? (myPaid + myPending));
  const myBurned = Number(myTotals?.burned_nim || 0);
  const myWallet = Number(myTotals?.wallet_nim ?? Math.max(0, myEarned - myBurned));
  const myOrders = Number(myTotals?.orders ?? ((Number(myTotals?.paid_count) || 0) + (Number(myTotals?.pending_count) || 0)));

  const cleanUser = String(address || '').replace(/[\s-]/g, '').toUpperCase();
  const myLeaderIndex = cleanUser
    ? leaders.findIndex((r) => {
        const rc = String(r.user || '').replace(/[\s-]/g, '').toUpperCase();
        return rc === cleanUser || cleanUser.startsWith(rc);
      })
    : -1;
  const myLeaderRank = myLeaderIndex >= 0 ? leaders[myLeaderIndex].rank : null;

  const shareUrl = useMemo(() => {
    if (typeof window === 'undefined') return siteURL() + '/cashback';
    return window.location.origin + pagePath('/cashback');
  }, []);

  const myEarnedStr = fmtNimUser(myEarned);
  const myWalletStr = fmtNimUser(myWallet);

  const shareText = useMemo(() => {
    return t('cashbackCard.shareCaption', {
      count: myOrders,
      nim: myEarnedStr,
      site: siteName(),
      url: shareUrl,
    });
  }, [t, myOrders, myEarnedStr, shareUrl]);

  const nativeShare = async () => {
    if (navigator.share) {
      try {
        await navigator.share({
          title: t('cashbackCard.shareTitle', { site: siteName() }),
          text: shareText,
          url: shareUrl,
        });
        return;
      } catch {
        /* cancelled */
      }
    }
    copyShare();
  };

  const copyShare = () => {
    if (Clipboard.copy(shareText)) {
      setCopied(true);
      // DS172411 (setTimeout): closure only, never a string — no untrusted data is evaluated.
      setTimeout(() => setCopied(false), 2200);
    }
  };

  const openTwitter = () =>
    window.open(`https://twitter.com/intent/tweet?text=${encodeURIComponent(shareText)}`, '_blank', 'noopener,noreferrer');
  const openFacebook = () =>
    window.open(
      `https://www.facebook.com/sharer/sharer.php?u=${encodeURIComponent(shareUrl)}&quote=${encodeURIComponent(shareText)}`,
      '_blank',
      'noopener,noreferrer',
    );

  const makeShareImage = async (): Promise<Blob> => {
    const W = 1080,
      H = 1350;
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
    const lang =
      (typeof document !== 'undefined' && (document.documentElement.getAttribute('data-lang') || document.documentElement.lang)) ||
      'en';
    const upper = (s: string) => {
      try {
        return s.toLocaleUpperCase(lang);
      } catch {
        return s.toUpperCase();
      }
    };

    try {
      const fonts = (document as any).fonts;
      if (fonts?.load) {
        await Promise.race([
          Promise.all([
            fonts.load(`900 40px ${serif}`),
            fonts.load(`700 40px ${serif}`),
            fonts.load(`900 20px ${font}`),
            fonts.load(`800 20px ${font}`),
            fonts.load(`700 20px ${font}`),
          ]),
          new Promise((r) => setTimeout(r, 2500)),
        ]);
      }
    } catch {}

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
    const setFont = (size: number, weight: number, family: string) => {
      ctx.font = `${weight} ${size}px ${family}`;
    };
    const measure = (value: string, size: number, weight: number, family = font) => {
      setFont(size, weight, family);
      return ctx.measureText(value).width;
    };
    const fitSize = (value: string, maxW: number, size: number, weight: number, family = font, min = Math.round(size * 0.6)) => {
      let s = size;
      while (s > min && measure(value, s, weight, family) > maxW) s -= 1;
      return s;
    };
    const wrap = (value: string, maxW: number, size: number, weight: number, family = font): string[] => {
      setFont(size, weight, family);
      const words = String(value).split(/\s+/).filter(Boolean);
      const lines: string[] = [];
      let line = '';
      for (const w of words) {
        const next = line ? line + ' ' + w : w;
        if (!line || ctx.measureText(next).width <= maxW) line = next;
        else {
          lines.push(line);
          line = w;
        }
      }
      if (line) lines.push(line);
      return lines.length ? lines : [''];
    };
    const fitBlock = (
      value: string,
      maxW: number,
      size: number,
      weight: number,
      maxLines: number,
      family = font,
      min = Math.round(size * 0.62),
    ) => {
      let s = size;
      for (;;) {
        const ls = wrap(value, maxW, s, weight, family);
        const widest = Math.max(...ls.map((l) => measure(l, s, weight, family)));
        if ((ls.length <= maxLines && widest <= maxW) || s <= min) {
          if (ls.length > maxLines) {
            const kept = ls.slice(0, maxLines);
            let last = kept[maxLines - 1] + ' ' + ls.slice(maxLines).join(' ');
            while (last.length > 1 && measure(last + '…', s, weight, family) > maxW) last = last.slice(0, -1);
            kept[maxLines - 1] = last.trimEnd() + '…';
            return { size: s, lines: kept };
          }
          return { size: s, lines: ls };
        }
        s -= 1;
      }
    };
    const text = (
      value: string,
      x: number,
      y: number,
      size: number,
      color: string,
      weight = 700,
      align: CanvasTextAlign = 'left',
      family = font,
    ) => {
      setFont(size, weight, family);
      ctx.fillStyle = color;
      ctx.textAlign = align;
      ctx.textBaseline = 'alphabetic';
      ctx.fillText(value, x, y);
    };
    const lines = (
      ls: string[],
      x: number,
      y: number,
      size: number,
      lh: number,
      color: string,
      weight = 700,
      align: CanvasTextAlign = 'left',
      family = font,
    ) => {
      ls.forEach((l, i) => text(l, x, y + i * lh, size, color, weight, align, family));
      return y + (ls.length - 1) * lh;
    };
    const iconPaths: Record<string, string[]> = {
      spark: [
        'M12 3l1.9 5.8a2 2 0 0 0 1.3 1.3L21 12l-5.8 1.9a2 2 0 0 0-1.3 1.3L12 21l-1.9-5.8a2 2 0 0 0-1.3-1.3L3 12l5.8-1.9a2 2 0 0 0 1.3-1.3L12 3z',
      ],
      wallet: [
        'M19 7V4a1 1 0 0 0-1-1H5a2 2 0 0 0 0 4h15a1 1 0 0 1 1 1v4h-3a2 2 0 0 0 0 4h3a1 1 0 0 0 1-1v-2a1 1 0 0 0-1-1',
        'M3 5v14a2 2 0 0 0 2 2h15a1 1 0 0 0 1-1v-4',
      ],
      bolt: ['M13 2 3 14h9l-1 8 10-12h-9l1-8z'],
      clock: ['M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0Z', 'M12 6v6l4 2'],
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
    const L = 92,
      R = 988,
      CW = R - L;
    const dashed = (y: number) => {
      ctx.setLineDash([10, 9]);
      ctx.strokeStyle = 'rgba(78,61,40,.42)';
      ctx.lineWidth = 2;
      ctx.beginPath();
      ctx.moveTo(L, y);
      ctx.lineTo(R, y);
      ctx.stroke();
      ctx.setLineDash([]);
    };

    ctx.fillStyle = kraft;
    ctx.fillRect(0, 0, W, H);
    ctx.strokeStyle = 'rgba(78,61,40,.06)';
    ctx.lineWidth = 1;
    for (let y = 0; y < H; y += 12) {
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(W, y);
      ctx.stroke();
    }
    fillRound(62, 62, 972, 1242, 18, 'rgba(78,61,40,.16)');
    fillRound(54, 54, 972, 1242, 18, paper);
    ctx.save();
    roundRect(ctx, 54, 54, 972, 1242, 18);
    ctx.clip();
    ctx.fillStyle = stamp;
    ctx.fillRect(54, 54, 972, 12);
    ctx.restore();
    strokeRound(54, 54, 972, 1242, 18, ink, 3);

    const serial = t('cashbackCard.canvasSerial');
    const route = t('cashbackCard.canvasCashbackRoute');
    const rightW = Math.min(300, Math.max(measure(serial, 14, 900), measure(route, 14, 900)));
    const serialSize = fitSize(serial, rightW, 14, 900, font, 11);
    const routeSize = fitSize(route, rightW, 14, 900, font, 11);
    text(serial, R, 112, serialSize, inkFaint, 900, 'right');
    text(route, R, 140, routeSize, green, 900, 'right');
    const brandX = logo ? 170 : L;
    if (logo) ctx.drawImage(logo, L, 94, 58, 58);
    const brandMaxW = R - rightW - 28 - brandX;
    const brandName = siteName().toUpperCase();
    text(brandName, brandX, 127, fitSize(brandName, brandMaxW, 29, 900, serif, 16), ink, 900, 'left', serif);
    const receipt = t('cashbackCard.canvasImpactReceipt');
    text(receipt, brandX, 155, fitSize(receipt, brandMaxW, 16, 900, font, 11), inkFaint, 900);
    dashed(190);

    const idMaxW = (avatar ? 884 - 24 : R) - L;
    const yourCard = t('cashbackCard.canvasYourCard');
    text(yourCard, L, 246, fitSize(yourCard, idMaxW, 18, 900, font, 12), inkFaint, 900);
    const sub = fitBlock(t('cashbackCard.canvasCardSub'), idMaxW, 30, 800, 2, serif, 20);
    let y = lines(sub.lines, L, 283, sub.size, Math.round(sub.size * 1.15), ink, 800, 'left', serif);
    if (avatar) {
      fillRound(884, 214, 72, 72, 10, white);
      ctx.drawImage(avatar, 890, 220, 60, 60);
      strokeRound(884, 214, 72, 72, 10, ink, 2);
    }
    y = Math.max(y, 290);

    const heroX = L,
      heroW = CW,
      padX = 38;
    const innerW = heroW - padX * 2;
    const heroTop = y + 30;
    const contribution = t('cashbackCard.canvasContribution');
    const earnedLabel = t('cashbackCard.canvasNimEarned');
    const ordersLine = t('cashbackCard.cashbackOrder', { count: myOrders });
    const labelIcon = 34,
      labelGap = 14,
      colGap = 44;
    const labelMaxW = 360;
    const fundedBlock = fitBlock(earnedLabel, labelMaxW - labelIcon - labelGap, 28, 900, 2, font, 20);
    const fundedW = Math.max(...fundedBlock.lines.map((l) => measure(l, fundedBlock.size, 900))) + labelIcon + labelGap;
    const ordersSize = fitSize(ordersLine, labelMaxW - labelIcon - labelGap, 20, 700, font, 15);
    const labelBlockW = Math.max(fundedW, measure(ordersLine, ordersSize, 700) + labelIcon + labelGap);
    let numSize = 152;
    const sideBySideRoom = innerW - labelBlockW - colGap;
    while (numSize > 96 && measure(myEarnedStr, numSize, 900, serif) > sideBySideRoom) numSize -= 2;
    const sideBySide = measure(myEarnedStr, numSize, 900, serif) <= sideBySideRoom;
    if (!sideBySide) numSize = fitSize(myEarnedStr, innerW, 152, 900, serif, 68);
    const fundedLH = Math.round(fundedBlock.size * 1.12);
    const labelBlockH = fundedLH * fundedBlock.lines.length + 12 + ordersSize;
    const numTop = heroTop + 76;
    const numBase = numTop + Math.round(numSize * 0.74);
    const labelTop = sideBySide ? numBase - Math.round(numSize * 0.37) - Math.round(labelBlockH / 2) : numBase + 34;
    const destination = t('cashbackCard.canvasWalletSwitch');
    const destBlock = fitBlock(destination, innerW - 36, 18, 700, 2, font, 14);
    const destLH = Math.round(destBlock.size * 1.3);
    const ruleY = (sideBySide ? numBase : labelTop + labelBlockH) + 38;
    const heroH = ruleY - heroTop + 22 + destLH * destBlock.lines.length + 18;

    const status = myEarned > 0 ? t('cashbackCard.statusEarned') : t('cashbackCard.canvasStatusChoose');
    const stBlock = fitBlock(status, CW - 72 - 28, 19, 800, 2, font, 14);
    const stLH = Math.round(stBlock.size * 1.35);
    const stH = Math.max(72, 34 + stLH * stBlock.lines.length);
    const footBottom = 1214;
    const tag = fitBlock(t('cashbackCard.canvasTagline'), CW, 18, 700, 2, font, 14);
    const tagLH = Math.round(tag.size * 1.35);
    const tagTop = footBottom - 58 - tagLH * (tag.lines.length - 1);
    const footRule = tagTop - 124;
    const tileH = 128;
    const projected = heroTop + heroH + 80 + tileH + 30 + stH + 36;
    const extra = Math.max(0, Math.min(34, (footRule - projected) / 3));
    fillRound(heroX, heroTop, heroW, heroH, 14, green);
    const cx = heroX + padX;
    text(contribution, cx, heroTop + 50, fitSize(contribution, innerW, 15, 900, font, 11), 'rgba(255,246,232,.72)', 900);
    text(myEarnedStr, cx - 4, numBase, numSize, white, 900, 'left', serif);
    const labelX = sideBySide ? cx + measure(myEarnedStr, numSize, 900, serif) + colGap : cx;
    drawIcon('spark', labelX, labelTop + Math.round((fundedLH - labelIcon) / 2), labelIcon, white, 1.8);
    const fx = labelX + labelIcon + labelGap;
    const fundedLast = lines(fundedBlock.lines, fx, labelTop + fundedBlock.size, fundedBlock.size, fundedLH, white, 900);
    text(ordersLine, fx, fundedLast + 12 + ordersSize + 4, ordersSize, 'rgba(255,246,232,.78)', 700);
    ctx.strokeStyle = 'rgba(255,246,232,.26)';
    ctx.lineWidth = 2;
    ctx.beginPath();
    ctx.moveTo(cx, ruleY);
    ctx.lineTo(heroX + heroW - padX, ruleY);
    ctx.stroke();
    drawIcon('wallet', cx, ruleY + 20, 22, 'rgba(255,246,232,.8)', 1.7);
    lines(destBlock.lines, cx + 36, ruleY + 22 + destBlock.size, destBlock.size, destLH, 'rgba(255,246,232,.8)', 700);
    y = heroTop + heroH;

    const fpLabel = upper(t('cashbackCard.breakdown'));
    text(fpLabel, L, y + extra + 56, fitSize(fpLabel, CW, 16, 900, font, 12), inkFaint, 900);
    y += extra;
    const tileTop = y + 80,
      tileW = CW;
    const drawTile = (tx: number, icon: string, label: string, val: string) => {
      fillRound(tx, tileTop, tileW, tileH, 12, recess);
      strokeRound(tx, tileTop, tileW, tileH, 12, 'rgba(78,61,40,.22)', 2);
      fillRound(tx + 22, tileTop + 28, 52, 52, 10, 'rgba(47,85,64,.12)');
      drawIcon(icon, tx + 35, tileTop + 41, 26, green, 2);
      const txL = tx + 92,
        txW = tileW - 92 - 20;
      const cap = upper(label);
      text(cap, txL, tileTop + 48, fitSize(cap, txW, 13, 900, font, 10), inkFaint, 900);
      text(val, txL, tileTop + 92, fitSize(val, txW, 34, 900, serif, 20), ink, 900, 'left', serif);
    };
    drawTile(L, 'wallet', t('cashbackCard.toWalletLabel'), `${myWalletStr} NIM`);
    y = tileTop + tileH;

    y += 30 + extra;
    fillRound(L, y, CW, stH, 10, 'rgba(199,72,29,.09)');
    ctx.fillStyle = stamp;
    ctx.fillRect(L, y, 6, stH);
    drawIcon('clock', L + 26, y + Math.round((stH - 24) / 2), 24, stamp, 2);
    lines(stBlock.lines, L + 66, y + 24 + stBlock.size, stBlock.size, stLH, inkDim, 800);
    y += stH;

    const host = siteName();
    text(host, L, footBottom, fitSize(host, CW - 220, 26, 900, serif, 18), stamp, 900, 'left', serif);
    lines(tag.lines, L, tagTop, tag.size, tagLH, inkDim, 700);
    const from = upper(t('cashbackCard.canvasReceiptFrom'));
    text(from, L, tagTop - 78, fitSize(from, CW, 13, 900, font, 10), inkFaint, 900);
    dashed(Math.max(y + 36, footRule));

    return await new Promise<Blob>((resolve, reject) => {
      canvas.toBlob((b) => (b ? resolve(b) : reject(new Error('toBlob failed'))), 'image/png');
    });
  };

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

  const downloadShareImage = async (e: { preventDefault: () => void }) => {
    const blob = shareBlobRef.current;
    if (!blob || typeof navigator === 'undefined') return;
    const ua = navigator.userAgent || '';
    const noDownloadAttr =
      /iPad|iPhone|iPod/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1) || Boolean((window as any).nimiqPay);
    if (!noDownloadAttr) return;
    const file = new File([blob], 'nimshop-cashback-card.png', { type: 'image/png' });
    if (navigator.canShare && navigator.canShare({ files: [file] })) {
      e.preventDefault();
      try {
        await navigator.share({ files: [file] });
      } catch {}
    }
  };

  const closeShareImage = () => {
    shareBlobRef.current = null;
    setShareImg((prev) => {
      if (prev && prev.startsWith('blob:')) URL.revokeObjectURL(prev);
      return null;
    });
  };

  useEffect(() => {
    if (!shareImg || typeof document === 'undefined') return;
    const html = document.documentElement,
      body = document.body;
    const prevHtml = html.style.overflow,
      prevBody = body.style.overflow;
    html.style.overflow = 'hidden';
    body.style.overflow = 'hidden';
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeShareImage();
    };
    window.addEventListener('keydown', onKey);
    return () => {
      html.style.overflow = prevHtml;
      body.style.overflow = prevBody;
      window.removeEventListener('keydown', onKey);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shareImg]);

  return (
    <div className="pt-page" style={{ padding: '14px 0 0' }}>
      {/* ------------------------------------------------ personal cashback card */}
      {authed ? (
        <section className="pt-card pt-impact" aria-label={t('cashbackCard.impactAria')}>
          <div className="pt-impact-shell">
            <div className="pt-impact-bar">
              <div className="pt-impact-bar-brand">
                <span className="pt-impact-logo">
                  <Icon name="spark" size={21} />
                </span>
                <div className="pt-impact-bar-copy">
                  <div className="pt-impact-kicker">{t('cashbackCard.kicker', { site: siteName() })}</div>
                  <h2 className="pt-impact-title">{t('cashbackCard.impactTitle')}</h2>
                </div>
              </div>
              <span className={`pt-impact-stamp${myEarned > 0 ? '' : ' is-empty'}`}>
                <Icon name="spark" size={14} /> {myEarned > 0 ? t('cashbackCard.stampEarned') : t('cashbackCard.stampReady')}
              </span>
            </div>

            <div className="pt-impact-summary">
              <div className="pt-impact-total-copy">
                <div className="pt-impact-kicker">{t('cashbackCard.myContribution')}</div>
                <div className="pt-impact-count-row">
                  <span className="pt-impact-count">{myEarnedStr}</span>
                  <span className="pt-impact-count-label">
                    <Icon name="spark" size={28} />
                    <span>{t('cashbackCard.nimEarned')}</span>
                  </span>
                </div>
                <div className="pt-impact-orders">
                  <span className="pt-impact-order-item">
                    <Icon name="receipt" size={14} /> {t('cashbackCard.cashbackOrder', { count: myOrders })}
                  </span>
                  <span className="pt-impact-order-destination">
                    {address ? <Identicon address={address} size={20} /> : <Icon name="wallet" size={15} />}
                    {t('cashbackCard.destWallet')}
                  </span>
                </div>
              </div>
            </div>

            <div className="pt-impact-metrics-label">{t('cashbackCard.breakdown')}</div>
            <div className="pt-impact-metrics">
              <div className="pt-impact-metric">
                <span className="pt-impact-metric-icon">
                  <Icon name="wallet" size={20} />
                </span>
                <span>
                  <small>{t('cashbackCard.toWalletLabel')}</small>
                  <strong>{myWalletStr} NIM</strong>
                </span>
              </div>
            </div>

            {myEarned > 0 ? (
              <div className="pt-impact-status">
                <Icon name="wallet" size={17} />
                <span>{t('cashbackCard.statusEarned')}</span>
              </div>
            ) : (
              <div className="pt-impact-status">
                <Icon name="spark" size={17} />
                <span>{t('cashbackCard.statusChoose')}</span>
              </div>
            )}

            <div className="pt-impact-share-head">
              <div>
                <div className="pt-impact-metrics-label">{t('cashbackCard.shareHead')}</div>
                <div className="pt-impact-share-copy">{t('cashbackCard.shareHeadCopy')}</div>
              </div>
              <Icon name="share" size={22} />
            </div>
            <div className="pt-actions">
              <ShareBtn onClick={openShareImage} icon="download" label={t('cashbackCard.downloadPng')} primary />
              <ShareBtn onClick={nativeShare} icon="share" label={t('cashbackCard.share')} />
              <ShareBtn onClick={openTwitter} brand="x" label={t('cashbackCard.postOnX')} />
              <ShareBtn onClick={openFacebook} brand="fb" label={t('cashbackCard.facebook')} />
              <ShareBtn onClick={copyShare} icon="link" label={copied ? t('actions.copied') : t('cashbackCard.copyLink')} />
            </div>
            {copied && (
              <div className="small pt-value pt-icon-inline pt-copied">
                <Icon name="check" size={15} /> {t('cashbackCard.copiedNote')}
              </div>
            )}
          </div>
        </section>
      ) : (
        <section className="pt-card pt-impact" aria-label={t('cashbackCard.impactAria')}>
          <div className="pt-impact-shell">
            <div className="pt-impact-bar">
              <div className="pt-impact-bar-brand">
                <span className="pt-impact-logo">
                  <Icon name="spark" size={21} />
                </span>
                <div className="pt-impact-bar-copy">
                  <div className="pt-impact-kicker">{t('cashbackCard.kicker', { site: siteName() })}</div>
                  <h2 className="pt-impact-title">{t('cashbackCard.impactTitle')}</h2>
                </div>
              </div>
              <span className="pt-impact-stamp is-empty">
                <Icon name="spark" size={14} /> {t('cashbackCard.stampReady')}
              </span>
            </div>
            <div className="pt-impact-empty">
              <div className="pt-icon-inline" style={{ justifyContent: 'center', color: 'var(--green-600)', marginTop: 8 }}>
                <Icon name="spark" size={32} />
              </div>
              <div style={{ textAlign: 'center' }}>
                <div className="strong" style={{ fontSize: '1.1rem' }}>
                  {t('cashbackCard.connectTitle')}
                </div>
                <div className="small muted" style={{ marginTop: 6, maxWidth: 420, marginInline: 'auto' }}>
                  {t('cashbackCard.connectBody')}
                </div>
              </div>
              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', justifyContent: 'center', marginTop: 4 }}>
                <span className="chip">
                  <Icon name="spark" size={13} /> {t('cashbackCard.emptyChipEarned')}
                </span>
              </div>
            </div>
          </div>
        </section>
      )}

      {between}

      {/* ------------------------------------------------ leaderboard */}
      <section className="pt-card">
        <div className="pt-card-head">
          <h2 className="pt-section-title">
            <Icon name="trophy" size={20} /> {t('cashbackCard.leaderboard')}
          </h2>
          <div className="pt-seg">
            {(['week', 'month', 'all'] as const).map((b) => (
              <button
                key={b}
                className={bucket === b ? 'btn btn-sm btn-gold' : 'btn btn-sm btn-outline'}
                onClick={() => {
                  setBucket(b);
                  setLeaderPage(0);
                }}
              >
                {b === 'week' ? t('cashbackCard.thisWeek') : b === 'month' ? t('cashbackCard.thisMonth') : t('cashbackCard.allTime')}
              </button>
            ))}
          </div>
        </div>

        {/* Global programme totals: a compact summary line for the board below.
            They used to be their own full-width row at the very top of the
            page, which pushed the owner's card/rate/board order down. */}
        <div className="pt-stats pt-stats-inline">
          <Stat icon="spark" value={`${formatNimCompact(globalEarned)} NIM`} label={t('cashbackCard.statEarned')} />
          <Stat icon="wallet" value={`${formatNimCompact(globalWallet)} NIM`} label={t('cashbackCard.statWallet')} />
          <Stat icon="gift" value={String(globalOrders)} label={t('cashbackCard.statOrders')} />
        </div>

        {authed && cleanUser && (
          <div className="pt-yourank">
            {address && <Identicon address={address} size={34} className="pt-ident-lg" />}
            <div className="pt-yourank-info">
              <div className="xs faint">{t('cashbackCard.yourStanding')}</div>
              <div className="pt-yourank-line">
                {myLeaderRank
                  ? rich(
                      t('cashbackCard.youRank', {
                        rank: myLeaderRank,
                        nim: myEarnedStr,
                        orders: t('cashbackCard.orders', { count: myOrders }),
                      }),
                    )
                  : rich(
                      t('cashbackCard.youFunded', {
                        nim: myEarnedStr,
                        orders: t('cashbackCard.orders', { count: myOrders }),
                        outside: leaders.length > 0 ? t('cashbackCard.outsideTop') : '',
                      }),
                    )}
              </div>
            </div>
          </div>
        )}

        {leaders.length === 0 ? (
          <p className="small muted center" style={{ margin: 0 }}>
            {t('cashbackCard.noCashbackYet')}
          </p>
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
                  <code className="pt-leader-code" title={row.user}>
                    {shortAddress(row.user)}
                  </code>
                  <span className="pt-leader-trees pt-icon-inline">
                    {formatNimCompact(row.total_nim)} NIM <Icon name="spark" size={14} />
                  </span>
                  <span className="pt-leader-orders">
                    {t('cashbackCard.orders', { count: row.orders })}
                    {Number(row.burned_nim || 0) > 0 && (
                      <>{t('cashbackCard.burnedSuffix', { nim: formatNimCompact(Number(row.burned_nim || 0)) })}</>
                    )}
                  </span>
                  {row.rank === myLeaderRank && (
                    <span className="pt-you-badge">
                      <Icon name="user" size={12} /> {t('cashbackCard.you')}
                    </span>
                  )}
                </li>
              ))}
            </ol>
            {leaderPages > 1 && (
              <div className="pt-pager">
                <button className="btn btn-sm btn-outline" disabled={leaderPageSafe === 0} onClick={() => setLeaderPage(leaderPageSafe - 1)}>
                  <Icon name="back" size={15} /> {t('actions.back')}
                </button>
                <span className="small muted">
                  {leaderPageSafe + 1} / {leaderPages}
                </span>
                <button
                  className="btn btn-sm btn-outline"
                  disabled={leaderPageSafe >= leaderPages - 1}
                  onClick={() => setLeaderPage(leaderPageSafe + 1)}
                >
                  {t('cashbackCard.more')} <Icon name="chevron" size={15} />
                </button>
              </div>
            )}
          </>
        )}
      </section>

      {/* ------------------------------------------------ share-image preview */}
      {shareImg && (
        <div className="pt-overlay" role="dialog" aria-modal="true" aria-label={t('cashbackCard.overlayHead')} onClick={closeShareImage}>
          <div className="pt-overlay-box" onClick={(e) => e.stopPropagation()}>
            <div className="pt-overlay-head">
              <div>
                <div className="strong">{t('cashbackCard.overlayHead')}</div>
                <div className="xs muted">{t('cashbackCard.shareHeadCopy')}</div>
              </div>
              <button className="btn btn-sm btn-ghost" onClick={closeShareImage} aria-label={t('actions.close')}>
                <Icon name="x" size={16} /> <span className="pt-overlay-close-label">{t('actions.close')}</span>
              </button>
            </div>
            <div className="pt-overlay-preview">
              <img src={shareImg} width={1080} height={1350} alt={t('cashbackCard.shareImgAlt', { site: siteName() })} />
            </div>
            <div className="pt-caption">
              <div className="pt-caption-label">
                <Icon name="copy" size={14} /> {t('cashbackCard.postCaption')}
              </div>
              <p className="pt-caption-text">{shareText}</p>
              <button
                className="btn btn-sm btn-ghost"
                onClick={() => {
                  if (Clipboard.copy(shareText)) {
                    setCopied(true);
                    // DS172411 (setTimeout): closure only, never a string — no untrusted data is evaluated.
                    setTimeout(() => setCopied(false), 2200);
                  }
                }}
              >
                <Icon name="copy" size={14} /> {copied ? t('cashbackCard.copiedCheck') : t('cashbackCard.copyCaption')}
              </button>
            </div>
            <div className="pt-overlay-actions">
              <a className="btn btn-gold" href={shareImg} download="nimshop-cashback-card.png" onClick={downloadShareImage}>
                <Icon name="download" size={16} /> {t('cashbackCard.downloadPng')}
              </a>
              <button
                className="btn btn-gold"
                onClick={async () => {
                  try {
                    const blob = shareBlobRef.current || (await makeShareImage());
                    const file = new File([blob], 'nimshop-cashback-card.png', { type: 'image/png' });
                    if (navigator.canShare && navigator.canShare({ files: [file] })) {
                      await navigator.share({
                        files: [file],
                        title: t('cashbackCard.myImpact', { site: siteName() }) + ' ⚡',
                        text: shareText,
                      });
                      return;
                    }
                    Clipboard.copy(shareText);
                    window.open('https://twitter.com/intent/tweet?text=' + encodeURIComponent(shareText), '_blank', 'noopener,noreferrer');
                  } catch {
                    Clipboard.copy(shareText);
                    window.open('https://twitter.com/intent/tweet?text=' + encodeURIComponent(shareText), '_blank', 'noopener,noreferrer');
                  }
                }}
              >
                <Icon name="share" size={16} /> {t('cashbackCard.postImageCaption')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function ShareBtn({
  onClick,
  icon,
  brand,
  label,
  primary,
}: {
  onClick: () => void;
  icon?: string;
  brand?: 'x' | 'fb';
  label: string;
  primary?: boolean;
}) {
  const glyph = brand === 'x' ? '𝕏' : brand === 'fb' ? 'f' : null;
  return (
    <button
      type="button"
      onClick={onClick}
      className={'btn btn-sm' + (primary ? ' btn-gold' : ' btn-outline')}
      style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}
    >
      {icon ? <Icon name={icon as any} size={14} /> : <span style={{ fontWeight: 800, fontSize: '0.9rem' }}>{glyph}</span>}
      {label}
    </button>
  );
}

function Stat({ icon, label, value }: { icon: string; label: string; value: string }) {
  return (
    <div className="pt-stat">
      <span className="pt-stat-ico">
        <Icon name={icon as any} size={22} />
      </span>
      <span className="pt-stat-num">{value}</span>
      <span className="pt-stat-label">{label}</span>
    </div>
  );
}

function shortAddress(addr: string): string {
  return canonicalIdenticonInput(addr);
}
