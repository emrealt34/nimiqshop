import { useEffect } from 'react';
import { getRatingConfig, listOrders, listQuotes } from '../../lib/api';
import { getUid, isAuthed, subscribeSession } from '../../lib/session';
import { RATING_REMINDER_COOLDOWN_MS, ratingReminderKey, unratedPurchases, type UnratedPurchase } from '../../lib/ratingReminder';
import { pagePath } from '../../lib/asset';
import { useT } from '../../i18n';
import { useSheet } from '../AppProviders';

const cooldowns = new Map<string, number>();
function snoozed(key: string): boolean {
  let until = cooldowns.get(key) || 0;
  try { until = Math.max(until, Number(localStorage.getItem(key)) || 0); } catch {}
  return until > Date.now();
}
function snooze(key: string) {
  const until = Date.now() + RATING_REMINDER_COOLDOWN_MS;
  cooldowns.set(key, until);
  try { localStorage.setItem(key, String(until)); } catch {}
}
function ReminderBody({ purchases, close }: { purchases: UnratedPurchase[]; close: () => void }) {
  const { t } = useT();
  return <div>
    <p>{t('ratingReminder.body', { count: purchases.length })}</p>
    <p className="small muted">{t('ratingReminder.optional')}</p>
    <div className="mt-2" style={{ display: 'grid', gap: 10 }}>{purchases.slice(0, 3).map(purchase =>
      <a className="btn btn-outline" key={`${purchase.kind}:${purchase.id}`} onClick={close} href={pagePath(`/order?id=${encodeURIComponent(purchase.id)}${purchase.kind === 'quote' ? '&type=quote' : ''}#purchase-rating`)} style={{ whiteSpace: 'normal', overflowWrap: 'anywhere' }}>
        {t('ratingReminder.rate')} · {purchase.name || t('ratingReminder.purchase')}
      </a>)}</div>
    {purchases.length > 3 && <a className="btn btn-outline btn-block mt-2" href={pagePath('/orders')} onClick={close}>{t('ratingReminder.all')}</a>}
    <button className="btn btn-ghost btn-block mt-2" onClick={close}>{t('ratingReminder.later')}</button>
  </div>;
}

/** Storefront only. Authenticated, bounded reads once per eligible visit;
 * no polling, no rating submission or wallet interaction from the reminder. */
export function RatingReminder() {
  const { openSheet, closeSheet, hasOpenSheet } = useSheet();
  useEffect(() => {
    let disposeCheck = () => {};
    let sheetId = 0;
    let checkedUid: string | undefined;
    let pending: { uid: string; purchases: UnratedPurchase[] } | undefined;
    const showPending = () => {
      if (!pending || sheetId || hasOpenSheet() || !isAuthed() || getUid() !== pending.uid) return;
      const { uid, purchases } = pending;
      pending = undefined;
      purchases.forEach(purchase => snooze(ratingReminderKey(uid, purchase)));
      sheetId = openSheet({ title: t => t('ratingReminder.title'), render: close => <ReminderBody purchases={purchases} close={close} /> });
    };
    const check = () => {
      const uid = isAuthed() ? getUid() : '';
      // Session bootstrap renews metadata for the same buyer. It must not
      // cancel their in-flight check or close an already visible reminder.
      if (uid === checkedUid) return;
      checkedUid = uid;
      disposeCheck();
      pending = undefined;
      if (sheetId) { closeSheet(sheetId); sheetId = 0; }
      if (!uid) return;
      let cancelled = false;
      disposeCheck = () => { cancelled = true; };
      void (async () => {
        try {
          const cfg: any = await getRatingConfig();
          if (!cfg?.enabled || cancelled) return;
          const [orders, quotes] = await Promise.all([listOrders(), listQuotes()]);
          if (cancelled || !isAuthed() || getUid() !== uid) return;
          const purchases = unratedPurchases(orders, quotes).filter(purchase => !snoozed(ratingReminderKey(uid, purchase)));
          if (!purchases.length) return;
          // Snooze when shown, including X/backdrop/Escape dismissal and
          // navigation to the purchase. No repeated nagging on every visit.
          pending = { uid, purchases };
          showPending();
        } catch { /* An unavailable history must never block shopping. */ }
      })();
    };
    check();
    const unsubscribe = subscribeSession(check);
    window.addEventListener('nimshop:sheets-changed', showPending);
    return () => { disposeCheck(); unsubscribe(); window.removeEventListener('nimshop:sheets-changed', showPending); if (sheetId) closeSheet(sheetId); };
  }, [openSheet, closeSheet, hasOpenSheet]);
  return null;
}
