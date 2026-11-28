#!/bin/sh
# Proves, on the running stack, that a service credential can be rotated with no
# downtime and that the old value stops working afterwards (issue #191, #157).
#
# It rotates one pair (Notification Gateway -> Auth, which holds session.validate),
# using temporary files and a temporary Compose override. The real secrets under
# secrets/ are never touched, and Auth is put back as it was on exit.
#
#   1. receiver accepts the new credential AND the previous one  (both work)
#   2. the previous one is dropped from the receiver              (old refused, new works)
#
# The probe asks Auth to validate a session without a cookie: a credential that is
# accepted gets `UNAUTHORIZED` (no session), one that is refused gets
# `SERVICE_UNAUTHENTICATED`. No session is involved and nothing is changed.
set -eu

compose=${COMPOSE:-docker compose}
project=${PROJECT:-ft-transcendence}
image=curlimages/curl:8.12.1
pair=notification-gateway-to-auth-service
env_current=NOTIFICATION_GATEWAY_CREDENTIAL_FILE
env_previous=NOTIFICATION_GATEWAY_PREVIOUS_CREDENTIAL_FILE

tmp=$(mktemp -d)
restore() {
	rm -rf "$tmp"
	$compose up -d --force-recreate auth-service >/dev/null 2>&1 || true
	COMPOSE="$compose" sh scripts/wait-ready.sh >/dev/null 2>&1 || true
}
trap restore EXIT

old=$(cat "${SECRETS_DIR:-secrets}/credentials/$pair")
new=$(openssl rand -hex 32)
printf '%s' "$old" >"$tmp/old"
printf '%s' "$new" >"$tmp/new"
chmod 0444 "$tmp/old" "$tmp/new"

probe() { # probe <credential value>: the error code Auth answers with
	docker run --rm --network "${project}_private" "$image" -s --max-time 10 \
		-H "Authorization: Bearer $1" http://auth-service.private:8081/internal/v1/sessions/current |
		sed -n 's/.*"code":"\([A-Z_]*\)".*/\1/p'
}
expect() { # expect <description> <credential value> <UNAUTHORIZED = accepted | SERVICE_UNAUTHENTICATED = refused>
	got=$(probe "$2")
	if [ "$got" = "$3" ]; then echo "  ok    $1"; else echo "  FAIL  $1 (answered $got, want $3)"; failed=1; fi
}
failed=0

write_override() { # write_override <with previous: yes|no>
	{
		echo "services:"
		echo "  auth-service:"
		echo "    environment:"
		echo "      $env_current: /run/secrets/rotation-new"
		if [ "$1" = yes ]; then echo "      $env_previous: /run/secrets/rotation-old"; fi
		if [ "$1" = yes ]; then echo "    secrets: [rotation-new, rotation-old]"; else echo "    secrets: [rotation-new]"; fi
		echo "secrets:"
		echo "  rotation-new: {file: $tmp/new}"
		if [ "$1" = yes ]; then echo "  rotation-old: {file: $tmp/old}"; fi
	} >"$tmp/override.yml"
}
apply() {
	$compose -f docker-compose.yml -f "$tmp/override.yml" up -d --force-recreate auth-service >/dev/null 2>&1
	COMPOSE="$compose" sh scripts/wait-ready.sh >/dev/null
}

echo "Before rotation:"
expect "the current credential is accepted" "$old" UNAUTHORIZED
expect "a credential nobody holds is refused" "$(openssl rand -hex 32)" SERVICE_UNAUTHENTICATED

echo "Step 1: Auth accepts the new credential and the previous one:"
write_override yes
apply
expect "new credential is accepted" "$new" UNAUTHORIZED
expect "previous credential is still accepted (the caller can be switched without downtime)" "$old" UNAUTHORIZED

echo "Step 2: the previous credential is dropped:"
write_override no
apply
expect "new credential is accepted" "$new" UNAUTHORIZED
expect "previous credential is now refused" "$old" SERVICE_UNAUTHENTICATED

[ "$failed" -eq 0 ] && echo "rotation drill passed" || { echo "rotation drill FAILED"; exit 1; }
