#!/bin/sh
# Waits until every public listener answers its readiness check. Services on the
# edge network are probed from inside the ingress container. Stats and Game Session
# are on the private network only (their public listener answers the Web BFF alone),
# so they are probed from a throwaway curl container attached to it.
#
# The services' own images are distroless and carry no probe tool, so readiness is
# observed from outside them. Auth has no health route yet (#85 adoption), so it
# is probed with a request that must answer 401: the service is up and checking
# sessions.
set -eu

compose=${COMPOSE:-docker compose}
project=${PROJECT:-ft-transcendence}
curl_image=curlimages/curl:8.12.1
deadline=$(( $(date +%s) + ${READY_TIMEOUT:-120} ))

probe_ok() { # probe_ok <service> <path>
	$compose exec -T ingress wget -q -O /dev/null -T 3 "http://$1:8080$2" 2>/dev/null
}

probe_private() { # probe_private <service> <path>: from the private network
	docker run --rm --network "${project}_private" "$curl_image" -sf -o /dev/null --max-time 3 "http://$1.private:8080$2" 2>/dev/null
}

probe_401() { # probe_401 <service> <path>
	$compose exec -T ingress wget -S -O /dev/null -T 3 "http://$1:8080$2" 2>&1 | grep -q ' 401 '
}

all_ready() {
	probe_401 auth-service /api/v1/auth/session &&
		probe_ok web-bff /readyz &&
		probe_ok user-service /readyz &&
		probe_private stats-service /readyz &&
		probe_private game-session /readyz &&
		probe_ok game-gateway /readyz &&
		probe_ok notification-gateway /readyz
}

until all_ready; do
	if [ "$(date +%s)" -ge "$deadline" ]; then
		echo "not ready after ${READY_TIMEOUT:-120}s:" >&2
		$compose ps >&2
		exit 1
	fi
	sleep 2
done
echo "stack ready"
