package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
	"nimiqshop/internal/db"
	"nimiqshop/internal/middleware"
)

// Explicit safe views: never serialize the database competition/question to
// customers. Correct indices, account IDs, prize emails and delivery notes
// belong only to the operator or their own account.
func quizParticipant(user string) string {
	if user == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("nimiqshop-quiz:" + user))
	return hex.EncodeToString(sum[:4])
}
func quizPhase(c db.QuizCompetition, now time.Time) string {
	if c.Status != "published" {
		return c.Status
	}
	if now.Before(c.StartsAt) {
		return "scheduled"
	}
	if !now.Before(c.EndsAt) {
		return "ended"
	}
	return "live"
}
func quizAttemptView(a db.QuizAttempt, total int) map[string]any {
	return map[string]any{"started_at": a.StartedAt, "answers": a.Answers, "submitted_at": a.SubmittedAt, "score": a.Score, "total": total, "prize_email": a.PrizeEmail}
}
func (h *Handlers) quizView(c db.QuizCompetition, user string) (map[string]any, error) {
	var mine any
	a, err := h.Store.GetQuizAttempt(c.ID, user)
	if err == nil {
		mine = quizAttemptView(a, len(c.Questions))
	} else if !errors.Is(err, db.ErrNotFound) {
		return nil, err
	}
	return map[string]any{
		"id": c.ID, "title": c.Title, "description": c.Description, "prize": c.Prize,
		"starts_at": c.StartsAt, "ends_at": c.EndsAt, "phase": quizPhase(c, time.Now().UTC()),
		"question_count": len(c.Questions), "participants": c.Participants, "attempt": mine,
		"winner": quizParticipant(c.WinnerUserID), "is_winner": c.WinnerUserID != "" && c.WinnerUserID == user,
		"decision_note": c.DecisionNote, "cancel_reason": c.CancelReason, "award_delivered_at": c.AwardDeliveredAt,
	}, nil
}
func quizError(ctx *fasthttp.RequestCtx, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(ctx, 404, "quiz or attempt not found")
	case errors.Is(err, db.ErrQuizClosed), errors.Is(err, db.ErrQuizLocked), errors.Is(err, db.ErrQuizWinner), errors.Is(err, db.ErrConflict):
		writeError(ctx, 409, err.Error())
	case errors.Is(err, db.ErrQuizAnswers):
		writeError(ctx, 400, err.Error())
	case errors.Is(err, db.ErrLimit):
		writeError(ctx, 409, "quiz participant limit reached")
	default:
		writeError(ctx, 500, "could not update quiz")
	}
}
func quizID(ctx *fasthttp.RequestCtx) string { id, _ := ctx.UserValue("id").(string); return id }

func (h *Handlers) ListQuizzes(ctx *fasthttp.RequestCtx) {
	items, err := h.Store.ListQuizCompetitions(200)
	if err != nil {
		quizError(ctx, err)
		return
	}
	out := []map[string]any{}
	for _, c := range items {
		if c.Status == "draft" {
			continue
		}
		view, err := h.quizView(c, middleware.UserID(ctx))
		if err != nil {
			quizError(ctx, err)
			return
		}
		out = append(out, view)
	}
	writeJSON(ctx, 200, map[string]any{"items": out, "server_time": time.Now().UTC()})
}
func (h *Handlers) GetQuiz(ctx *fasthttp.RequestCtx) {
	c, err := h.Store.GetQuizCompetition(quizID(ctx))
	if err != nil {
		quizError(ctx, err)
		return
	}
	if c.Status == "draft" {
		quizError(ctx, db.ErrNotFound)
		return
	}
	user := middleware.UserID(ctx)
	view, err := h.quizView(c, user)
	if err != nil {
		quizError(ctx, err)
		return
	}
	entries, err := h.Store.ListQuizEntries(c.ID)
	if err != nil {
		quizError(ctx, err)
		return
	}
	rows := []map[string]any{}
	for _, a := range entries {
		if a.SubmittedAt == nil {
			continue
		}
		rows = append(rows, map[string]any{"participant": quizParticipant(a.UserID), "score": a.Score, "is_me": a.UserID == user, "is_winner": a.UserID == c.WinnerUserID})
		if len(rows) >= 50 {
			break
		}
	}
	view["leaderboard"] = rows
	writeJSON(ctx, 200, view)
}
func (h *Handlers) StartQuiz(ctx *fasthttp.RequestCtx) {
	c, a, err := h.Store.StartQuiz(quizID(ctx), middleware.UserID(ctx), time.Now().UTC())
	if err != nil {
		quizError(ctx, err)
		return
	}
	questions := []map[string]any{}
	for _, q := range c.Questions {
		questions = append(questions, map[string]any{"id": q.ID, "prompt": q.Prompt, "options": q.Options})
	}
	view, err := h.quizView(c, middleware.UserID(ctx))
	if err != nil {
		quizError(ctx, err)
		return
	}
	writeJSON(ctx, 200, map[string]any{"competition": view, "attempt": quizAttemptView(a, len(c.Questions)), "questions": questions})
}
func (h *Handlers) SaveQuizProgress(ctx *fasthttp.RequestCtx) { h.saveQuizAnswers(ctx, false) }
func (h *Handlers) SubmitQuiz(ctx *fasthttp.RequestCtx)       { h.saveQuizAnswers(ctx, true) }
func (h *Handlers) saveQuizAnswers(ctx *fasthttp.RequestCtx, submit bool) {
	var req struct {
		Answers []int `json:"answers"`
	}
	if readJSON(ctx, &req) != nil {
		quizError(ctx, db.ErrQuizAnswers)
		return
	}
	a, err := h.Store.SaveQuizAnswers(quizID(ctx), middleware.UserID(ctx), req.Answers, submit, time.Now().UTC())
	if err != nil {
		quizError(ctx, err)
		return
	}
	writeJSON(ctx, 200, quizAttemptView(a, len(a.Answers)))
}
func (h *Handlers) ClaimQuizPrize(ctx *fasthttp.RequestCtx) {
	var req struct {
		Email string `json:"email"`
	}
	if readJSON(ctx, &req) != nil {
		writeError(ctx, 400, "valid prize delivery email required")
		return
	}
	email := strings.TrimSpace(req.Email)
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || parsed.Name != "" || len(email) > 254 {
		writeError(ctx, 400, "valid prize delivery email required")
		return
	}
	a, err := h.Store.ClaimQuizPrize(quizID(ctx), middleware.UserID(ctx), email, time.Now().UTC())
	if err != nil {
		quizError(ctx, err)
		return
	}
	writeJSON(ctx, 200, quizAttemptView(a, len(a.Answers)))
}

func (h *Handlers) AdminListQuizzes(ctx *fasthttp.RequestCtx) {
	items, err := h.Store.ListQuizCompetitions(200)
	if err != nil {
		quizError(ctx, err)
		return
	}
	writeJSON(ctx, 200, map[string]any{"items": items})
}
func (h *Handlers) AdminCreateQuiz(ctx *fasthttp.RequestCtx) { h.adminSaveQuiz(ctx, true) }
func (h *Handlers) AdminUpdateQuiz(ctx *fasthttp.RequestCtx) { h.adminSaveQuiz(ctx, false) }
func (h *Handlers) adminSaveQuiz(ctx *fasthttp.RequestCtx, create bool) {
	var req db.QuizCompetition
	if readJSON(ctx, &req) != nil {
		writeError(ctx, 400, "invalid quiz draft")
		return
	}
	if create {
		req.ID = ""
	} else {
		req.ID = quizID(ctx)
	}
	c, err := h.Store.PutQuizCompetition(req, create, time.Now().UTC())
	if err != nil {
		if errors.Is(err, db.ErrQuizLocked) || errors.Is(err, db.ErrNotFound) || errors.Is(err, db.ErrConflict) {
			quizError(ctx, err)
		} else {
			writeError(ctx, 400, err.Error())
		}
		return
	}
	h.audit(adminIdentity(ctx).User.ID, "quiz.draft.save", ctx, c.ID)
	writeJSON(ctx, 200, c)
}
func (h *Handlers) AdminDeleteQuiz(ctx *fasthttp.RequestCtx) {
	if err := h.Store.DeleteQuizDraft(quizID(ctx)); err != nil {
		quizError(ctx, err)
		return
	}
	h.audit(adminIdentity(ctx).User.ID, "quiz.draft.delete", ctx, quizID(ctx))
	writeJSON(ctx, 200, map[string]bool{"ok": true})
}
func (h *Handlers) AdminPublishQuiz(ctx *fasthttp.RequestCtx) {
	c, err := h.Store.PublishQuiz(quizID(ctx), time.Now().UTC())
	if err != nil {
		quizError(ctx, err)
		return
	}
	h.audit(adminIdentity(ctx).User.ID, "quiz.publish", ctx, c.ID)
	writeJSON(ctx, 200, c)
}
func (h *Handlers) AdminCancelQuiz(ctx *fasthttp.RequestCtx) {
	var req struct {
		Reason string `json:"reason"`
	}
	if readJSON(ctx, &req) != nil || strings.TrimSpace(req.Reason) == "" || len(req.Reason) > 4000 {
		writeError(ctx, 400, "public cancellation reason required")
		return
	}
	c, err := h.Store.CancelQuiz(quizID(ctx), req.Reason, time.Now().UTC())
	if err != nil {
		quizError(ctx, err)
		return
	}
	h.audit(adminIdentity(ctx).User.ID, "quiz.cancel", ctx, c.ID)
	writeJSON(ctx, 200, c)
}
func (h *Handlers) AdminQuizEntries(ctx *fasthttp.RequestCtx) {
	c, err := h.Store.GetQuizCompetition(quizID(ctx))
	if err != nil {
		quizError(ctx, err)
		return
	}
	entries, err := h.Store.ListQuizEntries(c.ID)
	if err != nil {
		quizError(ctx, err)
		return
	}
	ids := make([]string, len(entries))
	for i, a := range entries {
		ids[i] = a.UserID
	}
	addresses, err := h.Store.UserAddresses(ids)
	if err != nil {
		quizError(ctx, err)
		return
	}
	best := -1
	for _, a := range entries {
		if a.SubmittedAt != nil && a.Score > best {
			best = a.Score
		}
	}
	rows := []map[string]any{}
	for _, a := range entries {
		rows = append(rows, map[string]any{"user_id": a.UserID, "address": addresses[a.UserID], "started_at": a.StartedAt, "submitted_at": a.SubmittedAt, "score": a.Score, "prize_email": a.PrizeEmail, "eligible": a.SubmittedAt != nil && a.Score == best})
	}
	writeJSON(ctx, 200, map[string]any{"competition": c, "items": rows, "best_score": best})
}
func (h *Handlers) AdminFinalizeQuiz(ctx *fasthttp.RequestCtx) {
	var req struct {
		Winner string `json:"winner_user_id"`
		Note   string `json:"note"`
	}
	if readJSON(ctx, &req) != nil || len(req.Note) > 4000 {
		writeError(ctx, 400, "invalid winner decision")
		return
	}
	c, err := h.Store.FinalizeQuiz(quizID(ctx), req.Winner, req.Note, time.Now().UTC())
	if err != nil {
		quizError(ctx, err)
		return
	}
	h.audit(adminIdentity(ctx).User.ID, "quiz.finalize", ctx, c.ID+" winner="+c.WinnerUserID)
	writeJSON(ctx, 200, c)
}
func (h *Handlers) AdminDeliverQuizPrize(ctx *fasthttp.RequestCtx) {
	var req struct {
		Note string `json:"note"`
	}
	if readJSON(ctx, &req) != nil || len(req.Note) > 8000 {
		writeError(ctx, 400, "invalid private delivery note")
		return
	}
	c, err := h.Store.MarkQuizPrizeDelivered(quizID(ctx), req.Note, time.Now().UTC())
	if err != nil {
		quizError(ctx, err)
		return
	}
	h.audit(adminIdentity(ctx).User.ID, "quiz.prize.delivered", ctx, c.ID)
	// This records an EXTERNAL manual hand-off; it does not send money/mail.
	writeJSON(ctx, 200, c)
}
