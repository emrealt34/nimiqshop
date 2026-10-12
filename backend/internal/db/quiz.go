package db

// Quiz competitions are admin-authored, free to enter and non-custodial.
// Answers and scoring stay on the server. One transactional attempt key per
// competition/account is the rule, not a localStorage flag.
import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
)

const (
	quizCompetitionPrefix = "quiz:c:"
	quizIndexPrefix       = "quiz:ix:"
	quizAttemptPrefix     = "quiz:a:"
	quizMaxParticipants   = 10000
)

var (
	ErrQuizClosed  = errors.New("quiz is not open")
	ErrQuizLocked  = errors.New("published questions and completed attempts cannot be changed")
	ErrQuizAnswers = errors.New("invalid or incomplete quiz answers")
	ErrQuizWinner  = errors.New("select the configured number of eligible completed participants; higher scores must win before boundary ties")
)

type QuizQuestion struct {
	ID           string   `json:"id"`
	Prompt       string   `json:"prompt"`
	Options      []string `json:"options"`
	CorrectIndex int      `json:"correct_index"`
	Type         string   `json:"type,omitempty"`
	Min          int      `json:"min"`
	Max          int      `json:"max"`
	CorrectValue *int     `json:"correct_value,omitempty"`
}

type QuizAward struct {
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	Note        string     `json:"note,omitempty"` // admin only
}

type QuizPrize struct {
	Title       string `json:"title"`
	Value       string `json:"value"`
	Description string `json:"description"`
	ImageURL    string `json:"image_url"`
}

type QuizCompetition struct {
	ID               string               `json:"id"`
	Title            string               `json:"title"`
	Description      string               `json:"description"`
	Prize            QuizPrize            `json:"prize"`
	StartsAt         time.Time            `json:"starts_at"`
	EndsAt           time.Time            `json:"ends_at"`
	Status           string               `json:"status"`
	Questions        []QuizQuestion       `json:"questions"`
	Participants     int                  `json:"participants"`
	WinnerUserID     string               `json:"winner_user_id,omitempty"`
	WinnerCount      *int                 `json:"winner_count,omitempty"` // nil = legacy single winner; zero = all completed entries
	WinnerUserIDs    []string             `json:"winner_user_ids,omitempty"`
	Awards           map[string]QuizAward `json:"awards,omitempty"` // private delivery records
	DecisionNote     string               `json:"decision_note,omitempty"`
	CancelReason     string               `json:"cancel_reason,omitempty"`
	AwardNote        string               `json:"award_note,omitempty"` // PRIVATE: never in customer responses
	AwardDeliveredAt *time.Time           `json:"award_delivered_at,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

type QuizAttempt struct {
	CompetitionID string     `json:"competition_id"`
	UserID        string     `json:"user_id"`
	StartedAt     time.Time  `json:"started_at"`
	Answers       []int      `json:"answers"`
	SubmittedAt   *time.Time `json:"submitted_at,omitempty"`
	Score         int        `json:"score"`
	Points        int64      `json:"points,omitempty"`      // millionths of a point, avoiding floating-point tie decisions
	PrizeEmail    string     `json:"prize_email,omitempty"` // owner + admin only
}

func quizKey(id string) []byte { return []byte(quizCompetitionPrefix + id) }
func quizIndexKey(c QuizCompetition) []byte {
	return []byte(quizIndexPrefix + reverseTS(c.CreatedAt.UnixNano()) + ":" + c.ID)
}
func quizAttemptKey(id, user string) []byte { return []byte(quizAttemptPrefix + id + ":" + user) }
func putQuizJSON(txn *badger.Txn, key []byte, value any) error {
	blob, err := marshal(value)
	if err != nil {
		return err
	}
	return txn.Set(key, blob)
}

func validQuizText(s string, min, max int) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(s))
	return n >= min && n <= max
}
func validateQuiz(c QuizCompetition) error {
	if !validQuizText(c.Title, 1, 120) || !validQuizText(c.Description, 0, 2000) || !validQuizText(c.Prize.Title, 1, 140) || !validQuizText(c.Prize.Value, 0, 40) || !validQuizText(c.Prize.Description, 0, 1000) {
		return fmt.Errorf("quiz title/prize or description is missing or too long")
	}
	if c.StartsAt.IsZero() || c.EndsAt.IsZero() || !c.EndsAt.After(c.StartsAt) || c.EndsAt.Sub(c.StartsAt) > 366*24*time.Hour {
		return fmt.Errorf("set a start and end time, no more than 366 days apart")
	}
	if c.Prize.ImageURL != "" {
		u, err := url.Parse(c.Prize.ImageURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(c.Prize.ImageURL) > 2048 {
			return fmt.Errorf("prize image must be an HTTPS URL")
		}
	}
	if len(c.Questions) > 50 {
		return fmt.Errorf("a quiz can contain at most 50 questions")
	}
	if c.WinnerCount != nil && (*c.WinnerCount < 0 || *c.WinnerCount > quizMaxParticipants) {
		return fmt.Errorf("winner_count must be 0 (everyone) or 1–10000")
	}
	ids := map[string]bool{}
	for _, q := range c.Questions {
		if q.ID == "" || ids[q.ID] || !validQuizText(q.Prompt, 1, 1000) {
			return fmt.Errorf("each question needs a unique ID and question text")
		}
		ids[q.ID] = true
		if q.Type == "range" {
			if q.Min < 0 || q.Max > 1_000_000_000 || q.Max <= q.Min || q.CorrectValue == nil || *q.CorrectValue < q.Min || *q.CorrectValue > q.Max || len(q.Options) != 0 {
				return fmt.Errorf("range questions need integer bounds 0–1000000000, max > min and a correct_value within the range; no options")
			}
			continue
		}
		if (q.Type != "" && q.Type != "choice") || q.CorrectValue != nil || len(q.Options) < 2 || len(q.Options) > 6 || q.CorrectIndex < 0 || q.CorrectIndex >= len(q.Options) {
			return fmt.Errorf("each question needs a unique ID, 2–6 options and a correct answer")
		}
		seen := map[string]bool{}
		for _, option := range q.Options {
			key := strings.ToLower(strings.TrimSpace(option))
			if !validQuizText(option, 1, 300) || seen[key] {
				return fmt.Errorf("question options must be nonempty and distinct")
			}
			seen[key] = true
		}
	}
	return nil
}

// Only drafts can be edited. Publish freezes questions, prize and dates so
// an admin cannot silently change the rules after somebody has entered.
func (s *Store) PutQuizCompetition(input QuizCompetition, create bool, now time.Time) (QuizCompetition, error) {
	if create && input.ID == "" {
		input.ID = uuid.NewString()
	}
	if input.ID == "" || strings.ContainsAny(input.ID, ":/\\") || len(input.ID) > 80 {
		return QuizCompetition{}, fmt.Errorf("invalid quiz ID")
	}
	for i := range input.Questions {
		if input.Questions[i].Type == "range" && input.Questions[i].Options == nil {
			input.Questions[i].Options = []string{}
		}
		if input.Questions[i].ID == "" {
			input.Questions[i].ID = uuid.NewString()
		}
		input.Questions[i].Prompt = strings.TrimSpace(input.Questions[i].Prompt)
		for j := range input.Questions[i].Options {
			input.Questions[i].Options[j] = strings.TrimSpace(input.Questions[i].Options[j])
		}
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Prize.Title = strings.TrimSpace(input.Prize.Title)
	input.StartsAt = input.StartsAt.UTC()
	input.EndsAt = input.EndsAt.UTC()
	if err := validateQuiz(input); err != nil {
		return QuizCompetition{}, err
	}
	var result QuizCompetition
	err := s.Update(func(txn *badger.Txn) error {
		var old QuizCompetition
		err := getJSON(txn, quizKey(input.ID), &old)
		if create {
			if err == nil {
				return ErrConflict
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		} else {
			if err != nil {
				return err
			}
			if old.Status != "draft" {
				return ErrQuizLocked
			}
		}
		created := old.CreatedAt
		if create {
			created = now.UTC()
		}
		result = QuizCompetition{ID: input.ID, Title: input.Title, Description: input.Description, Prize: input.Prize, StartsAt: input.StartsAt, EndsAt: input.EndsAt, Status: "draft", Questions: input.Questions, WinnerCount: input.WinnerCount, CreatedAt: created, UpdatedAt: now.UTC()}
		if err := putQuizJSON(txn, quizKey(result.ID), result); err != nil {
			return err
		}
		return txn.Set(quizIndexKey(result), []byte(result.ID))
	})
	return result, err
}

func (s *Store) GetQuizCompetition(id string) (QuizCompetition, error) {
	var c QuizCompetition
	err := s.View(func(txn *badger.Txn) error { return getJSON(txn, quizKey(id), &c) })
	return c, err
}
func (s *Store) ListQuizCompetitions(limit int) ([]QuizCompetition, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	out := []QuizCompetition{}
	err := s.View(func(txn *badger.Txn) error {
		return scanIndex(txn, []byte(quizIndexPrefix), limit, func(id string) error {
			var c QuizCompetition
			if err := getJSON(txn, quizKey(id), &c); err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	})
	return out, err
}
func (s *Store) DeleteQuizDraft(id string) error {
	return s.Update(func(txn *badger.Txn) error {
		var c QuizCompetition
		if err := getJSON(txn, quizKey(id), &c); err != nil {
			return err
		}
		if c.Status != "draft" {
			return ErrQuizLocked
		}
		if err := txn.Delete(quizIndexKey(c)); err != nil {
			return err
		}
		return txn.Delete(quizKey(id))
	})
}
func (s *Store) PublishQuiz(id string, now time.Time) (QuizCompetition, error) {
	var c QuizCompetition
	err := s.Update(func(txn *badger.Txn) error {
		if err := getJSON(txn, quizKey(id), &c); err != nil {
			return err
		}
		if c.Status != "draft" {
			return ErrQuizLocked
		}
		if len(c.Questions) == 0 || !c.EndsAt.After(now) {
			return ErrQuizClosed
		}
		if err := validateQuiz(c); err != nil {
			return err
		}
		c.Status = "published"
		c.UpdatedAt = now.UTC()
		return putQuizJSON(txn, quizKey(id), c)
	})
	return c, err
}
func (s *Store) CancelQuiz(id, reason string, now time.Time) (QuizCompetition, error) {
	var c QuizCompetition
	if !validQuizText(reason, 1, 1000) {
		return c, fmt.Errorf("a public cancellation reason is required")
	}
	err := s.Update(func(txn *badger.Txn) error {
		if err := getJSON(txn, quizKey(id), &c); err != nil {
			return err
		}
		if c.Status != "draft" && c.Status != "published" {
			return ErrQuizLocked
		}
		c.Status = "cancelled"
		c.CancelReason = strings.TrimSpace(reason)
		c.UpdatedAt = now.UTC()
		return putQuizJSON(txn, quizKey(id), c)
	})
	return c, err
}
func quizOpen(c QuizCompetition, now time.Time) bool {
	return c.Status == "published" && !now.Before(c.StartsAt) && now.Before(c.EndsAt)
}
func (s *Store) GetQuizAttempt(id, user string) (QuizAttempt, error) {
	var a QuizAttempt
	err := s.View(func(txn *badger.Txn) error { return getJSON(txn, quizAttemptKey(id, user), &a) })
	return a, err
}
func (s *Store) StartQuiz(id, user string, now time.Time) (QuizCompetition, QuizAttempt, error) {
	var c QuizCompetition
	var a QuizAttempt
	if user == "" {
		return c, a, ErrNotFound
	}
	err := s.Update(func(txn *badger.Txn) error {
		if err := getJSON(txn, quizKey(id), &c); err != nil {
			return err
		}
		if !quizOpen(c, now) {
			return ErrQuizClosed
		}
		err := getJSON(txn, quizAttemptKey(id, user), &a)
		if err == nil {
			return nil
		} // retry/reload resumes the SAME attempt
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if c.Participants >= quizMaxParticipants {
			return ErrLimit
		}
		answers := make([]int, len(c.Questions))
		for i := range answers {
			answers[i] = -1
		}
		a = QuizAttempt{CompetitionID: id, UserID: user, StartedAt: now.UTC(), Answers: answers}
		c.Participants++
		c.UpdatedAt = now.UTC()
		if err := putQuizJSON(txn, quizKey(id), c); err != nil {
			return err
		}
		return putQuizJSON(txn, quizAttemptKey(id, user), a)
	})
	return c, a, err
}
func (s *Store) SaveQuizAnswers(id, user string, answers []int, submit bool, now time.Time) (QuizAttempt, error) {
	var a QuizAttempt
	err := s.Update(func(txn *badger.Txn) error {
		var c QuizCompetition
		if err := getJSON(txn, quizKey(id), &c); err != nil {
			return err
		}
		if err := getJSON(txn, quizAttemptKey(id, user), &a); err != nil {
			return err
		}
		if a.SubmittedAt != nil {
			if submit {
				return nil
			} // lost response is safe to retry, never rescores
			return ErrQuizLocked
		}
		if !quizOpen(c, now) {
			return ErrQuizClosed
		}
		if len(answers) != len(c.Questions) {
			return ErrQuizAnswers
		}
		for i, answer := range answers {
			q := c.Questions[i]
			if q.Type == "range" {
				if answer != -1 && (answer < q.Min || answer > q.Max) || (submit && answer == -1) {
					return ErrQuizAnswers
				}
				continue
			}
			if answer < -1 || answer >= len(c.Questions[i].Options) || (submit && answer == -1) {
				return ErrQuizAnswers
			}
		}
		a.Answers = append([]int(nil), answers...)
		if submit {
			at := now.UTC()
			a.SubmittedAt = &at
			a.Score = 0
			a.Points = 0
			for i, answer := range a.Answers {
				q := c.Questions[i]
				if q.Type == "range" {
					distance := int64(answer) - int64(*q.CorrectValue)
					if distance < 0 {
						distance = -distance
					}
					a.Points += 1_000_000 - distance*1_000_000/int64(q.Max-q.Min)
					if distance == 0 {
						a.Score++
					}
					continue
				}
				if answer == c.Questions[i].CorrectIndex {
					a.Score++
					a.Points += 1_000_000
				}
			}
		}
		return putQuizJSON(txn, quizAttemptKey(id, user), a)
	})
	return a, err
}
func quizEntries(txn *badger.Txn, id string) ([]QuizAttempt, error) {
	out := []QuizAttempt{}
	prefix := []byte(quizAttemptPrefix + id + ":")
	opts := badger.DefaultIteratorOptions
	opts.Prefix = prefix
	it := txn.NewIterator(opts)
	defer it.Close()
	for it.Rewind(); it.ValidForPrefix(prefix); it.Next() {
		if len(out) >= quizMaxParticipants {
			break
		}
		var a QuizAttempt
		if err := getJSON(txn, it.Item().KeyCopy(nil), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.SubmittedAt != nil) != (b.SubmittedAt != nil) {
			return a.SubmittedAt != nil
		}
		if QuizPoints(a) != QuizPoints(b) {
			return QuizPoints(a) > QuizPoints(b)
		}
		return a.UserID < b.UserID // display order only, NEVER a tiebreaker
	})
	return out, nil
}
func (s *Store) ListQuizEntries(id string) ([]QuizAttempt, error) {
	var out []QuizAttempt
	err := s.View(func(txn *badger.Txn) error { var err error; out, err = quizEntries(txn, id); return err })
	return out, err
}

// QuizPoints also reads legacy multiple-choice attempts without stored points.
func QuizPoints(a QuizAttempt) int64 {
	if a.Points == 0 && a.Score > 0 {
		return int64(a.Score) * 1_000_000
	}
	return a.Points
}
func QuizWinnerLimit(c QuizCompetition) int {
	if c.WinnerCount == nil {
		return 1
	}
	return *c.WinnerCount
}
func QuizWinners(c QuizCompetition) []string {
	if len(c.WinnerUserIDs) > 0 {
		return c.WinnerUserIDs
	}
	if c.WinnerUserID != "" {
		return []string{c.WinnerUserID}
	}
	return nil
}
func QuizIsWinner(c QuizCompetition, user string) bool {
	if user == "" {
		return false
	}
	for _, id := range QuizWinners(c) {
		if id == user {
			return true
		}
	}
	return false
}
func QuizAwardTime(c QuizCompetition, user string) *time.Time {
	if award, ok := c.Awards[user]; ok {
		return award.DeliveredAt
	}
	if len(c.WinnerUserIDs) == 0 {
		return c.AwardDeliveredAt
	}
	return nil
}

// All entries above the cutoff must win; a boundary tie is decided by the admin.
func QuizEligibility(c QuizCompetition, entries []QuizAttempt) (map[string]bool, int) {
	completed := []QuizAttempt{}
	for _, a := range entries {
		if a.SubmittedAt != nil {
			completed = append(completed, a)
		}
	}
	n := QuizWinnerLimit(c)
	if n == 0 || n > len(completed) {
		n = len(completed)
	}
	eligible := map[string]bool{}
	if n == 0 {
		return eligible, 0
	}
	cutoff := QuizPoints(completed[n-1])
	for _, a := range completed {
		if QuizPoints(a) >= cutoff {
			eligible[a.UserID] = true
		}
	}
	return eligible, n
}
func (s *Store) FinalizeQuiz(id, winner, note string, now time.Time) (QuizCompetition, error) {
	ids := []string{}
	if winner != "" {
		ids = append(ids, winner)
	}
	return s.FinalizeQuizWinners(id, ids, note, now)
}
func (s *Store) FinalizeQuizWinners(id string, winners []string, note string, now time.Time) (QuizCompetition, error) {
	var c QuizCompetition
	if !validQuizText(note, 0, 1000) {
		return c, fmt.Errorf("decision note is too long")
	}
	err := s.Update(func(txn *badger.Txn) error {
		if err := getJSON(txn, quizKey(id), &c); err != nil {
			return err
		}
		if c.Status != "published" || now.Before(c.EndsAt) {
			return ErrQuizClosed
		}
		entries, err := quizEntries(txn, id)
		if err != nil {
			return err
		}
		eligible, count := QuizEligibility(c, entries)
		if QuizWinnerLimit(c) == 0 {
			winners = nil
			for _, a := range entries {
				if a.SubmittedAt != nil {
					winners = append(winners, a.UserID)
				}
			}
		}
		if len(winners) != count {
			return ErrQuizWinner
		}
		selected := map[string]bool{}
		for _, id := range winners {
			if !eligible[id] || selected[id] {
				return ErrQuizWinner
			}
			selected[id] = true
		}
		if count > 0 {
			completed := []QuizAttempt{}
			for _, a := range entries {
				if a.SubmittedAt != nil {
					completed = append(completed, a)
				}
			}
			cutoff := QuizPoints(completed[count-1])
			for _, a := range completed {
				if QuizPoints(a) > cutoff && !selected[a.UserID] {
					return ErrQuizWinner
				}
			}
		}
		// Store winners in score order, independent of the admin's selection order.
		ordered := []string{}
		for _, a := range entries {
			if selected[a.UserID] {
				ordered = append(ordered, a.UserID)
			}
		}
		c.Status = "finalized"
		c.WinnerUserIDs = ordered
		c.WinnerUserID = ""
		if len(ordered) > 0 {
			c.WinnerUserID = ordered[0]
		}
		c.DecisionNote = strings.TrimSpace(note)
		c.UpdatedAt = now.UTC()
		return putQuizJSON(txn, quizKey(id), c)
	})
	return c, err
}
func (s *Store) ClaimQuizPrize(id, user, email string, now time.Time) (QuizAttempt, error) {
	var a QuizAttempt
	err := s.Update(func(txn *badger.Txn) error {
		var c QuizCompetition
		if err := getJSON(txn, quizKey(id), &c); err != nil {
			return err
		}
		if c.Status != "finalized" || !QuizIsWinner(c, user) || QuizAwardTime(c, user) != nil {
			return ErrQuizWinner
		}
		if err := getJSON(txn, quizAttemptKey(id, user), &a); err != nil {
			return err
		}
		a.PrizeEmail = email
		return putQuizJSON(txn, quizAttemptKey(id, user), a)
	})
	return a, err
}
func (s *Store) MarkQuizPrizeDelivered(id, note string, now time.Time) (QuizCompetition, error) {
	c, err := s.GetQuizCompetition(id)
	if err != nil {
		return c, err
	}
	if len(QuizWinners(c)) != 1 {
		return c, ErrQuizWinner
	}
	return s.MarkQuizWinnerPrizeDelivered(id, QuizWinners(c)[0], note, now)
}
func (s *Store) MarkQuizWinnerPrizeDelivered(id, user, note string, now time.Time) (QuizCompetition, error) {
	var c QuizCompetition
	if !validQuizText(note, 0, 2000) {
		return c, fmt.Errorf("delivery note is too long")
	}
	err := s.Update(func(txn *badger.Txn) error {
		if err := getJSON(txn, quizKey(id), &c); err != nil {
			return err
		}
		if c.Status != "finalized" || !QuizIsWinner(c, user) {
			return ErrQuizWinner
		}
		if QuizAwardTime(c, user) != nil {
			return nil
		}
		at := now.UTC()
		if c.Awards == nil {
			c.Awards = map[string]QuizAward{}
		}
		c.Awards[user] = QuizAward{DeliveredAt: &at, Note: strings.TrimSpace(note)}
		allDelivered := true
		for _, id := range QuizWinners(c) {
			if QuizAwardTime(c, id) == nil {
				allDelivered = false
			}
		}
		if allDelivered {
			c.AwardDeliveredAt = &at
		}
		if len(QuizWinners(c)) == 1 {
			c.AwardNote = strings.TrimSpace(note)
		}
		c.UpdatedAt = at
		return putQuizJSON(txn, quizKey(id), c)
	})
	return c, err
}
