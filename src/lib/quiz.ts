import { api } from './api';

export type QuizPrize = { title: string; value: string; description: string; image_url: string };
export type QuizQuestion = { id: string; prompt: string; options: string[] };
export type AdminQuizQuestion = QuizQuestion & { correct_index: number };
export type QuizAttempt = { answers: number[]; score: number; total: number; started_at: string; submitted_at: string | null; prize_email?: string };
export type QuizCompetition = {
  id: string; title: string; description: string; prize: QuizPrize; starts_at: string; ends_at: string;
  phase: 'scheduled' | 'live' | 'ended' | 'finalized' | 'cancelled'; question_count: number; participants: number;
  attempt: QuizAttempt | null; winner: string; is_winner: boolean; decision_note: string; cancel_reason: string;
  award_delivered_at: string | null;
  leaderboard?: { participant: string; score: number; is_me: boolean; is_winner: boolean }[];
};
export type AdminQuiz = {
  id?: string; title: string; description: string; prize: QuizPrize; starts_at: string; ends_at: string;
  status: 'draft' | 'published' | 'cancelled' | 'finalized'; questions: AdminQuizQuestion[];
  participants?: number; winner_user_id?: string; decision_note?: string; award_delivered_at?: string; award_note?: string;
};
export type AdminQuizEntry = { user_id: string; address: string; submitted_at?: string; score: number; prize_email: string; eligible: boolean };

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
export const adminQuizEntries = async (id: string): Promise<{ competition: AdminQuiz; items: AdminQuizEntry[]; best_score: number }> => await api(`/admin/quiz/${encodeURIComponent(id)}/entries`) as { competition: AdminQuiz; items: AdminQuizEntry[]; best_score: number };
export const adminFinalizeQuiz = (id: string, winnerUserId: string, note: string) => api(`/admin/quiz/${encodeURIComponent(id)}/finalize`, { method: 'POST', body: { winner_user_id: winnerUserId, note } });
export const adminDeliverQuizPrize = (id: string, note: string) => api(`/admin/quiz/${encodeURIComponent(id)}/delivered`, { method: 'POST', body: { note } });
