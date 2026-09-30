# What does this change?

<!-- One or two sentences. What is the user-visible effect, and why now? -->

## Type

- [ ] Bug fix
- [ ] Feature
- [ ] Refactor / cleanup
- [ ] Build, CI or release
- [ ] Documentation

## How was it checked?

<!-- The commands you ran, not the ones CI runs. e.g. `npm run verify`,
     `cd backend && go test -race ./...`, `npx playwright test --project=phone checkout` -->

## Checklist

- [ ] `npm run verify` passes (types, i18n parity, API contract, workflow policy, lint)
- [ ] `cd backend && go test -race ./...` passes
- [ ] New Go code keeps `gofmt`/`go vet`/`golangci-lint` clean and has tests
- [ ] User-visible strings added to **all** locale files in `src/i18n/locales/`
- [ ] No secret, wallet seed, supplier key or `.env` content in the diff
- [ ] Touched a payment, auth or cashback path? Say so explicitly and explain the test

## Screenshots

<!-- UI change: before/after, desktop and a phone width. Not needed for backend-only work. -->

## Security

- [ ] This change touches authentication, sessions, payment or wallet handling
- [ ] I did **not** find a vulnerability (if you did, do not open a PR — see `SECURITY.md`)
