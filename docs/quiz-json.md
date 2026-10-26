# Cashback quiz JSON

Upload or paste JSON in Admin → Quiz. Loading opens an editable draft; saving and publishing are separate actions. Admin can edit dates, translations, questions and cashback settings, and export the edited JSON.

```json
{
  "schema_version": 1,
  "title": "How does nimshop work?",
  "description": "Complete the quiz to unlock your cashback code.",
  "starts_at": "2026-10-13T12:00:00+03:00",
  "ends_at": "2026-11-11T12:00:00+03:00",
  "cashback_reward": {
    "code": "ILOVENIM",
    "code_mode": "shared",
    "cashback_bps": 100,
    "max_recipients": 0,
    "min_score_percent": 0
  },
  "questions": [
    {"type": "choice", "prompt": "Where is a country-specific gift card valid?", "options": ["Its purchase country", "Every country"], "correct_index": 0}
  ]
}
```

- `code_mode`: `shared` uses the configured code for everyone; `unique` issues a different account-bound code after each qualifying completion. Omission defaults to shared. In unique mode, `code` is the prefix. Existing earned codes remain valid when settings change.
- `cashback_bps`: 100 means 1%, 500 means 5%; valid range 1–10000.
- `max_recipients`: accounts allowed to unlock the shared code. Zero means unlimited. Quiz participation is unlimited regardless of this cap.
- `min_score_percent`: zero requires completion of every question; 100 requires a perfect score.
- The shared code activates only for the signed-in completing account. Knowing the code is insufficient. Each account can redeem it once; concurrent checkouts cannot consume it twice. An abandoned reservation follows the existing checkout expiry/release rules.
- Dates accept UTC or an explicit offset. The editor displays Turkey time (UTC+03:00). Missing dates default to import time and seven days later.

Questions support 2–6 choices or integer ranges with min, max and correct_value. Range questions cannot contain options. The server computes scores; participant responses never contain the answer key. Questions appear one at a time and progress resumes after reload.

The multilingual 10-question template is [shop-knowledge-quiz.json](examples/shop-knowledge-quiz.json). Translations support en, tr, de, fr, es and pt. Review AI-generated facts and answers before publishing. Changing scoring after participation archives the previous version and preserves its answers.

New quizzes award cashback codes rather than NIM transfers. Open future legacy contests are converted while preserving existing answers and an archive of the old prize configuration. Historical prize records and already queued transfers remain intact.
