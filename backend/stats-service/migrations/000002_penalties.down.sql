DROP TABLE stats.penalties;

ALTER TABLE stats.user_stats
    DROP CONSTRAINT user_stats_penalty_valid,
    DROP COLUMN penalty_points;
