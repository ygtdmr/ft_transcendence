#!/bin/sh
# Waits until every public listener answers its readiness check. Services on the
# edge network are probed from inside the ingress container. Stats and Game Session
# are not on it (their public listener answers the Web BFF alone), so they are probed
# from a throwaway curl container attached to the network that listener is on: private
# for Stats, game-rest for Game Session.
#
# The services' own images are distroless and carry no probe tool, so readiness is
# observed from outside them, through each service's /readyz.
set -eu

compose=${COMPOSE:-docker compose}
project=${PROJECT:-ft-transcendence}
curl_image=curlimages/curl:8.12.1
deadline=$(( $(date +%s) + ${READY_TIMEOUT:-120} ))

probe_ok() { # probe_ok <service> <path>
	$compose exec -T ingress wget -q -O /dev/null -T 3 "http://$1:8080$2" 2>/dev/null
}

probe_on() { # probe_on <network> <host> <path>: from a throwaway container on that network
	docker run --rm --network "${project}_$1" "$curl_image" -sf -o /dev/null --max-time 3 "http://$2:8080$3" 2>/dev/null
}

all_ready() {
	probe_ok auth-service /readyz &&
		probe_ok web-bff /readyz &&
		probe_ok user-service /readyz &&
		probe_on private stats-service.private /readyz &&
		probe_on game-rest game-session.rest /readyz &&
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
