# Nimiq quiz competitions

## Operator setup

Open `/admin?section=quiz` with the **separate operator session**. The system starts empty: it publishes no invented questions, prize or competition.

1. Create a draft. Enter a title, optional description, start and end timestamps, and the promised prize (e.g. Amazon gift card / 5 USD). Enter country restrictions in the prize details. An optional HTTPS image uses the storefront's normal product tile.
2. Add 1–50 questions, each with 2–6 distinct options and exactly one correct answer. All questions are manually authored by the operator.
3. Set a custom period or use **End of today / End of this month**. The editor shows local browser time; storage and deadline enforcement use UTC on the server. There is no automatic recurring competition: copy a previous competition into a new period when needed.
4. Publish. The server opens/closes participation at the configured times. Publishing freezes questions, prize and dates so earlier and later entrants are judged by the same rules. To replace published rules, cancel with a public reason and create/copy a new competition.
5. After the end, open **Participants & award**. Each correct answer earns one point. Only submitted attempts count. Confirm the sole top scorer, or select a winner from tied top scorers. A lower scorer cannot be chosen. A competition with no completed entries can be closed without a winner. Display order is not a speed tiebreaker.
6. Deliver the reward manually. The winner can privately provide a delivery email on `/quiz`; only that account and the operator can read it. The operator sees the winner's wallet address and email. After the external hand-off, **Mark manually delivered** records completion and an optional private reference. This operation sends **no money, gift-card code or email**.

## Participant flow and safeguards

- `/quiz` is a normal Astro/React route with the site's existing light/dark kraft theme and navigation.
- The page renders a login gate before showing competition data. **Every `/api/quiz` endpoint is protected by the existing wallet-session authentication middleware**, including list/detail reads. Mutating cookie requests use the existing CSRF checks and per-account write limiter.
- Starting creates one transactionally unique attempt per competition/account in BadgerDB. Reloads and retried start requests resume the same attempt; they do not create a second entry. Progress is server-saved, and client writes are serialized.
- Submission sends option indices, not a score. The server validates all indices and scores against its private immutable questions. Correct indices are never included in customer list/detail/start responses. The submitted attempt is immutable; a lost-response retry returns the same result.
- Server time controls entry/submission deadlines. Drafts and cancelled competitions cannot be played. Only completed attempts can win.
- This is **one attempt per account/wallet**, not a proof that each entrant is a unique human. Multiple-wallet/Sybil resistance and outside research are not prevented. No participation fee or wallet transfer is requested.
- Other players are shown with pseudonymous participant labels, not full wallet addresses or emails. Prize delivery references remain operator-only.
- Operator actions are audited. The explicit shop-reset preview includes quiz competitions, questions, indexes, attempts and delivery contacts; a confirmed full reset deletes them along with customer data.

## Deployment / checks

The frontend and Go backend must both be updated. Badger requires no SQL migration: the new `quiz:c:`, `quiz:ix:` and `quiz:a:` namespaces are created when the first draft/attempt is written.

Regression coverage includes transactional one-attempt enforcement, resume/replay safety, server scoring, frozen rules, end boundaries, tied winner selection, manual delivery idempotency, authentication/privacy API checks and browser login/layout/quiz tests.
