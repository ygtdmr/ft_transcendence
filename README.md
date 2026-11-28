# ft_transcendence — backend

Go backend services for `ft_transcendence`, a real-time multiplayer word-association game inspired by Codenames.

## Branch

This is the `backend` development branch, based on `main` and periodically synchronized with `origin/main`. Stable, verified work merges back into `main` through a reviewed Pull Request.

## Related branches

- `main` — primary integration branch, canonical integrated project state.
- `frontend` — React frontend implementation.
- `game` — room/game domain implementation and tests; transport and persistence integration require verification.
- `auth-service` — authentication and User Service work, including subsystem PRs; this does not imply integration into `main`.
- `bruno` — REST and WebSocket API contract (Bruno collection); treat it as the source of truth for endpoints, payloads and events.
- `ui-design` — visual design system and reference assets.

## Status

This `backend` checkout is an integration placeholder. Implementation exists on subsystem branches, including `game` and `auth-service`; inspect their current commits and PR targets before assessing completion. Their source does not establish complete deployed services or end-to-end integration.
