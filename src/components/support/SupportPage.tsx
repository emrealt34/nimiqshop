/**
 * SupportPage.tsx — React port of pages/support.js: support center with ticket
 * list, conversation + live polling, and a new-ticket form bound to real
 * orders/quotes.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { Icon } from '../ui/Icon';
import { AppRoot } from '../AppRoot';
import { openLoginSheet } from '../shell/SiteShell';
import { listTickets, getTicket, replyTicket, createTicket, updateTicketStatus, listOrders, listQuotes, friendlyApiMessage } from '../../lib/api';
import { getAddress } from '../../lib/session';
import { useSession } from '../../lib/useSession';
import { fmtDate, timeAgo, queryParam } from '../../lib/format';
import { useInterval } from '../../lib/useInterval';
import { useToast, useSheet } from '../AppProviders';
import { useT, rich, t as tr } from '../../i18n';
import { StatusBadge, EmptyState, ErrorState, AlertBox, ChatMessageRow, ChatComposer, SkeletonCards } from '../ui/uiKit';
import { useRouter } from '../../lib/router';
import { pagePath } from '../../lib/asset';

const STATUS_LEGEND: Array<[string, string]> = [
  ['open', tr('support.scStatusOpen')],
  ['waiting_admin', tr('support.scStatusWaitingAdmin')],
  ['waiting_user', tr('support.scStatusWaitingUser')],
  ['resolved', tr('support.scStatusResolved')],
  ['closed', 'Closed'],
];

function ticketPayload(res: any) {
  return {
    ticket: (res && res.ticket) || res || {},
    messages: Array.isArray(res && res.messages) ? res.messages : Array.isArray(res && res.ticket && res.ticket.messages) ? res.ticket.messages : [],
  };
}

export function SupportView() {
  const [ticket, setTicket] = useState<any | null>(null);
  const [tickets, setTickets] = useState<any[]>([]);
  const [view, setView] = useState<'list' | 'new' | 'convo'>('list');
  const [msg, setMsg] = useState<any[]>([]);
  const [err, setErr] = useState('');
  const [loading, setLoading] = useState(true);
  const [ticketChoices, setTicketChoices] = useState<any[]>([]);
  const chatRef = useRef<HTMLDivElement>(null);
  const { toast } = useToast();
  const { openSheet, closeSheet } = useSheet();
  const { t } = useT();
  // null = not decided yet (SSR / first paint) → neutral skeleton, never the
  // signed-out card. See lib/useSession.ts.
  const authed = useSession();
  const myAddress = getAddress();

  const ticketId = queryParam('ticket');

  // Coming from an order's "Do you need help?" card lands straight on the new
  // ticket form with that order pre-selected — no extra tapping.
  useEffect(() => {
    if (!ticketId && queryParam('order')) setView('new');
  }, [ticketId]);

  const showTicket = useCallback(
    async (id: string) => {
      setView('convo');
      setLoading(true);
      try {
        const res = await getTicket(id);
        const { ticket: t, messages } = ticketPayload(res);
        setTicket(t);
        setMsg(messages);
        setErr('');
      } catch (e) {
        setErr(friendlyApiMessage(e, t('support.scErrLoadTicket')));
      } finally {
        setLoading(false);
      }
    },
    []
  );

  const showList = useCallback(async () => {
    setView('list');
    setLoading(true);
    setErr('');
    try {
      const raw = await listTickets();
      const t = Array.isArray(raw) ? raw : Array.isArray(raw?.tickets) ? raw.tickets : Array.isArray(raw?.data) ? raw.data : [];
      setTickets(t);
    } catch (e) {
      setErr(friendlyApiMessage(e, t('support.scErrLoadTickets')));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (authed === null) return; // auth not decided yet — wait for the client
    if (!authed) {
      setTicket(null);
      setTickets([]);
      setMsg([]);
      setView('list');
      setLoading(false);
      return;
    }
    if (ticketId) showTicket(ticketId);
    else if (queryParam('order')) { setView('new'); setLoading(false); }
    else showList();
  }, [authed, ticketId, showTicket, showList]);

  const isClosed = ticket && ['closed', 'resolved'].includes(ticket.status);
  useInterval(
    async () => {
      if (view === 'convo' && ticket && !isClosed) {
        try {
          const fresh = await getTicket(ticket.id);
          const { ticket: ft, messages: fm } = ticketPayload(fresh);
          if (fm.length !== msg.length || ft.status !== ticket.status) {
            setTicket(ft);
            setMsg(fm);
          }
        } catch {}
      }
    },
    view === 'convo' && ticket && !isClosed ? 15000 : null,
    [view, ticket]
  );

  useEffect(() => {
    if (chatRef.current) chatRef.current.scrollTop = chatRef.current.scrollHeight;
  }, [msg, view]);

  if (authed === null) {
    // Auth not decided yet (server HTML / first paint): neutral skeleton only.
    return (
      <div className="container support-page">
        <SupportHero />
        <div className="grid" style={{ marginTop: '16px' }}>
          <SkeletonCards n={3} />
        </div>
      </div>
    );
  }

  if (!authed) {
    return (
      <div className="container support-page">
        <SupportHero />
        <div className="card locked fade-in">
          <div className="lock-ico">
            <Icon name="headset" size={34} />
          </div>
          <h2>{t('support.scWeAreHere')}</h2>
          <p>{t('support.scConnectToOpen')}</p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '12px', alignItems: 'center', marginTop: '8px' }}>
            <button className="btn btn-gold btn-lg" onClick={() => openLoginSheet({ openSheet, closeSheet, toast })} style={{ width: '100%', maxWidth: 340, justifyContent: 'center' }}>
              <Icon name="nimiq" size={20} />
              <span className="btn-label">{t('support.scConnectWallet')}</span>
            </button>
            <a href={pagePath("/track")} className="btn btn-ghost" style={{ width: '100%', maxWidth: 340, justifyContent: 'center' }}>{t('support.scTrackNoSignIn')}</a>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="container support-page">
      <SupportHero />
      <div id="content">
        {view === 'list' ? (
          <ListView loading={loading} err={err} tickets={tickets} onNew={() => openNewTicket()} onOpen={showTicket} onRetry={showList} />
        ) : view === 'new' ? (
          <NewTicketView choices={ticketChoices} loadChoices={loadChoices} onBack={() => showList()} />
        ) : (
          <ConvoView
            loading={loading}
            err={err}
            ticket={ticket}
            messages={msg}
            chatRef={chatRef}
            onBack={() => {
              window.history.replaceState(null, '', pagePath('/support'));
              showList();
            }}
            onSend={sendReply}
            onResolve={resolveTicket}
            onReopen={reopenTicket}
            myAddress={myAddress}
          />
        )}
      </div>
    </div>
  );

  function openNewTicket() {
    setView('new');
    setLoading(false);
    setErr('');
  }

  async function loadChoices() {
    setTicketChoices([]);
    setErr('');
    try {
      const [orders, quotes] = await Promise.all([listOrders(), listQuotes().catch(() => [])]);
      setTicketChoices([
        ...(orders || []).map((o: any) => ({ id: o.id, label: `${(o.payload && o.payload.product_name) || o.product_id} — ${fmtDate(o.created_at)}` })),
        ...(quotes || []).map((q: any) => ({ id: q.id, label: t('support.scQuoteLabel', { id: q.product_id, date: fmtDate(q.created_at) }) })),
      ]);
    } catch (e) {
      setErr(friendlyApiMessage(e, t('support.scErrLoadOrders')));
    }
  }

  async function sendReply(text: string) {
    if (!ticket) return;
    try {
      await replyTicket(ticket.id, text);
      const fresh = await getTicket(ticket.id);
      const { ticket: ft, messages: fm } = ticketPayload(fresh);
      setTicket(ft);
      setMsg(fm);
    } catch (err) {
      toast(friendlyApiMessage(err, t('support.scErrSendReply')), 'error');
      throw err;
    }
  }

  // Self-service close: the buyer may mark their own ticket "resolved".
  // status update is validated server-side (resolved | open, ownership).
  async function resolveTicket() {
    if (!ticket) return;
    try {
      await updateTicketStatus(ticket.id, 'resolved');
      toast(t('support.scToastResolved'), 'success');
      const fresh = await getTicket(ticket.id);
      const { ticket: ft, messages: fm } = ticketPayload(fresh);
      setTicket(ft);
      setMsg(fm);
    } catch (err) {
      toast(friendlyApiMessage(err, t('support.scErrClose')), 'error');
      throw err;
    }
  }

  async function reopenTicket() {
    if (!ticket) return;
    try {
      await updateTicketStatus(ticket.id, 'open');
      toast(t('support.scToastReopened'), 'success');
      const fresh = await getTicket(ticket.id);
      const { ticket: ft, messages: fm } = ticketPayload(fresh);
      setTicket(ft);
      setMsg(fm);
    } catch (err) {
      toast(friendlyApiMessage(err, t('support.scErrReopen')), 'error');
      throw err;
    }
  }
}

function SupportHero() {
  const { t } = useT();
  return (
    <div className="support-hero">
      <div className="support-hero-main">
        <div className="sup-ico">
          <Icon name="headset" size={26} />
        </div>
        <div>
          <h1 className="support-title">{t('support.scTitle')}</h1>
          <div className="xs faint mt-1">{t('support.scLede')}</div>
        </div>
      </div>
      <div className="sup-chips">
        <span className="chip">
          <Icon name="bolt" size={13} /> {t('support.chipFastReplies')}
        </span>
        <span className="chip">
          <Icon name="receipt" size={13} /> {t('support.chipOrderOrGeneral')}
        </span>
        <span className="chip">
          <Icon name="lock" size={13} /> {t('support.chipPrivate')}
        </span>
      </div>
    </div>
  );
}

function ListView({ loading, err, tickets, onNew, onOpen, onRetry }: any) {
  const { t } = useT();
  return (
    <>
      <div className="row between mb-2" style={{ flexWrap: 'wrap', gap: '10px' }}>
        <div className="xs muted">{t('support.scListHint')}</div>
        <button className="btn btn-gold" onClick={onNew}>
          <Icon name="plus" size={18} />
          <span className="btn-label">{t('support.scNewTicket')}</span>
        </button>
      </div>
      {loading ? (
        <div className="grid">
          <SkeletonCards n={3} />
        </div>
      ) : err ? (
        <ErrorState message={err} retry={onRetry} />
      ) : !tickets.length ? (
        <EmptyState
          iconName="headset"
          title={t('support.scNoTicketsTitle')}
          text={t('support.scNoTicketsText')}
          action={
            <button className="btn btn-gold" onClick={onNew}>
              <Icon name="plus" size={18} />
              <span>{t('support.scOpenTicket')}</span>
            </button>
          }
        />
      ) : (
        <div className="col fade-in">
          {tickets.map((t: any) => (
            <TicketRow key={t.id} t={t} onOpen={onOpen} />
          ))}
        </div>
      )}
    </>
  );
}

function TicketRow({ t, onOpen }: any) {
  const unread = t.last_message_by === 'admin' && !['closed', 'resolved'].includes(t.status);
  return (
    <a className={'ticket-card' + (unread ? ' unread' : '')} href={pagePath('/support?ticket=' + encodeURIComponent(t.id))} onClick={(e) => {
      e.preventDefault();
      window.history.pushState(null, '', pagePath('/support?ticket=' + encodeURIComponent(t.id)));
      onOpen(t.id);
    }}>
      <div className="ticket-ico">
        <Icon name="headset" size={20} />
      </div>
      <div className="t-main">
        <div className="t-subject">{t.subject}</div>
        <div className="t-snip">{`${t.last_message_by === 'admin' ? 'Support: ' : ''}${t.last_message_snippet || ''}`}</div>
      </div>
      <div className="t-meta">
        {unread ? (
          <span className="chip chip-new">
            <Icon name="headset" size={12} /> Support replied
          </span>
        ) : null}
        <StatusBadge status={t.status} />
        <span className="xs faint t-time">{timeAgo(t.updated_at)}</span>
      </div>
      <Icon name="chevron" size={18} />
    </a>
  );
}

function NewTicketView({ choices, loadChoices, onBack }: any) {
  const { t } = useT();
  const { navigate } = useRouter();
  const { toast } = useToast();
  const [sel, setSel] = useState('');
  const [subject, setSubject] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(choices.length > 0);
  const requestedChoices = useRef(false);

  useEffect(() => {
    if (requestedChoices.current) return;
    requestedChoices.current = true;
    if (!choices.length) {
      loadChoices().finally(() => setLoaded(true));
    } else setLoaded(true);
  }, [choices, loadChoices]);

  // The order <select> must never submit an empty value when orders exist
  // (that used to produce "order_id is required"), and must default to
  // "general" when they do not. Pre-select the order passed via ?order=
  // (from an order's help card), else the first order once we know them.
  useEffect(() => {
    if (!loaded || sel !== '') return;
    const want = queryParam('order');
    if (want && choices.some((c: any) => c.id === want)) setSel(want);
    else if (choices.length) setSel(choices[0].id);
  }, [loaded, choices, sel]);

  const topics = [
    t('support.scTopicCode'),
    t('support.scTopicPayment'),
    t('support.scTopicDelay'),
    t('support.scTopicRefund'),
  ];

  return (
    <div className="card fade-in" style={{ maxWidth: '640px', margin: '0 auto' }}>
      <div className="row between mb-2">
        <h3 style={{ margin: 0 }}>{t('support.scOpenTicketTitle')}</h3>
        <button className="btn btn-ghost btn-sm" onClick={onBack}>
          <Icon name="back" size={16} />
          <span className="btn-label">{t('support.scBack')}</span>
        </button>
      </div>
      {!loaded ? (
        <div className="center mt-1">
          <div className="spinner" style={{ margin: '14px auto' }} />
          <div className="small muted">{t('support.scLoadingOrders')}</div>
        </div>
      ) : (
        <>
          <div className="field">
            <label>{t('support.scWhichOrder')}</label>
            <select className="input" aria-label={t('support.scOrderAria')} value={sel} onChange={(e) => setSel(e.target.value)}>
              <option value="">{t('support.scGeneralOption')}</option>
              {choices.map((c: any) => (
                <option key={c.id} value={c.id}>
                  {c.label}
                </option>
              ))}
            </select>
          </div>
          {!choices.length && (
            <AlertBox type="info">
              {t('support.noOrdersYet')}
            </AlertBox>
          )}
          <div className="field">
            <label>{t('support.scQuickTopic')}</label>
            <div className="row mt-1" style={{ gap: '8px', flexWrap: 'wrap' }}>
              {topics.map((t) => (
                <button
                  key={t}
                  className="btn btn-ghost btn-sm topic-chip"
                  type="button"
                  onClick={() => {
                    setSubject(t);
                  }}
                >
                  {t}
                </button>
              ))}
            </div>
          </div>
          <div className="field">
            <label>{t('support.subject')}</label>
            <input className="input" placeholder={t('support.scSubjectPh')} maxLength={160} aria-label={t('support.subject')} value={subject} onChange={(e) => setSubject(e.target.value)} />
          </div>
          <div className="field">
            <label>{t('support.message')}</label>
            <textarea className="input" placeholder={t('support.scMessagePh')} maxLength={4000} style={{ minHeight: '130px' }} aria-label={t('support.message')} value={message} onChange={(e) => setMessage(e.target.value)} />
          </div>
          <button
            className="btn btn-gold btn-block"
            disabled={busy}
            onClick={async () => {
              if (!subject.trim() || !message.trim()) {
                toast(t('support.scToastNeedFields'), 'error');
                return;
              }
              setBusy(true);
              try {
                const res = await createTicket({ order_id: sel, subject: subject.trim(), message: message.trim() });
                toast(t('support.scToastCreated'), 'success');
                const created = ticketPayload(res).ticket;
                // same page, new ?ticket=… → the router remounts the view
                navigate('/support?ticket=' + encodeURIComponent(created.id), { replace: true });
              } catch (err) {
                toast(friendlyApiMessage(err, t('support.scErrCreate')), 'error');
                setBusy(false);
              }
            }}
          >
            <Icon name="send" size={18} />
            <span className="btn-label">{t('support.scSubmit')}</span>
          </button>
          <div className="xs faint mt-1">{t('support.scDupNote')}</div>
        </>
      )}
    </div>
  );
}

function ConvoView({ loading, err, ticket, messages, chatRef, onBack, onSend, onResolve, onReopen, myAddress }: any) {
  const { t } = useT();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const resolved = ticket && ticket.status === 'resolved';
  const closed = ticket && ticket.status === 'closed';

  if (loading) {
    return (
      <>
        <div className="row mb-2">
          <button className="btn btn-ghost btn-sm" onClick={onBack}>
            <Icon name="back" size={16} />
            <span className="btn-label">{t('support.scAllTickets')}</span>
          </button>
        </div>
        <div className="card">
          <SkeletonCards n={2} />
        </div>
      </>
    );
  }
  if (err) {
    return <ErrorState message={err} retry={onBack} />;
  }
  if (!ticket) return null;
  let lastDay = '';

  const act = async (fn: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    try {
      await fn();
      setConfirming(false);
    } catch {
      /* toast already shown upstream */
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fade-in">
      <div className="row mb-2">
        <button className="btn btn-ghost btn-sm" onClick={onBack}>
          <Icon name="back" size={16} />
          <span className="btn-label">{t('support.scAllTickets')}</span>
        </button>
      </div>
      <div className="card">
        <div className="convo-head">
          <div style={{ minWidth: 0 }}>
            <h3 style={{ margin: 0, display: 'flex', alignItems: 'center', gap: '10px', flexWrap: 'wrap' }}>
              <span>{ticket.subject}</span>
              <StatusBadge status={ticket.status} />
            </h3>
            <div className="xs faint mt-1">
              {t('support.scOpenedLabel', { date: fmtDate(ticket.created_at) })}{' '}
              {ticket.order_id ? (
                <>
                  {t('support.scOrderWord')} <span className="mono">{shortId(ticket.order_id)}</span>
                </>
              ) : (
                t('support.scGeneralEnquiry')
              )}
              {ticket.id ? (
                <>
                  {' '}
                  · ticket <span className="mono">{shortId(ticket.id)}</span>
                </>
              ) : null}
            </div>
          </div>
        </div>

        <div className="ticket-legend">
          {STATUS_LEGEND.map(([key, label]) => (
            <span key={key} className={'legend-step' + (ticket.status === key ? ' active' : '')}>
              {label}
            </span>
          ))}
        </div>

        {ticket.status === 'waiting_user' ? (
          <div className="alert info waiting-banner" style={{ marginBottom: 0 }}>
            <Icon name="headset" size={16} />
            <div className="small strong">{t('support.scWaitingReply')}</div>
          </div>
        ) : null}

        <div className="chat-window mt-2 mb-2" ref={chatRef}>
          {messages.map((m: any, i: number) => {
            const day = new Date(m.created_at).toDateString();
            const sep = day !== lastDay ? day : '';
            lastDay = day;
            const today = new Date().toDateString();
            const yesterday = new Date(Date.now() - 864e5).toDateString();
            return (
              <div key={i}>
                {sep ? (
                  <div className="day-chip">{day === today ? 'Today' : day === yesterday ? 'Yesterday' : fmtDate(m.created_at).split(',')[0]}</div>
                ) : null}
                <ChatMessageRow m={m} myAddress={myAddress} />
              </div>
            );
          })}
          {!messages.length ? (
            <div className="xs faint center" style={{ padding: '18px 0' }}>
              {t('support.noMessagesYet')}
            </div>
          ) : null}
        </div>

        {resolved ? (
          <>
            <div className="resolved-card">
              <Icon name="check" size={22} />
              <div>
                <div className="strong">{t('support.scResolvedHead')}</div>
                <div className="small" style={{ marginTop: 2 }}>
                  Glad we could help. Something else came up? Reopen it — the history stays here.
                </div>
              </div>
              <div style={{ marginLeft: 'auto' }}>
                <button className="btn btn-outline btn-sm" type="button" disabled={busy} onClick={() => act(onReopen)}>
                  <Icon name="refresh" size={14} />
                  <span className="btn-label">{t('support.scReopen')}</span>
                </button>
              </div>
            </div>
          </>
        ) : closed ? (
          <AlertBox type="info">
            {t('support.closedByTeamPre')}{' '}
            <button className="linklike" type="button" onClick={onBack}>
              {t('support.goBackAllTickets')}
            </button>
            .
          </AlertBox>
        ) : (
          <>
            <ChatComposer placeholder={t('support.scReplyPlaceholder')} sendLabel={t('support.scSendReply')} onSend={onSend} />
            {confirming ? (
              <div className="resolve-guard">
                <p>
                  <Icon name="alert" size={15} style={{ verticalAlign: '-3px', marginRight: 4 }} />
                  {rich(t('support.confirmResolve'))}
                </p>
                <div className="row" style={{ gap: '8px', flexWrap: 'wrap' }}>
                  <button className="btn btn-gold btn-sm" type="button" disabled={busy} onClick={() => act(onResolve)}>
                    <Icon name="check" size={15} />
                    <span className="btn-label">{t('support.scYesResolved')}</span>
                  </button>
                  <button className="btn btn-ghost btn-sm" type="button" disabled={busy} onClick={() => setConfirming(false)}>
                    Cancel
                  </button>
                </div>
              </div>
            ) : (
              <div className="ticket-actions">
                <span className="xs faint">{t('support.scCloseHint')}</span>
                <button className="btn btn-outline btn-sm" type="button" onClick={() => setConfirming(true)}>
                  <Icon name="check" size={14} />
                  <span className="btn-label">{t('support.scMarkResolved')}</span>
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

function shortId(id?: string | null): string {
  if (!id) return '';
  return id.length > 14 ? id.slice(0, 6) + '…' + id.slice(-4) : id;
}

export function SupportPage() {
  return (
    <AppRoot activeKey="support">
      <SupportView />
    </AppRoot>
  );
}
