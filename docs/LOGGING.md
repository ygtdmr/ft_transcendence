# Operational logging

How the backend services log, measured on the running stack (`make up`). This is the operational log only:
debugging output on stdout. The security-audit trail is a separate record with its own policy (#161) and is never
turned off by a log level. No ELK, Prometheus or Grafana is used.

## Settings

| Variable | Values | Default | Read by |
| --- | --- | --- | --- |
| `APP_ENV` | `development`, `test`, `production` | `development` | Auth, User, Stats, both gateways |
| `LOG_FORMAT` | `json`, `text` | `json` in production, `text` otherwise | Auth, User, Stats, both gateways |
| `LOG_LEVEL` | `debug`, `info`, `warn`, `error` | by environment | Auth, User, Stats, both gateways |

The stack runs every service except Game Session with `APP_ENV=production`, so its logs are JSON on stdout.
Containers never write a log file; Docker keeps stdout (`json-file`, 3 files of 10 MB per container).

## Fields

Every record is one JSON object:

| Field | Meaning | Present in |
| --- | --- | --- |
| `time`, `level`, `msg` | UTC timestamp, `DEBUG`/`INFO`/`WARN`/`ERROR`, a stable event name such as `http_request` | all six |
| `service` | the service name | Auth, User, Stats, gateways |
| `environment` | `APP_ENV` | Auth, User, Stats, gateways (Auth, User and Stats since this change) |
| `request_id` | the id the ingress stamps (`X-Request-Id`), adopted by every service | access records of Auth, User, Stats, gateways |
| `status`, `latency`, `method`, `path`, `client_ip` | one record per HTTP request | access records |

One `request_id` follows a request from the ingress's access log (same field, JSON, in
`deploy/ingress/nginx.conf`) through the services it calls.

## What is never logged

The shared logger redacts the value of any attribute whose key is sensitive (passwords, hashes, tokens, cookies,
authorization, credentials, secrets), using the same rule as API error bodies (`shared/sensitive`). Request
bodies, query strings and cookie values are not logged. `make security-test` checks that no generated secret and
no session or refresh cookie value appears in any container log after a full run.

## Gaps

| Gap | Effect | Fix | Owner |
| --- | --- | --- | --- |
| Game Session does not use the shared logger | Its records have no `service`, `environment` or `request_id`, no redaction, and it ignores `LOG_FORMAT` and `LOG_LEVEL`. | Build its logger with `shared/logging` (it does not import `shared` today). | BercemES |
| Time-based retention is not enforced | Docker rotates by size (about 30 MB per container), not by age; the 14 days proposed on #161 would need a separate cleanup. | Decide on #161: keep the size bound, or add an age limit. | #161 |
| Request-id propagation to asynchronous messages | The header is carried by `shared/rabbitmq` (#297); no service publishes or consumes yet. | Adoption with #189/#171. | zehraxaa |

## Retention

Not decided. #161 proposes at most 14 days for operational logs, never shipped to a central store, and 90 days for
audit records; nothing in this document or the stack implements either number until that issue is approved. What
exists today is the size bound above.
