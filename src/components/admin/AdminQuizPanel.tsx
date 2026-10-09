import { useEffect, useState } from 'react';
import { useSheet } from '../AppProviders';
import { Icon } from '../ui/Icon';
import { CopyButton } from '../ui/uiKit';
import { pagePath } from '../../lib/asset';
import { QuizPrizeCard } from '../quiz/QuizPage';
import {
  adminListQuizzes, adminCreateQuiz, adminUpdateQuiz, adminDeleteQuiz, adminPublishQuiz,
  adminCancelQuiz, adminQuizEntries, adminFinalizeQuiz, adminDeliverQuizPrize,
  type AdminQuiz, type AdminQuizEntry,
} from '../../lib/quiz';

function newDraft(source?: AdminQuiz): AdminQuiz {
  const now = new Date(); const end = new Date(now); end.setHours(23, 59, 59, 999);
  return { title: source?.title || '', description: source?.description || '',
    prize: source ? { ...source.prize } : { title: '', value: '', description: '', image_url: '' },
    starts_at: now.toISOString(), ends_at: end.toISOString(), status: 'draft',
    questions: source?.questions.map((q) => ({ ...q, id: crypto.randomUUID(), options: [...q.options] })) || [],
  };
}
function localDate(value: string): string {
  const date = new Date(value); if (!Number.isFinite(date.getTime())) return '';
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

export function AdminQuizPanel() {
  const { openSheet } = useSheet();
  const [items, setItems] = useState<AdminQuiz[]>([]);
  const [draft, setDraft] = useState<AdminQuiz | null>(null);
  const [manage, setManage] = useState<AdminQuiz | null>(null);
  const [entries, setEntries] = useState<AdminQuizEntry[]>([]);
  const [winner, setWinner] = useState('');
  const [note, setNote] = useState('');
  const [deliveryNote, setDeliveryNote] = useState('');
  const [reason, setReason] = useState('');
  const [search, setSearch] = useState('');
  const [page, setPage] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [loaded, setLoaded] = useState(false);

  const load = async () => { const list = await adminListQuizzes(); setItems(list); setLoaded(true); };
  useEffect(() => { let alive = true; adminListQuizzes().then((list) => { if (alive) { setItems(list); setLoaded(true); } }).catch((err) => { if (alive) { setError((err as Error).message); setLoaded(true); } }); return () => { alive = false; }; }, []);
  const run = async (action: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true); setError('');
    try { await action(); await load(); } catch (err) { setError((err as Error).message || 'Quiz request failed'); }
    finally { setBusy(false); }
  };
  const confirm = (title: string, message: string, action: () => Promise<unknown>) => openSheet({ title, render: (close) => <div><p>{message}</p><div className="row mt-2"><button className="btn btn-outline" onClick={close}>Cancel</button><button className="btn btn-gold" onClick={() => { close(); void run(action); }}>Confirm</button></div></div> });
  const openEntries = async (id: string) => {
    const result = await adminQuizEntries(id);
    setManage(result.competition); setEntries(result.items); setPage(0); setSearch('');
    const candidates = result.items.filter((entry) => entry.eligible);
    setWinner(result.competition.winner_user_id || (candidates.length === 1 ? candidates[0].user_id : ''));
    setNote(result.competition.decision_note || ''); setDeliveryNote(result.competition.award_note || ''); setReason('');
  };
  const editQuestion = (index: number, patch: Partial<AdminQuiz['questions'][number]>) => setDraft((old) => old && ({ ...old, questions: old.questions.map((q, i) => i === index ? { ...q, ...patch } : q) }));
  const schedule = (month: boolean) => {
    const now = new Date(); const end = month ? new Date(now.getFullYear(), now.getMonth() + 1, 0, 23, 59, 59, 999) : new Date(now);
    if (!month) end.setHours(23, 59, 59, 999);
    setDraft((old) => old && ({ ...old, starts_at: now.toISOString(), ends_at: end.toISOString() }));
  };
  const eligible = entries.filter((entry) => entry.eligible);
  const won = entries.find((entry) => entry.user_id === manage?.winner_user_id);
  const filtered = entries.filter((entry) => `${entry.address} ${entry.user_id} ${entry.prize_email}`.toLowerCase().includes(search.toLowerCase()));

  return <div className="quiz-admin">
    <div className="card"><div className="row between"><div><h2>Nimiq quiz competitions</h2><p className="small muted">One attempt per account. One point per correct answer. The highest score wins; tied leaders are decided by you. No automatic payment.</p></div><div className="row"><a className="btn btn-outline btn-sm" href={pagePath('/quiz')}>Open quiz page</a><button className="btn btn-gold" disabled={busy} onClick={() => { setDraft(newDraft()); setManage(null); setError(''); }}>New competition</button></div></div>
      <p className="xs muted">Write the questions yourself. Set a daily/monthly end or any custom dates. Publishing freezes the questions, prize and dates for fairness; clone or cancel to replace a published competition.</p>
    </div>
    {error && <div className="alert error mt-2" role="alert">{error}</div>}
    {!loaded && <div className="card mt-2">Loading…</div>}
    {loaded && !items.length && !draft && <div className="card mt-2">No competitions yet. Create a draft, add your Nimiq questions and prize, then publish it.</div>}
    {!draft && <div className="quiz-admin-list">{items.map((c) => <div className="card" key={c.id}><div className="row between"><div><strong>{c.title}</strong><div className="xs muted">{c.status} · {c.questions.length} questions · {c.participants || 0} entrants</div><div className="small">{c.prize.title} {c.prize.value && `· ${c.prize.value}`}</div><div className="xs muted">{new Date(c.starts_at).toLocaleString()} → {new Date(c.ends_at).toLocaleString()}</div></div><div className="row">
      {c.status === 'draft' && <><button className="btn btn-outline btn-sm" disabled={busy} onClick={() => { setDraft(structuredClone(c)); setManage(null); }}>Edit</button><button className="btn btn-gold btn-sm" disabled={busy || !c.questions.length} onClick={() => confirm('Publish competition', 'Publish these questions, prize and dates? They become locked. The server opens and closes entries at these times.', async () => { await adminPublishQuiz(c.id!); })}>Publish</button><button className="btn btn-ghost btn-sm" disabled={busy} onClick={() => confirm('Delete draft', 'Delete this unpublished draft and its questions?', async () => { await adminDeleteQuiz(c.id!); })}>Delete</button></>}
      <button className="btn btn-outline btn-sm" disabled={busy} onClick={() => { setDraft(newDraft(c)); setManage(null); }}>Copy to new period</button>
      {c.status !== 'draft' && <button className="btn btn-outline btn-sm" disabled={busy} onClick={() => void run(() => openEntries(c.id!))}>Participants & award</button>}
    </div></div></div>)}</div>}

    {draft && <form className="card mt-2 quiz-draft" onSubmit={(e) => { e.preventDefault(); void run(async () => { if (draft.id) await adminUpdateQuiz(draft.id, draft); else await adminCreateQuiz(draft); setDraft(null); }); }}>
      <div className="row between"><h2>{draft.id ? 'Edit draft' : 'New competition'}</h2><button className="btn btn-ghost btn-sm" type="button" disabled={busy} onClick={() => setDraft(null)}>Close editor</button></div>
      <div className="field"><label htmlFor="quiz-admin-title">Title</label><input id="quiz-admin-title" className="input" required maxLength={120} value={draft.title} onChange={(e) => setDraft({ ...draft, title: e.target.value })} /></div>
      <div className="field"><label htmlFor="quiz-admin-description">Description / competition rules</label><textarea id="quiz-admin-description" className="input" maxLength={2000} value={draft.description} onChange={(e) => setDraft({ ...draft, description: e.target.value })} /></div>
      <div className="row"><button className="btn btn-outline btn-sm" type="button" onClick={() => schedule(false)}>End of today</button><button className="btn btn-outline btn-sm" type="button" onClick={() => schedule(true)}>End of this month</button><span className="xs muted">Dates are shown in your browser's local timezone.</span></div>
      <div className="quiz-form-grid"><div className="field"><label htmlFor="quiz-admin-start">Starts</label><input id="quiz-admin-start" className="input" type="datetime-local" required value={localDate(draft.starts_at)} onChange={(e) => setDraft({ ...draft, starts_at: e.target.value ? new Date(e.target.value).toISOString() : '' })} /></div><div className="field"><label htmlFor="quiz-admin-end">Ends</label><input id="quiz-admin-end" className="input" type="datetime-local" required value={localDate(draft.ends_at)} onChange={(e) => setDraft({ ...draft, ends_at: e.target.value ? new Date(e.target.value).toISOString() : '' })} /></div></div>
      <h3>Prize — displayed on the quiz page</h3>
      <div className="quiz-form-grid"><div className="field"><label htmlFor="quiz-admin-prize-title">Prize name (e.g. Amazon gift card)</label><input id="quiz-admin-prize-title" className="input" required maxLength={140} value={draft.prize.title} onChange={(e) => setDraft({ ...draft, prize: { ...draft.prize, title: e.target.value } })} /></div><div className="field"><label htmlFor="quiz-admin-prize-value">Value (e.g. 5 USD)</label><input id="quiz-admin-prize-value" className="input" maxLength={40} value={draft.prize.value} onChange={(e) => setDraft({ ...draft, prize: { ...draft.prize, value: e.target.value } })} /></div></div>
      <div className="field"><label htmlFor="quiz-admin-prize-details">Prize details, country restrictions and manual delivery terms</label><textarea id="quiz-admin-prize-details" className="input" maxLength={1000} value={draft.prize.description} onChange={(e) => setDraft({ ...draft, prize: { ...draft.prize, description: e.target.value } })} /></div>
      <div className="field"><label htmlFor="quiz-admin-prize-image">Optional prize image (HTTPS URL)</label><input id="quiz-admin-prize-image" className="input" type="url" maxLength={2048} value={draft.prize.image_url} onChange={(e) => setDraft({ ...draft, prize: { ...draft.prize, image_url: e.target.value } })} /></div>
      {draft.prize.title && <QuizPrizeCard prize={draft.prize} />}
      <div className="row between mt-3"><h3>Questions ({draft.questions.length} / 50)</h3><button className="btn btn-outline btn-sm" type="button" disabled={draft.questions.length >= 50} onClick={() => setDraft({ ...draft, questions: [...draft.questions, { id: crypto.randomUUID(), prompt: '', options: ['', '', '', ''], correct_index: 0 }] })}>Add question</button></div>
      {draft.questions.map((q, i) => <fieldset className="quiz-admin-question" key={q.id}><legend>Question {i + 1}</legend><label className="field">Question text<textarea className="input" required maxLength={1000} value={q.prompt} onChange={(e) => editQuestion(i, { prompt: e.target.value })} /></label>
        <p className="xs muted">Select the correct option. Correct answers are never sent to participants.</p>
        {q.options.map((option, choice) => <div className="quiz-admin-option" key={choice}><input type="radio" name={`correct-${q.id}`} aria-label={`Option ${choice + 1} is correct`} checked={q.correct_index === choice} onChange={() => editQuestion(i, { correct_index: choice })} /><input className="input" aria-label={`Question ${i + 1}, option ${choice + 1}`} required maxLength={300} value={option} onChange={(e) => editQuestion(i, { options: q.options.map((old, index) => index === choice ? e.target.value : old) })} /><button className="btn btn-ghost btn-sm" type="button" aria-label={`Remove option ${choice + 1}`} disabled={q.options.length <= 2} onClick={() => editQuestion(i, { options: q.options.filter((_, index) => index !== choice), correct_index: q.correct_index === choice ? 0 : q.correct_index > choice ? q.correct_index - 1 : q.correct_index })}>×</button></div>)}
        <div className="row mt-1"><button className="btn btn-outline btn-sm" type="button" disabled={q.options.length >= 6} onClick={() => editQuestion(i, { options: [...q.options, ''] })}>Add option</button><button className="btn btn-ghost btn-sm" type="button" onClick={() => setDraft({ ...draft, questions: draft.questions.filter((_, index) => index !== i) })}>Remove question</button></div>
      </fieldset>)}
      <button className="btn btn-gold mt-2" type="submit" disabled={busy}>{busy ? 'Saving…' : 'Save draft'}</button>
    </form>}

    {manage && !draft && <section className="card mt-2 quiz-manage"><div className="row between"><h2>{manage.title} · participants & award</h2><button className="btn btn-ghost btn-sm" disabled={busy} onClick={() => void run(() => openEntries(manage.id!))}><Icon name="refresh" size={15} />Refresh</button></div>
      <p className="small muted">Highest score wins. Display ordering is not a tiebreaker. Only completed attempts can win. Correct answer counts are computed by the server.</p>
      <div className="field"><label htmlFor="quiz-entry-search">Find participant / delivery email</label><input id="quiz-entry-search" className="input" value={search} onChange={(e) => { setSearch(e.target.value); setPage(0); }} /></div>
      <div className="quiz-entry-list">{filtered.slice(page * 50, (page + 1) * 50).map((entry) => <div className="quiz-entry" key={entry.user_id}><div><code>{entry.address || entry.user_id}</code><CopyButton getText={entry.address || entry.user_id} /><div className="xs muted">{entry.submitted_at ? new Date(entry.submitted_at).toLocaleString() : 'In progress — not scored'}</div>{entry.prize_email && <div className="small">{entry.prize_email} <CopyButton getText={entry.prize_email} /></div>}</div><div><strong>{entry.submitted_at ? `${entry.score} / ${manage.questions.length}` : '—'}</strong>{entry.eligible && <span className="chip xs">Leading score</span>}</div></div>)}</div>
      {!entries.length && <p className="small muted">No attempts yet.</p>}
      {filtered.length > 50 && <div className="row mt-2"><button className="btn btn-outline btn-sm" disabled={!page} onClick={() => setPage(page - 1)}>Previous</button><span>{page + 1} / {Math.ceil(filtered.length / 50)}</span><button className="btn btn-outline btn-sm" disabled={(page + 1) * 50 >= filtered.length} onClick={() => setPage(page + 1)}>Next</button></div>}
      {manage.status === 'published' && <>
        {Date.now() < Date.parse(manage.ends_at) ? <p className="small muted">Winner selection unlocks after {new Date(manage.ends_at).toLocaleString()}.</p> : <div className="quiz-admin-decision mt-3"><h3>Close competition & confirm winner</h3>
          {eligible.length > 1 && <p className="small">Tie: {eligible.length} participants share the highest score. Select the winner yourself.</p>}
          {eligible.length ? <div className="field"><label htmlFor="quiz-winner">Highest-scoring participant</label><select id="quiz-winner" className="input" value={winner} onChange={(e) => setWinner(e.target.value)}><option value="">Choose winner…</option>{eligible.map((entry) => <option key={entry.user_id} value={entry.user_id}>{entry.address || entry.user_id} · {entry.score} / {manage.questions.length}</option>)}</select></div> : <p>No completed entries: close without a winner.</p>}
          <div className="field"><label htmlFor="quiz-decision-note">Public decision note (no private data or prize codes)</label><textarea id="quiz-decision-note" className="input" maxLength={1000} value={note} onChange={(e) => setNote(e.target.value)} /></div>
          <button className="btn btn-gold" disabled={busy || (eligible.length > 0 && !winner)} onClick={() => confirm('Confirm quiz result', 'This permanently confirms the winner. It does not send the prize or any money.', async () => { await adminFinalizeQuiz(manage.id!, winner, note); await openEntries(manage.id!); })}>Confirm result</button>
        </div>}
        <details className="mt-3"><summary>Cancel this competition</summary><div className="field mt-1"><label htmlFor="quiz-cancel-reason">Public cancellation reason</label><textarea id="quiz-cancel-reason" className="input" maxLength={1000} value={reason} onChange={(e) => setReason(e.target.value)} /></div><button className="btn btn-outline" disabled={busy || !reason.trim()} onClick={() => confirm('Cancel competition', 'Stop participation and publish this cancellation reason? No winner will be selected.', async () => { await adminCancelQuiz(manage.id!, reason); await openEntries(manage.id!); })}>Cancel competition</button></details>
      </>}
      {manage.status === 'finalized' && manage.winner_user_id && <div className="quiz-admin-decision mt-3"><h3>Manual prize delivery</h3><p><strong>{won?.address || manage.winner_user_id}</strong></p><p className="small">Delivery email: {won?.prize_email || 'Waiting for the winner to add their private email on the quiz page.'}</p>
        {manage.award_delivered_at ? <p className="small">Marked delivered: {new Date(manage.award_delivered_at).toLocaleString()}</p> : <><p className="small muted">Deliver the promised reward yourself. This panel records the hand-off only — it cannot transfer money or send a gift-card code.</p><div className="field"><label htmlFor="quiz-delivery-note">Private delivery reference / note</label><textarea id="quiz-delivery-note" className="input" maxLength={2000} value={deliveryNote} onChange={(e) => setDeliveryNote(e.target.value)} /></div><button className="btn btn-gold" disabled={busy} onClick={() => confirm('Mark prize delivered', 'Have you already delivered the prize manually? This only records completion; it sends nothing.', async () => { await adminDeliverQuizPrize(manage.id!, deliveryNote); await openEntries(manage.id!); })}>Mark manually delivered</button></>}
      </div>}
    </section>}
  </div>;
}
