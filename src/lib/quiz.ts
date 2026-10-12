import { api } from './api';

export type QuizPrize = { title: string; value: string; description: string; image_url: string };
export type QuizQuestion = { id: string; prompt: string; options: string[]; type?: 'choice' | 'range'; min?: number; max?: number };
export type AdminQuizQuestion = QuizQuestion & { correct_index: number; correct_value?: number };
export type QuizAttempt = { answers: number[]; score: number; total: number; started_at: string; submitted_at: string | null; prize_email?: string };
export type QuizCompetition = {
  id: string; title: string; description: string; prize: QuizPrize; starts_at: string; ends_at: string;
  phase: 'scheduled' | 'live' | 'ended' | 'finalized' | 'cancelled'; question_count: number; participants: number;
  attempt: QuizAttempt | null; winner: string; is_winner: boolean; decision_note: string; cancel_reason: string;
  award_delivered_at: string | null;
  winner_count?: number; winners?: string[]; has_ranges?: boolean;
  leaderboard?: { rank?: number; participant: string; score: number; is_me: boolean; is_winner: boolean }[];
};
export type AdminQuiz = {
  id?: string; title: string; description: string; prize: QuizPrize; starts_at: string; ends_at: string;
  status: 'draft' | 'published' | 'cancelled' | 'finalized'; questions: AdminQuizQuestion[];
  participants?: number; winner_user_id?: string; decision_note?: string; award_delivered_at?: string; award_note?: string;
  winner_count?: number; winner_user_ids?: string[]; awards?: Record<string, { delivered_at?: string; note?: string }>;
};
export type AdminQuizEntry = { user_id: string; address: string; submitted_at?: string; score: number; prize_email: string; eligible: boolean; is_winner?: boolean; award_delivered_at?: string };

export const listQuizzes = async (): Promise<QuizCompetition[]> => (await api('/quiz', { auth: true })).items || [];
export const getQuiz = async (id: string): Promise<QuizCompetition> => await api(`/quiz/${encodeURIComponent(id)}`, { auth: true }) as QuizCompetition;
export const startQuiz = async (id: string): Promise<{ competition: QuizCompetition; attempt: QuizAttempt; questions: QuizQuestion[] }> => await api(`/quiz/${encodeURIComponent(id)}/start`, { method: 'POST', auth: true }) as { competition: QuizCompetition; attempt: QuizAttempt; questions: QuizQuestion[] };
export const saveQuizProgress = (id: string, answers: number[]) => api(`/quiz/${encodeURIComponent(id)}/answers`, { method: 'PUT', auth: true, body: { answers } });
export const submitQuiz = async (id: string, answers: number[]): Promise<QuizAttempt> => await api(`/quiz/${encodeURIComponent(id)}/submit`, { method: 'POST', auth: true, body: { answers } }) as QuizAttempt;
export const claimQuizPrize = (id: string, email: string) => api(`/quiz/${encodeURIComponent(id)}/claim`, { method: 'POST', auth: true, body: { email } });

export const adminListQuizzes = async (): Promise<AdminQuiz[]> => (await api('/admin/quiz')).items || [];
export const adminCreateQuiz = async (draft: AdminQuiz): Promise<AdminQuiz> => await api('/admin/quiz', { method: 'POST', body: draft }) as AdminQuiz;
export const adminUpdateQuiz = async (id: string, draft: AdminQuiz): Promise<AdminQuiz> => await api(`/admin/quiz/${encodeURIComponent(id)}`, { method: 'PUT', body: draft }) as AdminQuiz;
export const adminDeleteQuiz = (id: string) => api(`/admin/quiz/${encodeURIComponent(id)}`, { method: 'DELETE' });
export const adminPublishQuiz = (id: string) => api(`/admin/quiz/${encodeURIComponent(id)}/publish`, { method: 'POST' });
export const adminCancelQuiz = (id: string, reason: string) => api(`/admin/quiz/${encodeURIComponent(id)}/cancel`, { method: 'POST', body: { reason } });
export const adminQuizEntries = async (id: string): Promise<{ competition: AdminQuiz; items: AdminQuizEntry[]; best_score: number; winner_slots: number }> => await api(`/admin/quiz/${encodeURIComponent(id)}/entries`) as { competition: AdminQuiz; items: AdminQuizEntry[]; best_score: number; winner_slots: number };
export const adminFinalizeQuiz = (id: string, winners: string[], note: string) => api(`/admin/quiz/${encodeURIComponent(id)}/finalize`, { method: 'POST', body: { winner_user_ids: winners, note } });
export const adminDeliverQuizPrize = (id: string, note: string, winnerUserId?: string) => api(`/admin/quiz/${encodeURIComponent(id)}/delivered`, { method: 'POST', body: { note, winner_user_id: winnerUserId } });

export const quizScoreLabel = (score: number) => Number(score.toFixed(6)).toLocaleString(undefined, { maximumFractionDigits: 6 });

/** Imports only editable draft fields. Server IDs, status, winners and entries are never imported. */
export function parseQuizImport(text: string): AdminQuiz {
  if (new TextEncoder().encode(text).length > 256 * 1024) throw new Error('JSON must be at most 256 KB.');
  let raw: any;
  try { raw = JSON.parse(text.replace(/^\uFEFF/, '')); } catch { throw new Error('Invalid JSON. Use a single quiz object, without Markdown code fences.'); }
  if (!raw || Array.isArray(raw) || typeof raw !== 'object') throw new Error('JSON must contain one quiz object.');
  const field = (value: any, name: string, max: number, required = false) => {
    if (value === undefined && !required) return '';
    if (typeof value !== 'string' || [...value.trim()].length > max || (required && !value.trim())) throw new Error(`${name} must be text${required ? ' and cannot be empty' : ''} (max ${max} characters).`);
    return value.trim();
  };
  const winnerCount = raw.winner_count ?? 1;
  if (!Number.isInteger(winnerCount) || winnerCount < 0 || winnerCount > 10000) throw new Error('winner_count: use 1, 3, or 0 for everyone (maximum 10000).');
  if (!Array.isArray(raw.questions) || raw.questions.length < 1 || raw.questions.length > 50) throw new Error('Include 1–50 questions.');
  const questions = raw.questions.map((q: any, index: number): AdminQuizQuestion => {
    if (!q || typeof q !== 'object' || Array.isArray(q)) throw new Error(`Question ${index + 1} must be an object.`);
    const base = { id: crypto.randomUUID(), prompt: field(q.prompt, `Question ${index + 1}`, 1000, true), correct_index: 0 };
    if (q.type === 'range') {
      if (![q.min, q.max, q.correct_value].every(Number.isInteger) || q.min < 0 || q.max > 1e9 || q.max <= q.min || q.correct_value < q.min || q.correct_value > q.max || (q.options !== undefined && (!Array.isArray(q.options) || q.options.length))) throw new Error(`Question ${index + 1}: use integer min/max and correct_value inside the range, without options.`);
      return { ...base, type: 'range', options: [], min: q.min, max: q.max, correct_value: q.correct_value };
    }
    if (q.type !== undefined && q.type !== 'choice') throw new Error(`Question ${index + 1}: type must be choice or range.`);
    if (!Array.isArray(q.options) || q.options.length < 2 || q.options.length > 6 || !Number.isInteger(q.correct_index) || q.correct_index < 0 || q.correct_index >= q.options.length) throw new Error(`Question ${index + 1}: include 2–6 options and a zero-based correct_index.`);
    const options = q.options.map((o: any) => field(o, `Question ${index + 1} option`, 300, true));
    if (new Set(options.map((o: string) => o.toLocaleLowerCase())).size !== options.length) throw new Error(`Question ${index + 1}: options must be distinct.`);
    return { ...base, type: 'choice', options, correct_index: q.correct_index };
  });
  const now = new Date();
  const date = (rawDate: any, fallback: Date, name: string) => {
    if (rawDate === undefined || rawDate === '') return fallback.toISOString();
    if (typeof rawDate !== 'string' || !/T.*(?:Z|[+-]\d{2}:\d{2})$/.test(rawDate) || !Number.isFinite(Date.parse(rawDate))) throw new Error(`${name}: use an ISO timestamp with Z or an explicit timezone offset.`);
    return new Date(rawDate).toISOString();
  };
  const start = date(raw.starts_at, now, 'starts_at');
  const end = date(raw.ends_at, new Date(Date.parse(start) + 7 * 86400000), 'ends_at');
  if (Date.parse(end) <= Date.parse(start) || Date.parse(end) - Date.parse(start) > 366 * 86400000) throw new Error('ends_at must follow starts_at, within 366 days.');
  const prize = raw.prize;
  if (!prize || typeof prize !== 'object' || Array.isArray(prize)) throw new Error('Include a prize object.');
  const image = field(prize.image_url, 'Prize image', 2048);
  if (image) { let url: URL; try { url = new URL(image); } catch { throw new Error('Prize image must be an HTTPS URL.'); } if (url.protocol !== 'https:' || url.username || url.password) throw new Error('Prize image must be an HTTPS URL without credentials.'); }
  return { title: field(raw.title, 'Title', 120, true), description: field(raw.description, 'Description', 2000), winner_count: winnerCount, starts_at: start, ends_at: end, status: 'draft', questions,
    prize: { title: field(prize.title, 'Prize title', 140, true), value: field(prize.value, 'Prize value', 40), description: field(prize.description, 'Prize description', 1000), image_url: image } };
}
