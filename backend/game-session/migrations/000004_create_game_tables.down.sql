BEGIN;

ALTER TABLE rooms DROP COLUMN last_game_id;

DROP TABLE IF EXISTS game_players;
DROP TABLE game_history;
DROP TABLE game_cards;
DROP TABLE games;

COMMIT;
