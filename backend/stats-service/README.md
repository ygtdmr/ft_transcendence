# stats-service

Owns finalized match results, win and loss counters, and match history. It does
not store passwords, sessions, profiles or live game state. The API contract is the
Bruno collection on the `bruno` branch.

This service records finalized matches through `service.MatchIngest` and leave
penalties through `service.PenaltyIngest`. Nothing calls them at runtime yet: the
RabbitMQ consumer is not implemented.

## Endpoints

| Method and path | Meaning                                                                           |
| --------------- | --------------------------------------------------------------------------------- |
| `GET /healthz`  | Liveness. 200 while the process serves HTTP. Never touches the database.          |
| `GET /readyz`   | Readiness. 200 while PostgreSQL answers, otherwise 503 `DEPENDENCY_UNAVAILABLE`.  |
| `GET /api/v1/users/me/matches` | The signed-in user's match history, newest first.             |
| `GET /internal/v1/user-stats/{user_id}` | Private listener only. Counters for User Service (`stats.read`). |
| `PUT /internal/v1/account-cleanups/{operation_id}` | Private listener only. Removes a deleted user's identity (`account.cleanup`). |

Level is `floor(wins / 4) + 1`. A user with no records has zero counters and level 1.

## Configuration

Copy `.env.example` to `.env` for reference. The service reads the process
environment only and does not load `.env` itself.

| Variable                  | Default         | Meaning                                                 |
| ------------------------- | --------------- | ------------------------------------------------------- |
| `DATABASE_URL`            | none, required  | `postgres://` URL. The service uses its own `stats` schema. |
| `PORT`                    | `8080`          | HTTP port.                                              |
| `APP_ENV`                 | `development`   | `development`, `production` or `test`. Sets log defaults. |
| `LOG_FORMAT`, `LOG_LEVEL` | by `APP_ENV`    | `text` or `json`; `debug`, `info`, `warn` or `error`.   |
| `MIGRATE_ON_START`        | `true`          | Apply embedded migrations at startup.                   |
| `INTERNAL_PORT`           | `8090`          | Private listener port; must differ from `PORT`.         |
| `USER_SERVICE_CREDENTIAL_FILE`, `USER_SERVICE_PREVIOUS_CREDENTIAL_FILE` | empty | User Service's credential files. Empty accepts no caller. |
| `AUTH_INTERNAL_URL`, `AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE` | empty | Auth's private listener and this service's credential. Required in production. |
| `AUTH_INTERNAL_TIMEOUT`   | `5s`            | Upper bound for one call to Auth.                       |

## Finalized matches

`MatchIngest.Ingest` takes the payload of Game Session's `match.finalized` event:

```json
{
  "game_id": 5001,
  "room_id": 1001,
  "winner": "RED",
  "loser": "BLUE",
  "end_reason": "ALL_TEAM_CARDS_REVEALED",
  "score": { "red": 9, "blue": 5 },
  "started_at": "2026-09-10T08:30:00Z",
  "finished_at": "2026-09-10T08:50:00Z",
  "players": [
    { "user_id": 42, "username": "player_one", "team": "RED", "role": "OPERATIVE" }
  ]
}
```

- `username` is the name recorded with the match and never changes afterwards.
- One transaction stores the match, its participants and each player's counters.
- `game_id` decides duplicates. A repeated or concurrent delivery changes nothing
  and returns `false`, even if its content differs from the first delivery.
- An event that can never be stored returns `service.ErrInvalidMatch`. A cancelled
  game is not a finalized match and is refused.

## Leave penalties

`PenaltyIngest.Ingest` takes the event id and payload of Game Session's
`player.penalized` record. The event id decides duplicates, and `points` comes from
the event, so this service never computes a penalty. A penalty changes only
`penalty_points`.

## Account cleanup

One transaction clears the user's counters and penalty points, and anonymizes
retained matches as `user_id: null`, `username: "deleted_user"`. `stats.deleted_users`
keeps the user id and operation id so a replay answers the same receipt and a late
event cannot recreate the identity.

## Tests

From this directory:

```
go test ./...
```

Integration tests need a PostgreSQL server where the role may create databases.
Set `STATS_TEST_DATABASE_URL` to run them; otherwise they are skipped.
