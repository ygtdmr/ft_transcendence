# Shared tests

All tests live here: `unit` covers response formatting, logging, configuration
loading and secret redaction, the single-Go-version guard for `backend/go.work`,
pagination and pool configuration; `integration` covers connection failures and
real PostgreSQL connectivity. The timeout test starts a local TCP listener.

Run from `backend/shared`:

```sh
go test ./...
go test ./tests/unit
go test ./tests/integration
go test -coverpkg=./httpresponse,./logging,./pagination,./postgres ./tests/...
```

Real PostgreSQL connectivity requires `POSTGRES_TEST_DATABASE_URL`. That test is
skipped when the variable is unset.

The origin tests (`tests/integration/origin_chain_test.go`) need no external
services: they check that a foreign WebSocket origin is refused before the cookie
validator is ever asked, that cross-site state-changing requests are refused
before authentication, and that no CORS header is ever sent.

The access-control tests (`tests/integration/access_control_test.go`) need no
external services; they drive real Gin servers through the shared guards and
check that rejected requests never reach a handler and that no credential
reaches the logs.

The response tests (`tests/integration/response_mapping_test.go`) need no external
services; they check the contract's cookie attributes, the 204 logout, error
mapping, and that an internal error's text reaches neither the response nor the
logs.

The validation tests (`tests/integration/validate_test.go`) need no external
services; they drive a real Gin server and compare against the literal Bruno
example for an invalid path ID.

The request-context tests (`tests/integration/request_context_test.go`) need no
external services except `TestRequestTimeoutStopsARealDatabaseQuery`, which needs
`POSTGRES_TEST_DATABASE_URL`.

The lifecycle tests (`tests/integration/lifecycle_test.go`) need no external
services: they start real HTTP servers, and `TestServeHandlesARealSIGTERM` builds
`tests/integration/testdata/lifecycle_server` with the local `go` toolchain and
sends the process a real `SIGTERM`. Only `TestReadinessTracksARealPostgresOutage`
needs `POSTGRES_TEST_DATABASE_URL` plus `POSTGRES_TEST_CONTAINER`.

The dependency tests (`tests/integration/dependencies_test.go`) are skipped per
variable: `RABBITMQ_TEST_URL`, `REDIS_TEST_URL` (AOF on, with a password),
`REDIS_TEST_URL_NO_AOF`. The outage drills additionally need
`RABBITMQ_TEST_CONTAINER`, `REDIS_TEST_CONTAINER` and `POSTGRES_TEST_CONTAINER`,
the names of **disposable** Docker containers the tests may stop and start.
Never point them at a shared stack. Example:

```sh
docker run -d --name t-rabbit -p 127.0.0.1:25672:5672 -e RABBITMQ_DEFAULT_USER=svc -e RABBITMQ_DEFAULT_PASS=itest-pass rabbitmq:4-alpine
docker run -d --name t-redis  -p 127.0.0.1:26379:6379 redis:7-alpine redis-server --appendonly yes --requirepass itest-pass
docker run -d --name t-redis-plain -p 127.0.0.1:26380:6379 redis:7-alpine redis-server --appendonly no
docker run -d --name t-pg -p 127.0.0.1:25432:5432 -e POSTGRES_USER=svc -e POSTGRES_PASSWORD=itest-pass -e POSTGRES_DB=app postgres:17-alpine
RABBITMQ_TEST_URL='amqp://svc:itest-pass@127.0.0.1:25672/' RABBITMQ_TEST_CONTAINER=t-rabbit \
REDIS_TEST_URL='redis://:itest-pass@127.0.0.1:26379/0' REDIS_TEST_URL_NO_AOF='redis://127.0.0.1:26380/0' REDIS_TEST_CONTAINER=t-redis \
POSTGRES_TEST_DATABASE_URL='postgres://svc:itest-pass@127.0.0.1:25432/app?sslmode=disable' POSTGRES_TEST_CONTAINER=t-pg \
  go test -race -count=1 ./tests/integration
```
