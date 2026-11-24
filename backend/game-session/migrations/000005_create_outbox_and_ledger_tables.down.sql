BEGIN;

DROP TABLE session_departures;
DROP TABLE account_cleanups;
DROP TABLE command_ledger;
DROP TABLE outbox_events;

COMMIT;
