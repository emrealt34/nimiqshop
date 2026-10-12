# Importing AI-authored quizzes

Open Admin → Quiz and upload one JSON file, or paste its contents. Loading only opens the editor: it does not save or publish. Edit the title, dates, reward, winner count and questions, then **Save draft**. A saved draft can be edited again. **Publish** is a separate confirmation; published questions, reward, dates and winner rules are locked. Clone or cancel a published competition to replace it fairly.

JSON files have a 256 KB limit, 1–50 questions, and this format:

```json
{
  "schema_version": 1,
  "title": "Closest-answer quiz",
  "description": "Rules and reward terms",
  "winner_count": 3,
  "prize": {
    "title": "Set the promised reward before publishing",
    "value": "",
    "description": "Each winner receives this reward; specify country and delivery terms",
    "image_url": ""
  },
  "questions": [
    {"type": "range", "prompt": "How many seconds are in 1.5 minutes?", "min": 1, "max": 180, "correct_value": 90},
    {"type": "choice", "prompt": "Which ticker belongs to Nimiq?", "options": ["NIM", "BTC"], "correct_index": 0}
  ]
}
```

Use `winner_count: 1` for one winner, `3` for top three, or `0` for everyone who submits a complete attempt before the deadline. A smaller completed field awards everyone in that field. The same displayed reward applies separately to each winner. At the cutoff, the admin chooses among equal scores; participants above that score cannot be skipped.

Dates are optional. Omitted `starts_at` uses import time; omitted `ends_at` uses seven days after that start. Explicit dates require ISO timestamps with `Z` or a timezone offset, e.g. `2026-10-12T12:00:00+03:00`. Review dates and reward details in the editor before publishing.

Numeric ranges use whole numbers with `0 <= min < max <= 1000000000`. Correct values must be inside the range. A range question has no options. Each range question awards `1 − |answer − correct_value| / (max − min)` points, rounded upwards to six decimal places; each choice question awards 1 or 0. Scores are computed by the server. The leaderboard displays all six decimal places when needed, and results appear on the quiz page, not Activity.

For an AI prompt, ask for **one JSON object without Markdown fences**, using this schema, 10 questions, the chosen bounds and correct values, and the desired `winner_count`. Verify generated answers before publishing. IDs, participants, status and winner records from imported JSON are ignored. Correct values/indices stay in admin views and are not sent to participants.

For manual rewards, each confirmed winner supplies a private delivery email and admin records manual delivery. With automatic NIM rewards enabled, the sender pays the winner’s signed-in Nimiq wallet; no email claim is required.

## Site-specific multilingual example

`docs/examples/shop-knowledge-quiz.json` is an unpublished nimshop.io template with 9 multiple-choice questions and 1 numeric cashback calculation. The owner-selected questions explain country-specific gift-card validity and contacting CryptoRefills from the delivery email address. GitHub infrastructure and anonymous-order questions are excluded. Fee inclusion, privacy and other factual rules are scored as choices, not as approximate numerical guesses. It is an editable draft template, not a published competition. Hypothetical percentages do not promise the operator's current cashback rate.

Optional `translations` maps accept `en`, `tr`, `de`, `fr`, `es`, `pt`. At competition and prize level each language has `title` and `description`; at question level it has `prompt` and, for multiple choice, `options` in the same order as the authored options. Range bounds and correct values are shared across every language. Missing languages fall back to authored text. Translations survive JSON import, save, edit, clone and export, and can be edited in the admin translation sections before publication. Publishing freezes translated text too. Public translation views never contain correct answers or private award records.

## Single-question participation

Participants see one question at a time, with progress and previous/next controls. Next stays disabled until a valid answer has been saved; failed saves expose a retry. Reloading resumes the first unanswered question, or the last question when every answer is saved. Final submission is available only at the last question, after all answers are valid and saved. Earlier answers remain editable before submission.

The example reward is a **total 10,000 NIM pool with 2,000 NIM per winner for up to five winners**, not 10,000 NIM per winner. It enables automatic payouts from the configured cashback wallet, with an end date of 11 November 2026 at 12:00 Turkey time (`2026-11-11T12:00:00+03:00`). Import still creates only a draft; publishing is explicit. If fewer than five participants finish, each selected winner still receives 2,000 NIM; the unused pool is not automatically redistributed.

## Published score-tie policy

`tie_break: "submitted_at"` ranks completed entries by server-computed points descending, then immutable server-confirmed final submission time ascending. Exact timestamp ties use a deterministic internal participant-ID fallback. The same order drives the public leaderboard, admin eligibility and winner finalization. The operator cannot substitute a later equal-score entry for an earlier one. Submitting again does not change the original completion timestamp. Missing `tie_break` preserves legacy administrator-decided ties for existing competitions; Changes to questions or ranking after participation archive that version with its attempts intact and create a fresh published version. Metadata/date edits retain the existing attempts until the payout deadline. Financial records are locked at the deadline.

The selected example uses `winner_count: 5`, a total 10,000 NIM pool and 2,000 NIM per winner. It has one attempt per account and no one-participant-per-IP restriction.

## Automatic NIM prizes

```json
"payout": {
  "enabled": true,
  "source": "cashback_wallet",
  "pool_nim": 10000,
  "per_winner_nim": 2000
},
"winner_count": 5,
"tie_break": "submitted_at",
"ends_at": "2026-11-11T12:00:00+03:00"
```

Amounts are positive whole NIM. The total pool must cover every configured winner. Automatic payouts require a fixed winner count (1–100) and server submission-time ties. No wallet seed belongs in JSON. The existing cashback sender schedules a durable separate prize queue after the deadline. Insufficient balance leaves payments pending until funding. Prize transfers include memos and appear in the owner’s cashback payment history without changing purchase cashback or staking totals. Admin may edit a published competition through fields or JSON before its automatic payout deadline. Finalized payout obligations cannot be cancelled, rewritten or marked delivered manually.
