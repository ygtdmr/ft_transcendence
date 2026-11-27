#!/usr/bin/env bash
# Negative and least-privilege checks against the RUNNING stack (`make up` first).
# Run with `make security-test`. Exit status is non-zero if any check fails.
#
# What it proves (issue #191): HTTPS only; private listeners unreachable from the
# ingress; each credential works for exactly one receiver and scope; injection,
# CSRF/CORS, cookie, size and rate abuse are refused with a contract status and
# never a 5xx; IDOR on profiles; containers run with least privilege; no secret
# appears in a log.
#
# Everything it sends is aimed at the local stack and uses throwaway accounts.
set -u

BASE=${BASE:-https://localhost:${INGRESS_PORT:-8443}}
COMPOSE=${COMPOSE:-docker compose}
PROJECT=${PROJECT:-ft-transcendence}
CURL_IMAGE=curlimages/curl:8.12.1
PASS=0
FAIL=0
# Usernames are at most 20 characters, so keep the suffix short.
SUFFIX=$(date +%s | tail -c 7)$RANDOM

ok()   { PASS=$((PASS + 1)); printf '  ok    %s\n' "$1"; }
bad()  { FAIL=$((FAIL + 1)); printf '  FAIL  %s\n' "$1"; [ -n "${2:-}" ] && printf '        %s\n' "$2"; }
check() { # check <name> <expected> <actual>
	if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "want [$2], got [$3]"; fi
}
section() { printf '\n%s\n' "$1"; }

c() { curl -sk --max-time 15 "$@"; }
status() { c -o /dev/null -w '%{http_code}' "$@"; }
header_of() { # header_of <name> <curl args...>: value of one response header
	name=$1; shift
	c -D - -o /dev/null "$@" | tr -d '\r' | awk -F': ' -v n="$(printf '%s' "$name" | tr 'A-Z' 'a-z')" 'tolower($1)==n {print $2; exit}'
}
body_code() { # body_code <curl args...>: .error.code of a JSON error body
	c "$@" | sed -n 's/.*"code":"\([A-Z_]*\)".*/\1/p' | head -1
}

# --- Throwaway accounts -------------------------------------------------------------
JAR_A=$(mktemp); JAR_B=$(mktemp)
trap 'rm -f "$JAR_A" "$JAR_B"' EXIT
PASSWORD='CorrectHorse9!Battery'
register() { # register <name> <jar>: prints the user id
	c -c "$2" -X POST "$BASE/api/v1/auth/register" -H 'Content-Type: application/json' \
		-d "{\"username\":\"$1\",\"email\":\"$1@example.com\",\"password\":\"$PASSWORD\"}" |
		sed -n 's/.*"user_id":\([0-9]*\).*/\1/p'
}
ID_A=$(register "sec_a_$SUFFIX" "$JAR_A")
ID_B=$(register "sec_b_$SUFFIX" "$JAR_B")
[ -n "$ID_A" ] && [ -n "$ID_B" ] || { echo "cannot register the throwaway accounts; is the stack up?"; exit 2; }

# ==================================================================================
section "1. Transport: HTTPS only, modern TLS, security headers"
published=$($COMPOSE ps --format '{{.Service}} {{.Publishers}}' 2>/dev/null | grep -c '0.0.0.0\|127.0.0.1')
check "exactly one service publishes a host port" "1" "$published"
check "the published port is bound to loopback only" "0" "$($COMPOSE ps --format '{{.Publishers}}' | grep -c '0\.0\.0\.0')"
plain=$(curl -s --max-time 10 -o /dev/null -w '%{http_code}' "http://localhost:${BASE##*:}/api/v1/auth/session")
check "plain HTTP to the TLS port is refused (400)" "400" "$plain"
edge_curl() { docker run --rm --network "${PROJECT}_edge" "$CURL_IMAGE" -sk --max-time 10 "$@" 2>/dev/null; }
check "TLS 1.2 is accepted" "200" "$(edge_curl --tlsv1.2 --tls-max 1.2 -o /dev/null -w '%{http_code}' https://ingress:8443/ingress/healthz)"
check "TLS 1.3 is accepted" "200" "$(edge_curl --tlsv1.3 --tls-max 1.3 -o /dev/null -w '%{http_code}' https://ingress:8443/ingress/healthz)"
check "TLS 1.1 is refused" "000" "$(edge_curl --tlsv1.1 --tls-max 1.1 -o /dev/null -w '%{http_code}' https://ingress:8443/ingress/healthz)"
check "the ingress configuration allows TLS 1.2 and 1.3 only" "ssl_protocols TLSv1.2 TLSv1.3;" "$($COMPOSE exec -T ingress nginx -T 2>/dev/null | grep -m1 'ssl_protocols' | sed 's/^ *//')"
for path in /api/v1/auth/session /no/such/route; do
	check "HSTS on $path" "max-age=31536000" "$(header_of Strict-Transport-Security "$BASE$path")"
	check "nosniff on $path" "nosniff" "$(header_of X-Content-Type-Options "$BASE$path")"
done
check "the server does not announce its version" "nginx" "$(header_of Server "$BASE/no/such/route")"

# ==================================================================================
section "2. Cookies: HttpOnly, Secure, SameSite, scoped refresh path"
cookies=$(c -D - -o /dev/null -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' \
	-d "{\"email\":\"sec_a_$SUFFIX@example.com\",\"password\":\"$PASSWORD\"}" | tr -d '\r' | grep -i '^set-cookie')
for name in ft_session ft_refresh; do
	line=$(printf '%s\n' "$cookies" | grep -i "^set-cookie: $name=")
	for attr in "HttpOnly" "Secure" "SameSite=Lax"; do
		case "$line" in *"$attr"*) ok "$name has $attr" ;; *) bad "$name has $attr" ;; esac
	done
done
case "$cookies" in *"ft_refresh="*"Path=/api/v1/auth/refresh"*) ok "ft_refresh is limited to the refresh path" ;; *) bad "ft_refresh is limited to the refresh path" ;; esac

# ==================================================================================
section "3. Cross-site requests: CSRF, CORS, WebSocket origin"
forbidden() { # forbidden <name> <curl args...>: 403 FORBIDDEN_ORIGIN, and the session survives
	check "$1" "403 FORBIDDEN_ORIGIN" "$(status -b "$JAR_A" -X DELETE "$BASE/api/v1/auth/session" "${@:2}") $(body_code -b "$JAR_A" -X DELETE "$BASE/api/v1/auth/session" "${@:2}")"
}
forbidden "write from a foreign Origin is refused" -H 'Origin: https://evil.example'
forbidden "write from the same host on another port is refused" -H 'Origin: https://localhost:9999'
forbidden "write from http:// instead of https:// is refused" -H 'Origin: http://localhost:8443'
forbidden "write with no Origin and Sec-Fetch-Site cross-site is refused" -H 'Sec-Fetch-Site: cross-site'
forbidden "write with no Origin and Sec-Fetch-Site same-site is refused" -H 'Sec-Fetch-Site: same-site'
check "the refused writes did not end the session" "200" "$(status -b "$JAR_A" "$BASE/api/v1/auth/session")"
check "a read from a foreign Origin is not a write (not checked)" "200" "$(status -b "$JAR_A" "$BASE/api/v1/auth/session" -H 'Origin: https://evil.example')"
spare() { c -c "$JAR_B.spare" -o /dev/null -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"sec_b_$SUFFIX@example.com\",\"password\":\"$PASSWORD\"}"; }
for allowed in "Origin: $BASE" "Sec-Fetch-Site: same-origin" "Sec-Fetch-Site: none" "X-Not-A-Browser: 1"; do
	spare
	check "write allowed: $allowed" "204" "$(status -b "$JAR_B.spare" -X DELETE "$BASE/api/v1/auth/session" -H "$allowed")"
done
rm -f "$JAR_B.spare"
preflight=$(c -D - -o /dev/null -X OPTIONS "$BASE/api/v1/users/me" -H 'Origin: https://evil.example' -H 'Access-Control-Request-Method: PATCH' | tr -d '\r' | grep -ic '^access-control-allow')
check "a CORS preflight is granted nothing" "0" "$preflight"
ws() { curl -sk --http1.1 --max-time 3 -o /dev/null -w '%{http_code}' "$@" -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
	-H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ=='; }
ws_code() { ws "$@" 2>/dev/null | head -c 3; }
check "notification socket: foreign Origin refused" "403" "$(ws_code -b "$JAR_A" -H 'Origin: https://evil.example' "$BASE/ws/v2/notifications")"
check "notification socket: no cookie refused" "401" "$(ws_code -H "Origin: $BASE" "$BASE/ws/v2/notifications")"
check "notification socket: cookie in the query string is not accepted" "401" "$(ws_code -H "Origin: $BASE" "$BASE/ws/v2/notifications?ft_session=x")"
check "room socket: foreign Origin refused" "403" "$(ws_code -b "$JAR_A" -H 'Origin: https://evil.example' "$BASE/ws/v2/rooms/1")"
check "room socket: unknown room is a 404 from Game Session" "404" "$(ws_code -b "$JAR_A" -H "Origin: $BASE" "$BASE/ws/v2/rooms/987654")"

# ==================================================================================
section "4. Private APIs are not reachable from the public side"
for path in /internal /internal/ /internal/v1/sessions/current /INTERNAL/v1/x //internal/v1/x /api/v1/auth/../../internal/v1/x '/api/v1/%2e%2e/internal/v1/x'; do
	got=$(status --path-as-is "$BASE$path")
	case "$got" in 404|400) ok "ingress answers $got for $path" ;; *) bad "ingress answers 404 or 400 for $path" "got $got" ;; esac
done
check "Game Session REST: a forged X-User-Id without a session is refused" "401" "$(status -X POST "$BASE/api/v2/rooms" -H 'Content-Type: application/json' -H "X-User-Id: $ID_A" -d '{"max_players":4}')"
room=$(c -b "$JAR_A" -X POST "$BASE/api/v2/rooms" -H 'Content-Type: application/json' -H "X-User-Id: $ID_B" -d '{"max_players":4}')
check "Game Session REST: the caller is the session's user, not the X-User-Id the client sent" "$ID_A" "$(printf '%s' "$room" | sed -n 's/.*"host_user_id":\([0-9]*\).*/\1/p')"
room_id=$(printf '%s' "$room" | sed -n 's/.*"room_id":\([0-9]*\).*/\1/p')
[ -z "$room_id" ] || status -b "$JAR_A" -X DELETE "$BASE/api/v2/rooms/$room_id/members/me" -H "Origin: $BASE" >/dev/null
check "the legacy room API (/api/v1/rooms) is not routed" "404" "$(status -b "$JAR_A" "$BASE/api/v1/rooms")"
check "an unknown /api path is answered by the BFF in the contract's shape" "NOT_FOUND" "$(body_code -b "$JAR_A" "$BASE/api/v1/no/such/route")"
probe() { $COMPOSE exec -T ingress wget -S -O /dev/null -T 3 "$1" 2>&1 | grep -E '^\s*HTTP/' | head -1 | awk '{print $2}'; }
for svc in auth-service user-service game-gateway notification-gateway; do
	ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"${PROJECT}_edge\").IPAddress}}" "${PROJECT}-${svc}-1" 2>/dev/null)
	[ -n "$ip" ] || { bad "$svc is on the edge network"; continue; }
	check "ingress cannot connect to $svc's private port" "" "$(probe "http://$ip:8081/internal/v1/x")"
done
# Stats and Game Session answer the Web BFF only: they are not on the network the ingress is on.
for svc in stats-service game-session; do
	nets=$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "${PROJECT}-${svc}-1")
	check "$svc is on the private network only" "${PROJECT}_private " "$nets"
	check "ingress cannot reach $svc's public port" "" "$(probe "http://$svc:8080/readyz")"
done
edge_members=$(docker network inspect "${PROJECT}_edge" -f '{{range .Containers}}{{.Name}} {{end}}' | tr ' ' '\n' | grep -v '^$' | sed "s/^${PROJECT}-//; s/-1$//" | sort | tr '\n' ' ')
check "the edge network holds the ingress and only what it forwards to" "auth-service game-gateway ingress notification-gateway user-service web-bff " "$edge_members"
ingress_nets=$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "${PROJECT}-ingress-1")
check "the ingress is on the edge network only" "${PROJECT}_edge " "$ingress_nets"
for store in postgres rabbitmq; do
	nets=$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "${PROJECT}-${store}-1")
	check "$store is on the private network only" "${PROJECT}_private " "$nets"
done
check "the private network has no route out" "true" "$(docker network inspect "${PROJECT}_private" -f '{{.Internal}}')"

# ==================================================================================
section "5. Service credentials: one per pair, scoped, no universal secret"
SECRETS=${SECRETS_DIR:-secrets}/credentials
credential_of() { if [ "$1" = "-" ]; then printf 'no-credential-sent'; else cat "$SECRETS/$1"; fi; }
private_call() { # private_call <credential file or -> <method> <url> [body]
	docker run --rm --network "${PROJECT}_private" "$CURL_IMAGE" -s -o /dev/null -w '%{http_code}' --max-time 10 \
		-H "Authorization: Bearer $(credential_of "$1")" -X "$2" -H 'Content-Type: application/json' ${4:+-d "$4"} "$3" 2>/dev/null
}
private_code() { # same, but prints the error code instead of the status
	docker run --rm --network "${PROJECT}_private" "$CURL_IMAGE" -s --max-time 10 -H "Authorization: Bearer $(credential_of "$1")" \
		-X "$2" -H 'Content-Type: application/json' ${4:+-d "$4"} "$3" 2>/dev/null | sed -n 's/.*"code":"\([A-Z_]*\)".*/\1/p' | head -1
}
REV=http://game-gateway.private:8081/internal/v1/session-revocations/3f9b2c71-5d4e-4a86-9c1b-7e2a6d0f8b53
BODY='{"user_id":42,"cause":"SESSION_REVOKED"}'
check "no credential: 401 SERVICE_UNAUTHENTICATED" "401 SERVICE_UNAUTHENTICATED" "$(private_call - PUT $REV "$BODY") $(private_code - PUT $REV "$BODY")"
check "a made-up credential is refused" "401" "$(docker run --rm --network "${PROJECT}_private" "$CURL_IMAGE" -s -o /dev/null -w '%{http_code}' -X PUT -H 'Authorization: Bearer 0000000000000000000000000000000000000000000000000000000000000000' -H 'Content-Type: application/json' -d "$BODY" $REV)"
check "another receiver's credential is refused (no universal secret)" "401" "$(private_call auth-service-to-user-service PUT $REV "$BODY")"
check "a valid credential without the scope: 403 SERVICE_FORBIDDEN" "403 SERVICE_FORBIDDEN" "$(private_call user-service-to-game-gateway PUT $REV "$BODY") $(private_code user-service-to-game-gateway PUT $REV "$BODY")"
check "the right credential with the right scope is accepted" "204" "$(private_call auth-service-to-game-gateway PUT $REV "$BODY")"
check "the reverse direction's credential is refused" "401" "$(private_call game-gateway-to-auth-service PUT $REV "$BODY")"
SESS=http://auth-service.private:8081/internal/v1/sessions/current
check "Auth: no credential on session validation" "401" "$(private_call - GET $SESS)"
ACCT=http://auth-service.private:8081/internal/v1/accounts/$ID_A
check "Auth: account metadata is refused to Game Gateway (scope account.metadata.read)" "403 SERVICE_FORBIDDEN" "$(private_call game-gateway-to-auth-service GET $ACCT) $(private_code game-gateway-to-auth-service GET $ACCT)"
check "Auth: account metadata is refused to Notification Gateway" "403" "$(private_call notification-gateway-to-auth-service GET $ACCT)"
check "Auth: account metadata is allowed to User Service" "200" "$(private_call user-service-to-auth-service GET $ACCT)"
check "Auth: a valid credential without a session cookie still gets no session (401)" "401" "$(private_call game-gateway-to-auth-service GET $SESS)"
check "Game Session: Stats' credential has no caller entry" "401" "$(private_call stats-service-to-auth-service GET http://game-session.private:8081/internal/v1/memberships/1)"
check "Auth: the Web BFF's credential is accepted for session validation (scope session.validate)" "UNAUTHORIZED" "$(private_code web-bff-to-auth-service GET $SESS)"
check "Auth: the Web BFF's credential is refused account metadata (scope)" "403 SERVICE_FORBIDDEN" "$(private_call web-bff-to-auth-service GET $ACCT) $(private_code web-bff-to-auth-service GET $ACCT)"
check "User: the Web BFF's credential is refused (presence.read is not wired yet)" "401" "$(private_call web-bff-to-auth-service GET 'http://user-service.private:8081/internal/v1/presence?user_id=1')"

# ==================================================================================
section "6. Abuse: injection, oversized input, rate limits"
for payload in "' OR '1'='1" '"; DROP TABLE users;--' '{"$ne":null}' '<script>alert(1)</script>' "$(printf 'a%.0s' $(seq 1 300))"; do
	got=$(status -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' \
		--data-binary "$(printf '{"email":%s,"password":"x"}' "$(printf '%s' "$payload" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')")")
	case "$got" in 400|401|422) ok "login with hostile email -> $got" ;; *) bad "login with hostile email must be 4xx" "got $got for: ${payload:0:30}" ;; esac
done
check "malformed JSON is 400, not 500" "400" "$(status -b "$JAR_A" -X PATCH "$BASE/api/v1/users/me" -H 'Content-Type: application/json' -H "Origin: $BASE" -d '{"username":')"
check "search with SQL metacharacters is not a server error" "200" "$(status -b "$JAR_A" "$BASE/api/v1/users/search?q=%27%20OR%201%3D1--")"
sqlpath=$(status -b "$JAR_A" "$BASE/api/v1/users/1%27%20OR%201=1")
case "$sqlpath" in 400|404|422) ok "SQL metacharacters in a path id -> $sqlpath" ;; *) bad "SQL metacharacters in a path id must be 4xx" "got $sqlpath" ;; esac
check "a 200 KB body is refused at the ingress" "413" "$(head -c 204800 /dev/zero | tr '\0' 'a' | status -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' --data-binary @-)"
big=$(status "$BASE/api/v1/auth/session" -H "X-Padding: $(head -c 20000 /dev/zero | tr '\0' 'a')")
case "$big" in 400|431|494|000) ok "a 20 KB header is refused ($big)" ;; *) bad "a 20 KB header is refused" "got $big" ;; esac
check "a 3 MB avatar upload is refused at the ingress" "413" "$(head -c 3145728 /dev/zero | status -b "$JAR_A" -X PUT "$BASE/api/v1/users/me/avatar" -H 'Content-Type: image/png' -H "Origin: $BASE" --data-binary @-)"
codes=""
for i in 1 2 3 4 5 6 7 8; do codes="$codes $(status -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"rl_$SUFFIX@example.com\",\"password\":\"wrong-password-$i\"}")"; done
case "$codes" in *429*) ok "login attempts on one email are rate limited (429):$codes" ;; *) bad "login attempts on one email must hit 429" "$codes" ;; esac

# ==================================================================================
section "7. Access control: IDOR on profiles"
check "an anonymous caller cannot read a profile" "401" "$(status "$BASE/api/v1/users/$ID_B")"
other=$(c -b "$JAR_A" "$BASE/api/v1/users/$ID_B")
case "$other" in *'"email"'*) bad "another user's profile does not expose the e-mail" "$other" ;; *"user_id"*) ok "another user's profile does not expose the e-mail" ;; *) bad "another user's profile can be read" "$other" ;; esac
own=$(c -b "$JAR_A" "$BASE/api/v1/users/me")
case "$own" in *"sec_a_$SUFFIX@example.com"*) ok "own profile includes the e-mail" ;; *) bad "own profile includes the e-mail" ;; esac
notroute() { case "$2" in 404|405) ok "$1 ($2)" ;; *) bad "$1" "got $2" ;; esac; }
notroute "no route changes another user's profile" "$(status -b "$JAR_A" -X PATCH "$BASE/api/v1/users/$ID_B" -H 'Content-Type: application/json' -H "Origin: $BASE" -d '{"username":"hijacked"}')"
notroute "no route deletes another user's account" "$(status -b "$JAR_A" -X DELETE "$BASE/api/v1/users/$ID_B" -H "Origin: $BASE")"

# ==================================================================================
section "8. Least privilege: containers, database roles, broker"
for svc in auth-service user-service stats-service game-session game-gateway notification-gateway web-bff ingress; do
	cid="${PROJECT}-${svc}-1"
	read -r user ro priv nnp caps <<<"$(docker inspect -f '{{.Config.User}}|{{.HostConfig.ReadonlyRootfs}}|{{.HostConfig.Privileged}}|{{index .HostConfig.SecurityOpt 0}}|{{.HostConfig.CapDrop}}' "$cid" | tr '|' ' ')"
	[ "$ro" = "true" ] && [ "$priv" = "false" ] && [ "$nnp" = "no-new-privileges:true" ] && [ "$caps" = "[ALL]" ] \
		&& ok "$svc: read-only, unprivileged, no-new-privileges, all capabilities dropped" \
		|| bad "$svc: hardened runtime" "readonly=$ro privileged=$priv secopt=$nnp capdrop=$caps"
	uid=$(docker image inspect -f '{{.Config.User}}' "$(docker inspect -f '{{.Image}}' "$cid")")
	case "$svc:$uid" in ingress:101|ingress:nginx|*:nonroot|*:65532*) ok "$svc: runs as a non-root user ($uid)" ;; *) bad "$svc: runs as a non-root user" "image user [$uid]" ;; esac
done
mounts=$(docker ps -q --filter "label=com.docker.compose.project=$PROJECT" | xargs docker inspect -f '{{.Name}} {{range .Mounts}}{{.Source}} {{end}}' | grep -c 'docker.sock')
check "no container mounts the Docker socket" "0" "$mounts"
hostnet=$(docker ps -q --filter "label=com.docker.compose.project=$PROJECT" | xargs docker inspect -f '{{.HostConfig.NetworkMode}}' | grep -c '^host$')
check "no container uses the host network" "0" "$hostnet"
db_matrix=""
for pair in auth:auth-service users:user-service stats:stats-service gamesession:game-session; do
	role=${pair%%:*}; svc=${pair##*:}; pw=$(cat "${SECRETS_DIR:-secrets}/db/$svc.password")
	for db in auth users stats gamesession; do
		if $COMPOSE exec -T -e PGPASSWORD="$pw" postgres psql -h 127.0.0.1 -U "$role" -d "$db" -tAc 'select 1' >/dev/null 2>&1; then
			[ "$role" = "$db" ] && r=own || r=CROSS
		else
			[ "$role" = "$db" ] && r=BROKEN || r=denied
		fi
		db_matrix="$db_matrix $r"
	done
done
check "each database role reaches only its own database" " own denied denied denied denied own denied denied denied denied own denied denied denied denied own" "$db_matrix"
users=$($COMPOSE exec -T rabbitmq rabbitmqctl list_users --silent 2>/dev/null | awk '{print $1}' | sort | tr '\n' ' ')
check "RabbitMQ has the generated account only (no guest)" "ft " "$users"

# ==================================================================================
section "9. No secret in a log or in the repository"
logs=$($COMPOSE logs --no-log-prefix 2>&1)
leaked=0
for f in "${SECRETS_DIR:-secrets}"/credentials/* "${SECRETS_DIR:-secrets}"/db/*.password "${SECRETS_DIR:-secrets}/jwt_secret" "${SECRETS_DIR:-secrets}/postgres_superuser" "${SECRETS_DIR:-secrets}/rabbitmq_password"; do
	value=$(tr -d '\n' <"$f")
	if printf '%s' "$logs" | grep -qF -- "$value"; then leaked=$((leaked + 1)); echo "        found the value of $f in a log"; fi
done
check "no generated secret appears in any container log" "0" "$leaked"
jwt=$(tr -d '\n' <"${SECRETS_DIR:-secrets}/jwt_secret")
if docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "${PROJECT}-auth-service-1" | grep -qF -- "$jwt"; then bad "the signing key is not in Auth's environment (docker inspect)"; else ok "the signing key is not in Auth's environment (docker inspect)"; fi
session_value=$(awk '$6=="ft_session"{print $7}' "$JAR_A" | head -1)
refresh_value=$(awk '$6=="ft_refresh"{print $7}' "$JAR_A" | head -1)
if [ -n "$session_value" ] && printf '%s' "$logs" | grep -qF -- "$session_value"; then bad "session cookie value is not logged"; else ok "session cookie value is not logged"; fi
if [ -n "$refresh_value" ] && printf '%s' "$logs" | grep -qF -- "$refresh_value"; then bad "refresh cookie value is not logged"; else ok "refresh cookie value is not logged"; fi
tracked=$(git ls-files secrets certs .env 2>/dev/null | wc -l | tr -d ' ')
check "no secret, key or .env is tracked by git" "0" "$tracked"
hex=$(git grep -nE '\b[0-9a-f]{64}\b' -- . ':!*.sum' ':!*.json' ':!*_test.go' ':!tests' 2>/dev/null | wc -l | tr -d ' ')
check "no 64-character hex secret in tracked source" "0" "$hex"

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
