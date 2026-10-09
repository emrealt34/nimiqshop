import { useCallback, useEffect, useRef, useState } from 'react';
import { AppRoot } from '../AppRoot';
import { useSheet, useToast } from '../AppProviders';
import { openLoginSheet } from '../shell/SiteShell';
import { Icon } from '../ui/Icon';
import { UnifiedThumb } from '../ui/UnifiedThumb';
import { useSession } from '../../lib/useSession';
import { friendlyApiMessage } from '../../lib/api';
import { queryParam } from '../../lib/format';
import { useT } from '../../i18n';
import { listQuizzes, getQuiz, startQuiz, saveQuizProgress, submitQuiz, claimQuizPrize, type QuizCompetition, type QuizQuestion, type QuizPrize } from '../../lib/quiz';

const PHASE_KEYS = { scheduled: 'quiz.scheduled', live: 'quiz.live', ended: 'quiz.ended', finalized: 'quiz.finalized', cancelled: 'quiz.cancelled' };

export function QuizPrizeCard({ prize }: { prize: QuizPrize }) {
  const { t } = useT();
  return <div className="quiz-prize">
    <div className="quiz-prize-thumb"><UnifiedThumb src={prize.image_url || ''} alt={prize.title} bg="#FFFFFF" /></div>
    <div className="quiz-prize-copy">
      <span className="quiz-eyebrow"><Icon name="gift" size={15} /> {t('quiz.prize')}</span>
      <h2>{prize.title}</h2>
      {prize.value && <span className="chip quiz-prize-value">{prize.value}</span>}
      {prize.description && <p className="small">{prize.description}</p>}
      <p className="xs muted">{t('quiz.manualPrize')}</p>
    </div>
  </div>;
}

export function QuizView() {
  const { t, lang } = useT();
  const authed = useSession();
  const { openSheet, closeSheet } = useSheet();
  const { toast } = useToast();
  const [items, setItems] = useState<QuizCompetition[]>([]);
  const [selected, setSelected] = useState('');
  const [competition, setCompetition] = useState<QuizCompetition | null>(null);
  const [questions, setQuestions] = useState<QuizQuestion[] | null>(null);
  const [answers, setAnswers] = useState<number[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState<'saved' | 'saving' | 'failed'>('saved');
  const [email, setEmail] = useState('');
  const progressQueue = useRef<Promise<unknown>>(Promise.resolve());
  const revision = useRef(0);
  const mounted = useRef(true);
  const operation = useRef(false);
  const scope = useRef(0);

  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => {
    let alive = true;
    if (!authed) { setItems([]); setSelected(''); setCompetition(null); setQuestions(null); setLoaded(false); return; }
    setError('');
    listQuizzes().then((list) => {
      if (!alive) return;
      setItems(list); setLoaded(true);
      const requested = queryParam('id');
      setSelected(list.find((c) => c.id === requested)?.id || list.find((c) => c.phase === 'live')?.id || list[0]?.id || '');
    }).catch((err) => { if (alive) { setError(friendlyApiMessage(err, t('quiz.requestFailed'))); setLoaded(true); } });
    return () => { alive = false; };
  }, [authed, t]);

  const refresh = useCallback(async () => {
    if (!authed || !selected) return;
    const version = scope.current;
    const c = await getQuiz(selected);
    if (!mounted.current || version !== scope.current) return;
    setCompetition(c);
    setItems((old) => old.map((item) => item.id === c.id ? c : item));
  }, [authed, selected]);

  useEffect(() => {
    let alive = true;
    scope.current++; revision.current++; setQuestions(null); setAnswers([]); setCompetition(null); setError(''); setSaving('saved');
    if (!authed || !selected) return;
    getQuiz(selected).then((c) => { if (alive) { setCompetition(c); setEmail(c.attempt?.prize_email || ''); } })
      .catch((err) => { if (alive) setError(friendlyApiMessage(err, t('quiz.requestFailed'))); });
    return () => { alive = false; };
  }, [authed, selected, t]);

  useEffect(() => {
    if (!authed || !selected) return;
    const update = () => { if (document.visibilityState === 'visible') void refresh().catch(() => {}); };
    const timer = setInterval(update, 30000);
    window.addEventListener('focus', update);
    return () => { clearInterval(timer); window.removeEventListener('focus', update); };
  }, [authed, selected, refresh]);

  const perform = async (fn: () => Promise<unknown>) => {
    if (operation.current) return;
    operation.current = true; setBusy(true); setError('');
    try { await fn(); } catch (err) { if (mounted.current) setError(friendlyApiMessage(err, t('quiz.requestFailed'))); }
    finally { operation.current = false; if (mounted.current) setBusy(false); }
  };
  const begin = () => perform(async () => {
    const version = scope.current;
    const game = await startQuiz(selected);
    if (!mounted.current || version !== scope.current) return;
    setCompetition(game.competition);
    if (game.attempt.submitted_at) { await refresh(); return; }
    setQuestions(game.questions); setAnswers(game.attempt.answers);
  });
  const choose = (index: number, answer: number) => {
    const next = answers.map((old, i) => i === index ? answer : old);
    setAnswers(next); setSaving('saving');
    const version = ++revision.current;
    const id = selected;
    const attemptScope = scope.current;
    // Serialize writes: a slow response for an older choice must not overwrite
    // a newer choice on the server. Submission waits for this queue too.
    progressQueue.current = progressQueue.current.catch(() => {}).then(() => { if (mounted.current && scope.current === attemptScope) return saveQuizProgress(id, next); })
      .then(() => { if (mounted.current && version === revision.current) setSaving('saved'); })
      .catch(() => { if (mounted.current && version === revision.current) setSaving('failed'); });
  };
  const finish = () => perform(async () => {
    const version = scope.current;
    await progressQueue.current.catch(() => {});
    if (!mounted.current || version !== scope.current) return;
    await submitQuiz(selected, answers);
    if (mounted.current && version === scope.current) { setQuestions(null); await refresh(); toast(t('quiz.submitted'), 'success'); }
  });

  if (authed === null) return <div className="container quiz-page"><div className="card" role="status">{t('common.loading')}</div></div>;
  if (!authed) return <div className="container quiz-page">
    <header className="quiz-heading"><Icon name="nimiq" size={30} /><div><h1>{t('quiz.title')}</h1><p className="muted small">{t('quiz.intro')}</p></div></header>
    <div className="card quiz-gate"><Icon name="lock" size={32} /><h2>{t('quiz.loginRequired')}</h2><p>{t('quiz.loginHint')}</p>
      <button className="btn btn-gold" onClick={() => openLoginSheet({ openSheet, closeSheet, toast })}>{t('quiz.signIn')}</button>
    </div>
  </div>;

  const date = (value: string) => new Date(value).toLocaleString(lang);
  const completed = !!competition?.attempt?.submitted_at;
  const canPlay = competition?.phase === 'live' && !completed;
  return <div className="container quiz-page">
    <header className="quiz-heading"><Icon name="nimiq" size={30} /><div><h1>{t('quiz.title')}</h1><p className="muted small">{t('quiz.intro')}</p></div></header>
    {error && <div className="alert error" role="alert">{error}</div>}
    {!loaded && <div className="card" role="status">{t('common.loading')}</div>}
    {loaded && items.length === 0 && !error && <div className="card quiz-gate"><Icon name="gift" size={32} /><h2>{t('quiz.empty')}</h2><p className="small muted">{t('quiz.emptyHint')}</p></div>}
    {items.length > 0 && <div className="field quiz-selector"><label htmlFor="quiz-select">{t('quiz.competition')}</label><select id="quiz-select" className="input" value={selected} disabled={busy || !!questions} onChange={(e) => setSelected(e.target.value)}>
      {items.map((c) => <option key={c.id} value={c.id}>{c.title} · {t(PHASE_KEYS[c.phase])}</option>)}
    </select></div>}
    {competition && <>
      <section className="card quiz-contest">
        <div className="row between"><h2>{competition.title}</h2><span className={`chip quiz-phase-${competition.phase}`}>{t(PHASE_KEYS[competition.phase])}</span></div>
        {competition.description && <p className="small">{competition.description}</p>}
        <QuizPrizeCard prize={competition.prize} />
        <dl className="quiz-facts"><div><dt>{t('quiz.starts')}</dt><dd>{date(competition.starts_at)}</dd></div><div><dt>{t('quiz.ends')}</dt><dd>{date(competition.ends_at)}</dd></div><div><dt>{t('quiz.questions')}</dt><dd>{competition.question_count}</dd></div><div><dt>{t('quiz.participants')}</dt><dd>{competition.participants}</dd></div></dl>
        <p className="small quiz-rules">{t('quiz.rules')}</p>
        {competition.cancel_reason && <div className="alert warn">{competition.cancel_reason}</div>}
        {completed && <div className="quiz-result" role="status"><Icon name="check" size={20} /><strong>{t('quiz.yourScore', { score: competition.attempt!.score, total: competition.question_count })}</strong><span className="small">{t('quiz.noRetry')}</span></div>}
        {canPlay && !questions && <><p className="xs muted">{t('quiz.startWarning')}</p><button className="btn btn-gold" disabled={busy} onClick={begin}>{busy ? t('common.loading') : competition.attempt ? t('quiz.resume') : t('quiz.start')}</button></>}
        {competition.phase === 'ended' && <p className="small muted">{t('quiz.waitDecision')}</p>}
        {competition.phase === 'finalized' && <div className="quiz-winner">
          <h3>{competition.is_winner ? t('quiz.youWon') : competition.winner ? t('quiz.winner', { participant: competition.winner }) : t('quiz.noWinner')}</h3>
          {competition.decision_note && <p className="small">{competition.decision_note}</p>}
          {competition.winner && <p className="small">{competition.award_delivered_at ? t('quiz.delivered') : t('quiz.prizePending')}</p>}
          {competition.is_winner && !competition.award_delivered_at && <form onSubmit={(e) => { e.preventDefault(); void perform(async () => { await claimQuizPrize(selected, email.trim()); await refresh(); toast(t('quiz.emailSaved'), 'success'); }); }}>
            <div className="field"><label htmlFor="quiz-prize-email">{t('quiz.prizeEmail')}</label><input id="quiz-prize-email" className="input" type="email" required maxLength={254} value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" /></div>
            <p className="xs muted">{t('quiz.emailPrivate')}</p><button className="btn btn-outline" disabled={busy}>{t('quiz.saveEmail')}</button>
          </form>}
        </div>}
      </section>
      {questions && !completed && <form className="quiz-game" onSubmit={(e) => { e.preventDefault(); void finish(); }}>
        {questions.map((q, index) => <fieldset key={q.id} className="card quiz-question" disabled={busy || competition.phase !== 'live'}>
          <legend>{index + 1}. {q.prompt}</legend>
          {q.options.map((option, choice) => <label key={choice} className={`quiz-option${answers[index] === choice ? ' is-chosen' : ''}`}><input type="radio" name={`quiz-${q.id}`} value={choice} checked={answers[index] === choice} onChange={() => choose(index, choice)} /><span>{option}</span></label>)}
        </fieldset>)}
        <div className="card quiz-submit"><p className="small">{t('quiz.answered', { count: answers.filter((a) => a >= 0).length, total: questions.length })}</p><p className="xs muted" role="status">{saving === 'saving' ? t('quiz.saving') : saving === 'failed' ? t('quiz.saveFailed') : t('quiz.saved')}</p><p className="small">{t('quiz.submitWarning')}</p>
          <button className="btn btn-gold" type="submit" disabled={busy || competition.phase !== 'live' || answers.some((a) => a < 0)}>{busy ? t('common.loading') : t('quiz.submit')}</button>
        </div>
      </form>}
      <section className="card quiz-leaderboard"><div className="row between"><h2>{t('quiz.leaderboard')}</h2><button className="btn btn-ghost btn-sm" type="button" disabled={busy} onClick={() => void perform(refresh)}><Icon name="refresh" size={15} />{t('quiz.refresh')}</button></div>
        {!competition.leaderboard?.length && <p className="small muted">{t('quiz.noResults')}</p>}
        <ol>{competition.leaderboard?.map((entry) => <li key={entry.participant}><span>{entry.is_me ? t('quiz.you') : t('quiz.player', { participant: entry.participant })}{entry.is_winner && <Icon name="star" size={16} />}</span><strong>{entry.score} / {competition.question_count}</strong></li>)}</ol>
      </section>
    </>}
  </div>;
}

export function QuizPage() { return <AppRoot activeKey="quiz"><QuizView /></AppRoot>; }
