#!/bin/sh
# Creates every deployment secret that does not exist yet, under secrets/ (git-ignored).
#
# It never overwrites an existing file: replacing a credential is a deliberate
# rotation (docs/DEPLOY.md), not a side effect of `make up`.
#
# Access: secrets/ is mode 0700, so only the owner can reach any file in it. The
# files that containers read (password and credential files) are mode 0444,
# because Compose mounts them as plain bind mounts and a container's unprivileged
# user must be able to read them; the 0700 directory is what keeps other users of
# the host out. The environment files, which only Compose reads, are 0600.
#
#   secrets/db/<service>.password          one PostgreSQL password per service role
#   secrets/credentials/<caller>-to-<receiver>
#                                          one service credential per caller/receiver pair
#   secrets/env/<service>.env              DATABASE_URL (and RABBITMQ_URL for Stats)
#   secrets/jwt_secret                     Auth's signing key, mounted as JWT_SECRET_FILE
#   secrets/google_client_secret           Auth's Google client secret (empty: Google sign-in off)
#   secrets/postgres_superuser, secrets/rabbitmq_password, secrets/rabbitmq.conf
set -eu
umask 077

dir=${SECRETS_DIR:-secrets}
mkdir -p "$dir/db" "$dir/credentials" "$dir/env"
chmod 0700 "$dir"

random_hex() { openssl rand -hex 32; }
random_base64() { openssl rand -base64 32; }

create() { # create <file> <function that prints the value> [mode]
	if [ ! -s "$1" ]; then
		"$2" >"$1"
		chmod "${3:-0444}" "$1"
		echo "created $1"
	fi
}

create "$dir/postgres_superuser" random_hex
create "$dir/rabbitmq_password" random_hex 0600
# Auth reads the signing key from this file (JWT_SECRET_FILE), so a container's
# unprivileged user must be able to read it; the 0700 directory keeps the host's
# other users out. It is set on every run so a file made with 0600 by an earlier
# version of this script is fixed too.
create "$dir/jwt_secret" random_base64 0444
chmod 0444 "$dir/jwt_secret"

# Auth's Google client secret, read through GOOGLE_CLIENT_SECRET_FILE. Empty means
# Google sign-in is off. To turn it on, put the secret in this file (no newline
# needed) and set GOOGLE_CLIENT_ID in .env; Auth refuses to start with only one of
# the two.
if [ ! -e "$dir/google_client_secret" ]; then
	: >"$dir/google_client_secret"
	chmod 0444 "$dir/google_client_secret"
	echo "created $dir/google_client_secret (empty: Google sign-in is off)"
fi

for service in auth-service user-service stats-service game-session; do
	create "$dir/db/$service.password" random_hex
done

# One credential per direction: the caller holds it as its outbound credential,
# the receiver as the inbound credential for that caller. No universal secret.
for pair in \
	auth-service-to-user-service \
	auth-service-to-game-session \
	auth-service-to-game-gateway \
	auth-service-to-notification-gateway \
	user-service-to-auth-service \
	web-bff-to-auth-service \
	user-service-to-game-gateway \
	user-service-to-notification-gateway \
	user-service-to-stats-service \
	user-service-to-game-session \
	game-session-to-user-service \
	stats-service-to-auth-service \
	game-gateway-to-auth-service \
	game-gateway-to-game-session \
	notification-gateway-to-auth-service; do
	create "$dir/credentials/$pair" random_hex
done

# Environment files carry the values a service reads from its environment rather
# than from a file. They are derived from the files above and only created once.
database_url() { # database_url <role/database> <password file>
	printf 'postgres://%s:%s@postgres:5432/%s?sslmode=disable' "$1" "$(cat "$2")" "$1"
}

write_env() { # write_env <service> <database> <extra lines...>
	file="$dir/env/$1.env"
	[ -s "$file" ] && return 0
	service=$1
	database=$2
	shift 2
	{
		printf 'DATABASE_URL=%s\n' "$(database_url "$database" "$dir/db/$service.password")"
		for line in "$@"; do printf '%s\n' "$line"; done
	} >"$file"
	chmod 0600 "$file"
	echo "created $file"
}

write_env auth-service auth
# An environment file made before Auth read JWT_SECRET_FILE still carries the key
# as a plain value, and Auth refuses to start with both set. The line is dropped;
# the key itself stays in secrets/jwt_secret.
if grep -q '^JWT_SECRET=' "$dir/env/auth-service.env"; then
	grep -v '^JWT_SECRET=' "$dir/env/auth-service.env" >"$dir/env/auth-service.env.new"
	mv "$dir/env/auth-service.env.new" "$dir/env/auth-service.env"
	chmod 0600 "$dir/env/auth-service.env"
	echo "updated $dir/env/auth-service.env (JWT_SECRET is a file now)"
fi
write_env user-service users
write_env stats-service stats "RABBITMQ_URL=amqp://ft:$(cat "$dir/rabbitmq_password")@rabbitmq:5672/"
write_env game-session gamesession

# RabbitMQ 4 reads its default account from a config file (the image's *_FILE
# variables are deprecated). The file is only mounted read-only into the broker.
if [ ! -s "$dir/rabbitmq.conf" ]; then
	{
		printf 'default_user = ft\n'
		printf 'default_pass = %s\n' "$(cat "$dir/rabbitmq_password")"
	} >"$dir/rabbitmq.conf"
	chmod 0444 "$dir/rabbitmq.conf"
	echo "created $dir/rabbitmq.conf"
fi
