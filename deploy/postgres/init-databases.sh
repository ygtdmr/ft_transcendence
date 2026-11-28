#!/bin/sh
# Runs once, when the data directory is first created (docker-entrypoint-initdb.d).
#
# Every service gets its own database and its own login role. A role owns only
# its database and is refused on every other one, so no service can read or write
# another service's data and no foreign key can span services.
set -eu

create_database() { # create_database <role and database name> <password file>
	role=$1
	password=$(cat "$2")
	psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<SQL
CREATE ROLE "$role" LOGIN PASSWORD '$password'
    NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE DATABASE "$role" OWNER "$role";
REVOKE ALL ON DATABASE "$role" FROM PUBLIC;
SQL
}

create_database auth        /run/secrets/db_auth_service
create_database users       /run/secrets/db_user_service
create_database stats       /run/secrets/db_stats_service
create_database gamesession /run/secrets/db_game_session

# The maintenance databases are not for services either.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<'SQL'
REVOKE ALL ON DATABASE postgres FROM PUBLIC;
REVOKE ALL ON DATABASE template1 FROM PUBLIC;
SQL
