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
