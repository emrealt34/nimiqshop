/**
 * AdminSupportPanel — operator-side support inbox.
 *
 * Shows every conversation with Active / All / Resolved / Closed filters, an
 * inline chat for the selected ticket, reply-with-next-status, and quick
 * actions (mark resolved, close, reopen). Mirrors the Go admin endpoints.
 */
import { Fragment, useCallback, useEffect, useRef, useState } from 'react';
import { Icon } from '../ui/Icon';
import { Identicon } from '../ui/Identicon';
import { StatusBadge, AlertBox } from '../ui/uiKit';
import {
  adminListSupportTickets,
  adminGetSupportTicket,
  adminReplySupportTicket,
  adminUpdateSupportTicketStatus,
} from '../../lib/api';
import { timeAgo, fmtDate, formatWalletAddress } from '../../lib/format';

type TicketRow = {
  id: string;
  subject: string;
  status: string;
  order_id?: string | null;
  product_id?: string;
  order_kind?: string;
  user_address?: string;
  last_message_snippet?: string;
  last_message_by?: string;
  message_count?: number;
  created_at?: string;
  updated_at?: string;
};

const FILTERS: Array<{ key: string; label: string; test: (s: string) => boolean }> = [
  { key: 'active', label: 'Active', test: (s) => !['resolved', 'closed'].includes(s) },
  { key: 'all', label: 'All', test: () => true },
  { key: 'resolved', label: 'Resolved', test: (s) => s === 'resolved' },
  { key: 'closed', label: 'Closed', test: (s) => s === 'closed' },
];

/** One-tap canned replies for operators — inserted into the composer. */
const MACROS = [
  {
    label: 'Manual check',
    text: 'Thanks for your patience — the supplier flagged this order for a manual check. We are on it: you will get the code or a full refund within 24 hours.',
  },
  {
    label: 'Refund started',
    text: 'We could not deliver this order, so a full refund is on its way back to your wallet. It lands as soon as the chain confirms — usually a few minutes.',
  },
  {
    label: 'Code re-sent',
    text: 'Your code was re-sent to the delivery email on this order. Please check spam too, and tell us if it is still missing in an hour.',
  },
  {
    label: 'Need details',
    text: 'To move this forward we need one more detail from you: the exact error or screenshot you saw, and the email used at checkout.',
  },
];

/** Live-inbox poll cadence (ms). */
const POLL_MS = 8000;

const STATUS_LABEL: Record<string, string> = {
  open: 'New',
  waiting_admin: 'Waiting on you',
  waiting_user: 'Waiting on buyer',
  resolved: 'Resolved',
  closed: 'Closed',
};

/** Day separator label shared with the buyer thread ("Today"/"Yesterday"/date). */
function dayLabel(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const day = d.toDateString();
  if (day === now.toDateString()) return 'Today';
  const y = new Date(now.getTime() - 86400000);
  if (day === y.toDateString()) return 'Yesterday';
  return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: d.getFullYear() === now.getFullYear() ? undefined : 'numeric' });
}

function AdminMsgRow({ m, buyer }: { m: any; buyer?: string }) {
  const isNote = m.sender === 'admin_note';
  const isAdmin = m.sender === 'admin' || isNote;
  const t = m.created_at ? new Date(m.created_at) : null;
  const time =
    t && !isNaN(t as unknown as number) ? t.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' }) : '';
  return (
    <div className={`msg ${isAdmin ? 'msg-out' : 'msg-in'}`}>
      <div className={`msg-ava ${isAdmin ? 'msg-ava-you' : 'msg-ava-staff'}`}>
        {isNote ? <Icon name="lock" size={16} /> : isAdmin ? <Icon name="shield" size={16} /> : <Identicon address={buyer || m.sender} className="msg-ava-img" />}
      </div>
      <div className="msg-body">
        <div className="msg-who">
          <span>{isNote ? 'Internal note · staff only' : isAdmin ? 'You · operator' : 'Buyer'}</span>
          {isAdmin ? <span className="msg-staff">{isNote ? 'Note' : 'Staff'}</span> : null}
        </div>
        <div className={`msg-bubble ${isNote ? 'note' : isAdmin ? 'out' : 'in'}`}>{m.message}</div>
        <div className="msg-time">{time}</div>
      </div>
    </div>
  );
}

export function AdminSupportPanel() {
  const [rows, setRows] = useState<TicketRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [filter, setFilter] = useState('active');
  const [sel, setSel] = useState<string | null>(null);
  const [detail, setDetail] = useState<any>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [text, setText] = useState('');
  const [nextStatus, setNextStatus] = useState('waiting_user');
  const [internal, setInternal] = useState(false);
  const [unread, setUnread] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const chatRef = useRef<HTMLDivElement>(null);
  const rowsRef = useRef<TicketRow[]>([]);
  const selRef = useRef<string | null>(null);
  useEffect(() => {
    rowsRef.current = rows;
  }, [rows]);
  useEffect(() => {
    selRef.current = sel;
  }, [sel]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r: any = await adminListSupportTickets();
      const list = Array.isArray(r?.tickets) ? r.tickets : Array.isArray(r) ? r : [];
      setRows(list);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // Live inbox: poll the list AND the open conversation so new buyer messages
  // arrive without pressing Refresh. A ticket whose updated_at jumps while it
  // is not the open one (and whose last message is from the buyer) gets an
  // unread dot until the operator opens it.
  useEffect(() => {
    const t = setInterval(async () => {
      try {
        const all: any = await adminListSupportTickets();
        const list: TicketRow[] = Array.isArray(all?.tickets) ? all.tickets : Array.isArray(all) ? all : [];
        const prev = new Map(rowsRef.current.map((r) => [r.id, String(r.updated_at || '')]));
        const fresh = list
          .filter(
            (r) =>
              prev.has(r.id) &&
              String(r.updated_at || '') > (prev.get(r.id) as string) &&
              r.last_message_by === 'user' &&
              r.id !== selRef.current
          )
          .map((r) => r.id);
        if (fresh.length) setUnread((u) => Array.from(new Set([...u, ...fresh])));
        setRows(list);
        const sid = selRef.current;
        if (sid) {
          const r: any = await adminGetSupportTicket(sid);
          if (r) setDetail(r);
        }
      } catch {
        /* keep the stale view on network hiccups */
      }
    }, POLL_MS);
    return () => clearInterval(t);
  }, []);

  const open = useCallback(async (id: string) => {
    setSel(id);
    setUnread((u) => u.filter((x) => x !== id));
    setDetail(null);
    setDetailLoading(true);
    try {
      const r: any = await adminGetSupportTicket(id);
      setDetail(r || {});
    } finally {
      setDetailLoading(false);
    }
  }, []);

  useEffect(() => {
    if (chatRef.current) chatRef.current.scrollTop = chatRef.current.scrollHeight;
  }, [detail]);

  const refreshOne = useCallback(
    async (id: string) => {
      try {
        const r: any = await adminGetSupportTicket(id);
        setDetail(r || {});
        const all: any = await adminListSupportTickets();
        setRows(Array.isArray(all?.tickets) ? all.tickets : Array.isArray(all) ? all : []);
      } catch {
        /* keep stale view */
      }
    },
    []
  );

  const sendReply = async () => {
    const m = text.trim();
    if (!m || !sel || busy) return;
    setBusy(true);
    try {
      await adminReplySupportTicket(sel, m, internal ? undefined : nextStatus, internal);
      setText('');
      setInternal(false);
      await refreshOne(sel);
    } finally {
      setBusy(false);
    }
  };

  const setStatus = async (status: string) => {
    if (!sel || busy) return;
    setBusy(true);
    try {
      await adminUpdateSupportTicketStatus(sel, status);
      await refreshOne(sel);
    } finally {
      setBusy(false);
    }
  };

  const visible = rows.filter((r) => (FILTERS.find((f) => f.key === filter) || FILTERS[0]).test(String(r.status || '')));

  const countFor = (key: string) => rows.filter((r) => (FILTERS.find((f) => f.key === key) || FILTERS[0]).test(String(r.status || ''))).length;

  return (
    <div className="card mt-2">
      <div className="card-title">
        <Icon name="headset" size={15} />
        <span>Support inbox</span>
        <span className="xs faint" style={{ marginLeft: 'auto' }}>
          {rows.length} conversation{rows.length === 1 ? '' : 's'}
          {unread.length ? (
            <span className="msg-staff" style={{ marginLeft: 8 }}>
              {unread.length} new
            </span>
          ) : null}
        </span>
      </div>

      <div className="asup-toolbar mt-1">
        <div className="asup-filters">
          {FILTERS.map((f) => (
            <button
              key={f.key}
              className={`btn btn-sm btn-ghost ${filter === f.key ? 'on' : ''}`}
              type="button"
              onClick={() => setFilter(f.key)}
            >
              {f.label}
              <span className="xs faint" style={{ marginLeft: 4 }}>
                {countFor(f.key)}
              </span>
            </button>
          ))}
        </div>
        <button className="btn btn-ghost btn-sm" type="button" onClick={load} disabled={loading}>
          <Icon name="refresh" size={14} />
          <span className="btn-label">Refresh</span>
        </button>
      </div>

      {loading ? (
        <div className="small muted center" style={{ padding: '20px 0' }}>
          Loading conversations…
        </div>
      ) : !visible.length ? (
        <div className="asup-empty">No conversations here right now.</div>
      ) : (
        <div className="grid" style={{ gridTemplateColumns: 'minmax(0,1fr) minmax(0,2fr)', gap: '14px', alignItems: 'start', marginTop: '12px' }}>
          <div className="asup-list">
            {visible.map((t) => {
              const active = sel === t.id;
              const isOpen = !['resolved', 'closed'].includes(String(t.status || ''));
              return (
                <button
                  key={t.id}
                  className={`asup-card ${active ? 'selected' : ''}`}
                  type="button"
                  onClick={() => open(t.id)}
                  style={{ borderStyle: isOpen && t.last_message_by === 'admin' ? 'solid' : undefined, borderColor: active ? undefined : 'var(--line-strong)' }}
                >
                  <div style={{ minWidth: 0, flex: 1 }}>
                    <div className="asup-subject">{t.subject}</div>
                    <div className="asup-snip">
                      {t.last_message_by === 'admin' ? 'Staff: ' : 'You: '}
                      {t.last_message_snippet || 'no messages yet'}
                    </div>
                  </div>
                  <div className="asup-meta">
                    {unread.includes(t.id) ? <span className="asup-unread" title="New buyer messages" /> : null}
                    <span className={`asup-badge ${isOpen ? '' : ''}`} style={{ color: isOpen ? 'var(--stamp)' : 'var(--ink-faint)' }}>
                      {STATUS_LABEL[String(t.status || '')] || t.status}
                    </span>
                    <span className="xs faint">{timeAgo(t.updated_at || '')}</span>
                  </div>
                </button>
              );
            })}
          </div>

          <div>
            {!sel ? (
              <div className="asup-empty" style={{ border: '2px dashed var(--line-dash)', borderRadius: 'var(--r-l)' }}>
                Select a conversation to read and reply to it.
              </div>
            ) : detailLoading ? (
              <div className="asup-empty" style={{ border: '2px dashed var(--line-dash)', borderRadius: 'var(--r-l)' }}>
                Loading conversation…
              </div>
            ) : detail ? (
              <div className="asup-detail">
                <div className="asup-detail-head">
                  <div style={{ minWidth: 0 }}>
                    <div className="strong">{detail.ticket?.subject}</div>
                    <div className="asup-addr" style={{ marginTop: 2 }}>
                      {detail.ticket?.user_address ? formatWalletAddress(detail.ticket.user_address) : '—'}
                    </div>
                    {detail.order ? (
                      <div className="xs faint" style={{ marginTop: 2 }}>
                        {detail.order.product_name}
                        {detail.order.country ? ` · ${detail.order.country}` : ''} · opened {fmtDate(detail.ticket?.created_at || '')}
                      </div>
                    ) : (
                      <div className="xs faint" style={{ marginTop: 2 }}>
                        General enquiry · opened {fmtDate(detail.ticket?.created_at || '')}
                      </div>
                    )}
                  </div>
                  <div className="row" style={{ gap: '8px', flexWrap: 'wrap', alignItems: 'center' }}>
                    <StatusBadge status={detail.ticket?.status} />
                    <span className="asup-who">#{String(detail.ticket?.id || '').replace(/^tkt_/, '').slice(0, 8)}</span>
                  </div>
                </div>

                <div className="asup-chat" ref={chatRef}>
                  {Array.isArray(detail.messages) && detail.messages.length ? (
                    detail.messages.map((m: any, i: number) => {
                      const prev = i > 0 ? detail.messages[i - 1] : null;
                      const newDay = !prev || new Date(prev.created_at).toDateString() !== new Date(m.created_at).toDateString();
                      return (
                        <Fragment key={m.id || i}>
                          {newDay ? <div className="day-chip">{dayLabel(m.created_at)}</div> : null}
                          <AdminMsgRow m={m} buyer={detail.ticket?.user_address} />
                        </Fragment>
                      );
                    })
                  ) : (
                    <div className="asup-empty">No messages yet on this ticket.</div>
                  )}
                </div>

                <div className="asup-reply">
                  {['resolved', 'closed'].includes(String(detail.ticket?.status || '')) ? (
                    <div className="mb-2">
                      <AlertBox type="info">
                        This ticket is {detail.ticket?.status}. It stays visible under {detail.ticket?.status === 'resolved' ? 'Resolved' : 'Closed'} —
                        reopen it below if the buyer comes back to it.
                      </AlertBox>
                    </div>
                  ) : null}
                  <div className="asup-macros">
                    {MACROS.map((mc) => (
                      <button
                        key={mc.label}
                        className="chip xs"
                        type="button"
                        disabled={busy}
                        title={mc.text}
                        onClick={() => setText((t) => (t.trim() ? t.replace(/\s+$/, '') + '\n\n' : '') + mc.text)}
                      >
                        <Icon name="spark" size={12} /> {mc.label}
                      </button>
                    ))}
                  </div>
                  <div className="asup-reply-row">
                    <textarea
                      className="input"
                      placeholder={internal ? 'Internal note — the buyer never sees this…' : 'Type a reply to the buyer…'}
                      aria-label="Reply to buyer"
                      value={text}
                      onChange={(e) => setText(e.target.value)}
                    />
                    <div className="asup-reply-actions">
                      <label className="xs row" style={{ gap: 6, alignItems: 'center', margin: 0, flex: 'none' }}>
                        <input
                          type="checkbox"
                          checked={internal}
                          onChange={(e) => setInternal(e.target.checked)}
                          aria-label="Send as internal note"
                        />
                        Internal note
                      </label>
                      <div className="field" style={{ margin: 0 }}>
                        <select
                          className="asup-status-sel"
                          aria-label="Next status after reply"
                          value={nextStatus}
                          disabled={internal}
                          onChange={(e) => setNextStatus(e.target.value)}
                        >
                          <option value="waiting_user">After reply → waiting on buyer</option>
                          <option value="resolved">After reply → mark resolved</option>
                          <option value="closed">After reply → close</option>
                        </select>
                      </div>
                      <button className="btn btn-gold" type="button" disabled={busy || !text.trim()} onClick={sendReply}>
                        <Icon name={internal ? 'lock' : 'send'} size={15} />
                        <span className="btn-label">{internal ? 'Save note' : 'Send reply'}</span>
                      </button>
                    </div>
                  </div>
                  <div className="row" style={{ gap: '8px', flexWrap: 'wrap', marginTop: 10 }}>
                    {!['resolved', 'closed'].includes(String(detail.ticket?.status || '')) ? (
                      <>
                        <button className="btn btn-outline btn-sm" type="button" disabled={busy} onClick={() => setStatus('resolved')}>
                          <Icon name="check" size={14} />
                          <span className="btn-label">Mark resolved</span>
                        </button>
                        <button className="btn btn-outline btn-sm" type="button" disabled={busy} onClick={() => setStatus('closed')}>
                          <Icon name="lock" size={14} />
                          <span className="btn-label">Close conversation</span>
                        </button>
                      </>
                    ) : null}
                    {['resolved', 'closed'].includes(String(detail.ticket?.status || '')) ? (
                      <button className="btn btn-outline btn-sm" type="button" disabled={busy} onClick={() => setStatus('open')}>
                        <Icon name="refresh" size={14} />
                        <span className="btn-label">Reopen conversation</span>
                      </button>
                    ) : null}
                  </div>
                </div>
              </div>
            ) : null}
          </div>
        </div>
      )}
    </div>
  );
}
