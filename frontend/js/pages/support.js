/* pages/support.js — support center: ticket list, ticket conversation with
 * live polling, and a new-ticket form bound to real orders/quotes.
 */
import { bootShell, openLogin } from '../shell.js';
import { el, icon, $, replaceChildren, fmtDate, timeAgo, queryParam, pageInterval } from '../util.js';
import { listTickets, getTicket, replyTicket, createTicket, listOrders, listQuotes } from '../api.js';
import { isAuthed, getAddress } from '../session.js';
import { toast, statusBadge, emptyState, errorState, skeletonCards, alertBox, chatComposer, chatMessageRow } from '../ui.js';

bootShell('support');

const main = $('#main');
// Mount guard: client-side navigation can import this module more than
// once; wipe #main so a re-mount never stacks a second page shell on top
// of the first (the duplicate locked-card bug).
replaceChildren(main);
main.appendChild(el('div.container.support-page', {},
  el('div.support-hero', {},
    el('div.support-hero-main', {},
      el('div.sup-ico', {}, icon('headset', 26)), // properly framed icon tile
      el('div', {},
        el('h1.support-title', { text: 'Support center' }),
        el('div.xs.faint.mt-1', { text: 'Real humans, fast answers. Every ticket lives on your order — nothing gets lost.' }),
      ),
    ),
    el('div.sup-chips', {},
      el('span.chip', {}, icon('bolt', 13), 'Fast, human replies'),
      el('span.chip', {}, icon('receipt', 13), 'Tied to your order'),
      el('span.chip', {}, icon('lock', 13), 'Private — you & support'),
    ),
  ),
  el('div#content'),
));

const content = $('#content');
let pollTimer = null;
let lastReplyDraft = ''; // survives re-renders (polling refresh) until sent

const STATUS_LEGEND = [
  ['open', 'We have your ticket'],
  ['waiting_admin', 'Support is working on it'],
  ['waiting_user', 'Waiting for your reply'],
  ['resolved', 'Fixed & resolved'],
  ['closed', 'Closed'],
];

function lockedView() {
  return el('div.card.locked.fade-in', {},
    el('div.lock-ico', {}, icon('headset', 34)),
    el('h2', { text: 'We are here to help' }),
    el('p', { text: 'Connect your wallet to open tickets about your orders and chat with our support team.' }),
    el('button.btn.btn-gold.btn-lg', { on: { click: () => openLogin(() => init()) } }, icon('nimiq', 20), el('span.btn-label', { text: 'Connect wallet' })),
  );
}

async function init() {
  clearInterval(pollTimer);
  if (!isAuthed()) { replaceChildren(content, lockedView()); return; }
  const ticketId = queryParam('ticket');
  if (ticketId) await showTicket(ticketId);
  else await showList();
}

/* ---------------- list ---------------- */

async function showList() {
  replaceChildren(content,
    el('div.row.between.mb-2', { style: { flexWrap: 'wrap', gap: '10px' } },
      el('div.xs.muted', { text: 'Tickets are linked to orders — replies appear here and on the order page.' }),
      el('button.btn.btn-gold', { on: { click: showNewTicket } }, icon('plus', 18), el('span.btn-label', { text: 'New ticket' })),
    ),
    el('div#ticketList', {}, el('div.grid', {}, skeletonCards(3))),
  );

  try {
    const raw = await listTickets();
    // The API may return either a bare array or an envelope. Never call
    // .length on null/undefined when an account has no tickets.
    const tickets = Array.isArray(raw) ? raw : (Array.isArray(raw?.tickets) ? raw.tickets : (Array.isArray(raw?.data) ? raw.data : []));
    const box = $('#ticketList');
    if (!box) return; // RACE FIX: the view was swapped away while we awaited — do not render into a dead node
    if (!tickets.length) {
      replaceChildren(box, emptyState({
        iconName: 'headset',
        title: 'No tickets yet',
        text: 'If anything ever looks wrong with an order, open a ticket and we will take care of it.',
        action: el('button.btn.btn-gold', { on: { click: showNewTicket } }, icon('plus', 18), el('span', { text: 'Open a ticket' })),
      }));
      return;
    }
    const list = el('div.col.fade-in');
    for (const t of tickets) list.appendChild(ticketRow(t));
    replaceChildren(box, list);
  } catch (err) {
    replaceChildren($('#ticketList'), errorState(err.message, showList));
  }
}

function ticketRow(t) {
  const unread = t.last_message_by === 'admin' && !['closed', 'resolved'].includes(t.status);
  return el('a.ticket-card' + (unread ? '.unread' : ''), { href: `/support?ticket=${encodeURIComponent(t.id)}` },
    el('div.ticket-ico', {}, icon('headset', 20)),
    el('div.t-main', {},
      el('div.t-subject', { text: t.subject }),
      el('div.t-snip', { text: `${t.last_message_by === 'admin' ? 'Support: ' : ''}${t.last_message_snippet || ''}` }),
    ),
    el('div.t-meta', {},
      unread ? el('span.chip.chip-new', {}, icon('headset', 12), 'Support replied') : null,
      statusBadge(t.status),
      el('span.xs.faint.t-time', { text: timeAgo(t.updated_at) }),
    ),
    icon('chevron', 18), // direction affordance, aligned with the meta column
  );
}

/* ---------------- new ticket ---------------- */

async function showNewTicket() {
  clearInterval(pollTimer);
  replaceChildren(content, el('div.card.fade-in', { style: { maxWidth: '640px', margin: '0 auto' } },
    el('div.row.between.mb-2', {},
      el('h3', { style: { margin: 0 }, text: 'Open a support ticket' }),
      el('button.btn.btn-ghost.btn-sm', { on: { click: showList } }, icon('back', 16), el('span.btn-label', { text: 'Back' })),
    ),
    el('div.center.mt-1', {}, el('div.spinner', { style: { margin: '14px auto' } }), el('div.small.muted', { text: 'Loading your orders…' })),
  ));

  const card = content.firstChild;

  let choices = [];
  try {
    const [orders, quotes] = await Promise.all([listOrders(), listQuotes().catch(() => [])]);
    choices = [
      ...(orders || []).map((o) => ({ id: o.id, label: `${(o.payload && o.payload.product_name) || o.product_id} — ${fmtDate(o.created_at)}` })),
      ...(quotes || []).map((q) => ({ id: q.id, label: `CryptoRefills Lightning payment (${q.product_id}) — ${fmtDate(q.created_at)}` })),
    ];
  } catch (err) {
    card.textContent = '';
    card.appendChild(errorState(err.message, showNewTicket));
    return;
  }

  card.textContent = '';
  card.append(
    el('div.row.between.mb-2', {},
      el('h3', { style: { margin: 0 }, text: 'Open a support ticket' }),
      el('button.btn.btn-ghost.btn-sm', { on: { click: showList } }, icon('back', 16), el('span.btn-label', { text: 'Back' })),
    ),
  );

  if (!choices.length) {
    card.appendChild(alertBox('info', 'Tickets are attached to an order, but you have no orders yet. Buy something first — then we can help with anything.'));
    card.appendChild(el('a.btn.btn-gold.btn-block', { href: '/' }, icon('bag', 18), el('span', { text: 'Go to the shop' })));
    return;
  }

  const select = el('select.input', { 'aria-label': 'Order' },
    choices.map((c) => el('option', { value: c.id, text: c.label })),
  );
  const subject = el('input.input', { placeholder: 'Short summary, e.g. “Code not redeemable”', maxlength: 160, 'aria-label': 'Subject' });
  // One-tap topics: fill the subject so tickets arrive pre-sorted.
  const topics = ['Code not working', 'Payment issue', 'Delivery delay', 'Refund question'];
  const topicRow = el('div.row.mt-1', { style: { gap: '8px', flexWrap: 'wrap' } },
    topics.map((t) => el('button.btn.btn-ghost.btn-sm.topic-chip', {
      type: 'button',
      on: { click: () => { subject.value = t; subject.focus(); } },
    }, t)),
  );
  const message = el('textarea.input', { placeholder: 'Describe what happened, what you expected, and any error text.', maxlength: 4000, style: { minHeight: '130px' }, 'aria-label': 'Message' });
  message.addEventListener('keydown', (e) => { if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); submit.click(); } });

  const submit = el('button.btn.btn-gold.btn-block', {
    on: {
      click: async () => {
        if (!subject.value.trim() || !message.value.trim()) {
          toast('Please add a subject and a message.', 'error');
          return;
        }
        submit.disabled = true;
        try {
          const res = await createTicket({
            order_id: select.value,
            subject: subject.value.trim(),
            message: message.value.trim(),
          });
          toast('Ticket created — we usually reply quickly.', 'success');
          history.replaceState(null, '', `/support?ticket=${encodeURIComponent(res.ticket.id)}`);
          showTicket(res.ticket.id);
        } catch (err) {
          toast(err.message || 'Could not create the ticket', 'error');
          submit.disabled = false;
        }
      },
    },
  }, icon('send', 18), el('span.btn-label', { text: 'Submit ticket' }));

  card.append(
    el('div.field', {}, el('label', { text: 'Which order is this about?' }), select),
    el('div.field', {}, el('label', { text: 'Quick topic' }), topicRow),
    el('div.field', {}, el('label', { text: 'Subject' }), subject),
    el('div.field', {}, el('label', { text: 'Message' }), message),
    submit,
    el('div.xs.faint.mt-1', { text: 'One ticket per order: if one is already open, your message is added to it — nothing is duplicated.' }),
  );
}

/* ---------------- conversation ---------------- */

async function showTicket(ticketId) {
  clearInterval(pollTimer);
  replaceChildren(content, el('div', {},
    el('div.row.mb-2', {},
      el('button.btn.btn-ghost.btn-sm', { on: { click: () => { history.replaceState(null, '', '/support'); showList(); } } }, icon('back', 16), el('span.btn-label', { text: 'All tickets' })),
    ),
    el('div.card', {}, skeletonCards(2)),
  ));

  try {
    const res = await getTicket(ticketId);
    renderConversation(res.ticket, res.messages || []);
  } catch (err) {
    replaceChildren(content, errorState(err.message, () => showTicket(ticketId)));
  }
}

function renderConversation(ticket, messages) {
  messages = Array.isArray(messages) ? messages : [];
  const card = el('div.card.fade-in', {});
  card.append(
    el('div.row.between.mb-1', { style: { flexWrap: 'wrap', gap: '10px' } },
      el('div', { style: { minWidth: 0 } },
        el('h3', { style: { margin: 0 }, text: ticket.subject }),
        el('div.xs.faint', { text: `Opened ${fmtDate(ticket.created_at)} · order ${ticket.order_id}` }),
      ),
      statusBadge(ticket.status),
    ),
    el('div.ticket-legend', {},
      STATUS_LEGEND.map(([key, label]) => el('span.legend-step' + (ticket.status === key ? '.active' : ''), {}, label)),
    ),
  );

  if (ticket.status === 'waiting_user') {
    card.appendChild(el('div.alert.info.waiting-banner', { style: { marginBottom: 0 } }, icon('headset', 16),
      el('div.small.strong', { text: 'Support is waiting for your reply — answer below to keep the ticket moving.' })));
  }

  /* Scrollable chat window with day separators; auto-scrolls to the newest
   * message on open and on every refresh. */
  const chat = el('div.chat.chat-window.mt-2.mb-2');
  const inner = el('div.chat-inner');
  chat.appendChild(inner);
  const myAddress = getAddress();
  let lastDay = '';
  for (const m of messages) {
    const day = new Date(m.created_at).toDateString();
    if (day !== lastDay) {
      lastDay = day;
      const today = new Date().toDateString();
      const yesterday = new Date(Date.now() - 864e5).toDateString();
      inner.appendChild(el('div.day-chip.xs.faint', { text: day === today ? 'Today' : day === yesterday ? 'Yesterday' : fmtDate(m.created_at).split(',')[0] }));
    }
    inner.appendChild(chatMessageRow(m, myAddress));
  }
  if (!messages.length) inner.appendChild(el('div.xs.faint.center', { style: { padding: '18px 0' }, text: 'No messages yet — your first message starts the conversation.' }));
  card.appendChild(chat);

  const isClosed = ['closed', 'resolved'].includes(ticket.status);

  if (!isClosed) {
    const composer = chatComposer({
      placeholder: 'Write a reply…',
      sendLabel: 'Send reply',
      onSend: async (text) => {
        try {
          await replyTicket(ticket.id, text);
          lastReplyDraft = ''; // sent — start the refreshed thread clean
          const fresh = await getTicket(ticket.id);
          renderConversation(fresh.ticket, fresh.messages || []);
        } catch (err) {
          toast(err.message || 'Could not send the reply', 'error');
          throw err; // let the composer restore, but keep the typed text
        }
      },
    });
    // Keep the typed text on a failed send (composer clears only on success).
    composer.ta.addEventListener('input', () => { lastReplyDraft = composer.ta.value; });
    if (lastReplyDraft) composer.ta.value = lastReplyDraft;
    card.appendChild(composer.box);
  } else {
    card.appendChild(alertBox('info', `This ticket is ${ticket.status}. If the issue returns, open a new ticket from the order page.`));
  }

  replaceChildren(content, el('div', {},
    el('div.row.mb-2', {},
      el('button.btn.btn-ghost.btn-sm', { on: { click: () => { clearInterval(pollTimer); history.replaceState(null, '', '/support'); showList(); } } }, icon('back', 16), el('span.btn-label', { text: 'All tickets' })),
    ),
    card,
  ));
  requestAnimationFrame(() => { const w = card.querySelector('.chat-window'); if (w) w.scrollTop = w.scrollHeight; });

  if (!isClosed) {
    pollTimer = pageInterval(async () => {
      try {
        const fresh = await getTicket(ticket.id);
        if ((fresh.messages || []).length !== messages.length || fresh.ticket.status !== ticket.status) {
          renderConversation(fresh.ticket, fresh.messages || []);
        }
      } catch { /* keep polling */ }
    }, 15000);
  }
}

window.addEventListener('nimshop:session', () => init());
init();
