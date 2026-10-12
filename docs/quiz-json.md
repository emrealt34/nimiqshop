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

Each confirmed winner supplies a private delivery email. Admin records manual delivery separately for that winner. Selecting winners or marking delivery does not send money, emails or gift codes.

## Site-specific multilingual example

`docs/examples/shop-knowledge-quiz.json` contains 10 NimiqShop questions, including the fee-excluding cashback base. It is an editable draft template, not a published competition. Hypothetical percentages do not promise the operator's current cashback rate.

Optional `translations` maps accept `en`, `tr`, `de`, `fr`, `es`, `pt`. At competition and prize level each language has `title` and `description`; at question level it has `prompt` and, for multiple choice, `options` in the same order as the authored options. Range bounds and correct values are shared across every language. Missing languages fall back to authored text. Translations survive JSON import, save, edit, clone and export, and can be edited in the admin translation sections before publication. Publishing freezes translated text too. Public translation views never contain correct answers or private award records.
