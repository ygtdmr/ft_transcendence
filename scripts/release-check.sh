#!/usr/bin/env bash
# Release check (#199): one command that produces the evidence the release proof asks for.
#
#   scripts/release-check.sh            every backend module: gofmt, build, vet, `go mod tidy -diff`,
#                                       and `go test -race` against REAL PostgreSQL, RabbitMQ and Redis
#   scripts/release-check.sh --stack    the same, then `make up` and the stack suites
#                                       (security-test, integration-test, rotation-drill)
#
# It starts throwaway containers on random loopback ports and removes them when it ends, so it
# never touches the stack's data. It writes release-evidence/<UTC time>-<commit>.md (git-ignored):
# the commit, the environment, the exact commands and per-module counts, so a run can be pasted
# into the issue. A failed step does not stop the run; the exit status is non-zero if anything
# failed, and also if any test was SKIPPED, because a skipped real-dependency test proves nothing
# (RELEASE_ALLOW_SKIPS=1 reports skips without failing).
#
# What it does not prove, and says so in the report: behaviour under representative load, a real
# Google sign-in, the frontend, and a second gateway instance.
set -u -o pipefail

cd "$(dirname "$0")/.."
WITH_STACK=0
[ "${1:-}" = "--stack" ] && WITH_STACK=1

MODULES="shared auth-service user-service stats-service game-gateway notification-gateway web-bff game-session"
RUN=$(date -u +%Y%m%dT%H%M%SZ)
SHA=$(git rev-parse --short HEAD)
DIRTY=$(git status --porcelain | grep -c -v '^??' || true)
OUT=release-evidence
LOGS="$OUT/$RUN-$SHA.logs"
REPORT="$OUT/$RUN-$SHA.md"
mkdir -p "$LOGS"
FAILURES=0
SKIPS=0

say() { printf '%s\n' "$*" | tee -a "$REPORT"; }
fail() { FAILURES=$((FAILURES + 1)); }

# --- Throwaway real dependencies -------------------------------------------------------------
SUFFIX=$$
PG=rc-pg-$SUFFIX
MQ=rc-mq-$SUFFIX
RD=rc-redis-$SUFFIX
RP=rc-redis-plain-$SUFFIX
cleanup() { docker rm -f "$PG" "$MQ" "$RD" "$RP" >/dev/null 2>&1; }
trap cleanup EXIT

# The ports are fixed for the whole run, not random: shared's outage drills stop and start these containers,
# and Docker gives a restarted container a new random host port, which would leave every later module
# dialing a dead one.
free_port() { python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }

wait_dependencies() {
	i=0
	until docker exec "$PG" pg_isready -U svc -d app -q 2>/dev/null &&
		docker exec "$MQ" rabbitmq-diagnostics -q ping >/dev/null 2>&1 &&
		docker exec "$RD" redis-cli -a itest-pass ping 2>/dev/null | grep -q PONG &&
		docker exec "$RP" redis-cli ping 2>/dev/null | grep -q PONG; do
		i=$((i + 1))
		[ "$i" -lt 90 ] || return 1
		sleep 1
	done
}

start_dependencies() {
	PGP=$(free_port) MQP=$(free_port) RDP=$(free_port) RPP=$(free_port)
	docker run -d --name "$PG" -p "127.0.0.1:$PGP:5432" -e POSTGRES_USER=svc -e POSTGRES_PASSWORD=itest-pass \
		-e POSTGRES_DB=app postgres:17.11-alpine >/dev/null || return 1
	# --user rabbitmq: as root the broker cannot read its own Erlang cookie on some hosts.
	docker run -d --name "$MQ" --user rabbitmq -p "127.0.0.1:$MQP:5672" -e RABBITMQ_DEFAULT_USER=svc \
		-e RABBITMQ_DEFAULT_PASS=itest-pass rabbitmq:4.4.0-alpine >/dev/null || return 1
	docker run -d --name "$RD" -p "127.0.0.1:$RDP:6379" redis:7-alpine redis-server --appendonly yes \
		--requirepass itest-pass >/dev/null || return 1
	docker run -d --name "$RP" -p "127.0.0.1:$RPP:6379" redis:7-alpine redis-server --appendonly no >/dev/null || return 1

	wait_dependencies || return 1

	# Game Session's database tests expect the schema to exist; they reset the tables themselves.
	docker exec "$PG" psql -q -U svc -d app -c 'CREATE DATABASE gs' >/dev/null || return 1
	for file in $(ls backend/game-session/migrations/*.up.sql | sort); do
		docker exec -i "$PG" psql -q -v ON_ERROR_STOP=1 -U svc -d gs <"$file" >/dev/null || return 1
	done

	export POSTGRES_TEST_DATABASE_URL="postgres://svc:itest-pass@127.0.0.1:$PGP/app?sslmode=disable"
	export AUTH_TEST_DATABASE_URL=$POSTGRES_TEST_DATABASE_URL USER_TEST_DATABASE_URL=$POSTGRES_TEST_DATABASE_URL
	export STATS_TEST_DATABASE_URL=$POSTGRES_TEST_DATABASE_URL POSTGRES_TEST_CONTAINER=$PG
	export GS_DB="postgres://svc:itest-pass@127.0.0.1:$PGP/gs?sslmode=disable"
	export RABBITMQ_TEST_URL="amqp://svc:itest-pass@127.0.0.1:$MQP/"
	export STATS_TEST_RABBITMQ_URL=$RABBITMQ_TEST_URL RABBITMQ_TEST_CONTAINER=$MQ
	export REDIS_TEST_URL="redis://:itest-pass@127.0.0.1:$RDP/0" REDIS_TEST_CONTAINER=$RD
	export REDIS_TEST_URL_NO_AOF="redis://127.0.0.1:$RPP/0"
}

# --- Header ------------------------------------------------------------------------------------
: >"$REPORT"
say "# Release check at $SHA"
say ""
say "- **Commit:** \`$(git rev-parse HEAD)\` ($(git log -1 --format=%s | cut -c1-90))"
say "- **Uncommitted changes to tracked files:** $DIRTY"
say "- **Started (UTC):** $RUN"
say "- **Host:** $(uname -sr) $(uname -m)"
say "- **Go:** $(go version | awk '{print $3, $4}')   **Docker:** $(docker version --format '{{.Server.Version}}' 2>/dev/null)   **Compose:** $(docker compose version --short 2>/dev/null)"
say "- **Command:** \`scripts/release-check.sh$([ "$WITH_STACK" = 1 ] && echo ' --stack')\`; per module: \`gofmt -l .\`, \`GOWORK=off go build ./...\`, \`go vet ./...\`, \`go mod tidy -diff\`, \`go test -race -count=1 -v ./...\` (Game Session with \`-p 1\`: its database tests share one database)"
say ""

# --- Modules -----------------------------------------------------------------------------------
say "## Modules, against real PostgreSQL 17.11, RabbitMQ 4.4.0 and Redis 7"
say ""
if ! start_dependencies >"$LOGS/dependencies.log" 2>&1; then
	say "**The throwaway dependencies did not start** (see \`$LOGS/dependencies.log\`). Nothing below can be trusted."
	fail
fi
say "| Module | gofmt | build | vet | tidy | passed | failed | skipped | time |"
say "| --- | --- | --- | --- | --- | --- | --- | --- | --- |"
SKIP_REASONS="$LOGS/skip-reasons.txt"
: >"$SKIP_REASONS"
for m in $MODULES; do
	dir=backend/$m
	# An earlier module's outage drill may have just restarted a dependency.
	wait_dependencies >/dev/null 2>&1 || { say "| $m | dependencies did not come back | | | | | | | |"; fail; continue; }
	started=$(date +%s)
	fmt=ok
	[ -z "$(cd "$dir" && gofmt -l . | head -1)" ] || { fmt=FAIL; fail; }
	build=ok
	(cd "$dir" && GOWORK=off go build ./...) >"$LOGS/$m.build.log" 2>&1 || { build=FAIL; fail; }
	vet=ok
	(cd "$dir" && GOWORK=off go vet ./...) >"$LOGS/$m.vet.log" 2>&1 || { vet=FAIL; fail; }
	tidy=ok
	(cd "$dir" && GOWORK=off go mod tidy -diff) >"$LOGS/$m.tidy.log" 2>&1 || { tidy=FAIL; fail; }
	log="$LOGS/$m.test.log"
	tested=0
	# Game Session's database tests share one database (GS_DB) and reset its tables, so its packages must
	# not run at the same time (`-p 1`); the other modules isolate their tests in their own schemas.
	parallel=""
	[ "$m" = game-session ] && parallel="-p 1"
	(cd "$dir" && GOWORK=off go test $parallel -race -count=1 -timeout 20m -v ./...) >"$log" 2>&1 || tested=1
	passed=$(grep -c -E '^\s*--- PASS' "$log" || true)
	failed=$(grep -c -E '^\s*--- FAIL' "$log" || true)
	skipped=$(grep -c -E '^\s*--- SKIP' "$log" || true)
	if [ "$tested" = 1 ] || [ "$failed" != 0 ]; then fail; failed="$failed (go test failed)"; fi
	SKIPS=$((SKIPS + skipped))
	grep -A1 -E '^\s*--- SKIP' "$log" | grep -v -E '^\s*--- SKIP|^--$' | sed -E 's/^ +//' | sed "s|^|$m: |" >>"$SKIP_REASONS"
	say "| $m | $fmt | $build | $vet | $tidy | $passed | $failed | $skipped | $(($(date +%s) - started)) s |"
done
say ""
if [ "$SKIPS" != 0 ]; then
	say "**$SKIPS test(s) were skipped**, so they proved nothing here. Distinct reasons:"
	say ""
	sed -E 's/^([^:]+): [^ ]+:[0-9]+: /\1: /' "$SKIP_REASONS" | sort | uniq -c | sort -rn | head -12 | sed 's/^/    /' | tee -a "$REPORT"
	say ""
	[ "${RELEASE_ALLOW_SKIPS:-0}" = 1 ] || fail
fi
cleanup

# --- The running stack -------------------------------------------------------------------------
if [ "$WITH_STACK" = 1 ]; then
	say "## The stack"
	say ""
	say "| Step | Result |"
	say "| --- | --- |"
	if make up >"$LOGS/make-up.log" 2>&1; then say "| \`make up\` | ready |"; else say "| \`make up\` | **FAILED** |"; fail; fi
	if make security-test >"$LOGS/security-test.log" 2>&1; then r=ok; else r=FAILED; fail; fi
	say "| \`make security-test\` | $r: $(grep -E '[0-9]+ passed, [0-9]+ failed' "$LOGS/security-test.log" | tail -1) |"
	if make integration-test >"$LOGS/integration-test.log" 2>&1; then r=ok; else r=FAILED; fail; fi
	say "| \`make integration-test\` | $r: $(grep -c -E '^\s*--- PASS' "$LOGS/integration-test.log" || true) passed, $(grep -c -E '^\s*--- FAIL' "$LOGS/integration-test.log" || true) failed |"
	if make rotation-drill >"$LOGS/rotation-drill.log" 2>&1; then r=ok; else r=FAILED; fail; fi
	say "| \`make rotation-drill\` | $r |"
	say ""
fi

# --- Verdict -----------------------------------------------------------------------------------
say "## Not proven by this run"
say ""
say "- Behaviour under representative load, and several users on different clients playing real games."
say "- A real Google sign-in, and the frontend (not served by the stack yet)."
say "- A second gateway instance (the contract assumes one), and a real upgrade with a new migration."
[ "$WITH_STACK" = 1 ] || say "- The running stack: run with \`--stack\` for the security test, the integration test and the rotation drill."
say ""
if [ "$FAILURES" = 0 ]; then
	say "**Result: PASS** (commit \`$SHA\`)"
else
	say "**Result: FAIL** ($FAILURES step(s) failed; logs in \`$LOGS\`)"
fi
say ""
say "Report: \`$REPORT\`"
[ "$FAILURES" = 0 ]
