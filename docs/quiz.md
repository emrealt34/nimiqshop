# Cashback quizzes

The authenticated `/quiz` page presents one question at a time and saves progress on the server. Admin → Quiz supports JSON import/export, translations, editing and explicit publication. See [the JSON configuration](quiz-json.md).

Completing a quiz unlocks its configured cashback code for the completing account, subject to an optional score threshold and recipient cap. Participation is unlimited. Grants and cap increments commit atomically with submission; retries do not grant twice. A shared code cannot bypass account eligibility, and each account has one redemption through the existing checkout reservation lifecycle.

Authentication, CSRF checks, server deadlines and private answer keys apply. Submitted answers are immutable. Editing scoring preserves previous attempts in an archived version. Grants survive server restarts; database backup and restore remain necessary for storage failure recovery.

New cashback quizzes do not create NIM prize transfers or request a prize delivery email. Existing historical awards and queued payment obligations remain durable and retain the existing payout recovery mechanism.

Regression tests cover account gating, partial completion, duplicate submission, concurrent recipient caps, score thresholds, restart persistence, legacy conversion, real API checkout reservations and browser flows at mobile and desktop widths.
