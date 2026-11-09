package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

var _ service.StatsStore = (*StatsReadsRepository)(nil)

// StatsReadsRepository reads counters and match history.
type StatsReadsRepository struct {
	pool *pgxpool.Pool
}

// NewStatsReads returns a read store; pool ownership stays with the caller.
func NewStatsReads(pool *pgxpool.Pool) *StatsReadsRepository {
	return &StatsReadsRepository{pool: pool}
}

// Counters implements service.StatsStore.
func (r *StatsReadsRepository) Counters(ctx context.Context, userID int64) (int, int, int, error) {
	var wins, losses, penalties int
	err := r.pool.QueryRow(ctx, `SELECT wins, losses, penalty_points FROM user_stats WHERE user_id = $1`, userID).
		Scan(&wins, &losses, &penalties)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, 0, nil
	}
	if err != nil {
		return 0, 0, 0, classify(err)
	}

	return wins, losses, penalties, nil
}

// MatchHistory implements service.StatsStore.
func (r *StatsReadsRepository) MatchHistory(ctx context.Context, userID int64, limit int, before *time.Time) ([]service.MatchSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT m.game_id, m.room_id, p.team, p.role, m.winner, m.end_reason, m.score_red, m.score_blue, m.finished_at,
		       o.ids, o.names
		FROM match_participants p
		JOIN matches m ON m.game_id = p.game_id
		CROSS JOIN LATERAL (
			SELECT array_agg(q.user_id ORDER BY q.user_id) AS ids, array_agg(q.username ORDER BY q.user_id) AS names
			FROM match_participants q WHERE q.game_id = p.game_id AND q.team <> p.team
		) o
		WHERE p.user_id = $1 AND ($2::timestamptz IS NULL OR m.finished_at < $2)
		ORDER BY m.finished_at DESC, m.game_id DESC
		LIMIT $3`, userID, before, limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()

	matches := []service.MatchSummary{}
	for rows.Next() {
		match, err := scanSummary(rows)
		if err != nil {
			return nil, classify(err)
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}

	return matches, nil
}

func scanSummary(rows pgx.Rows) (service.MatchSummary, error) {
	var match service.MatchSummary
	var winner string
	var ids []*int64
	var names []string
	if err := rows.Scan(&match.GameID, &match.RoomID, &match.Team, &match.Role, &winner, &match.EndReason,
		&match.ScoreRed, &match.ScoreBlue, &match.FinishedAt, &ids, &names); err != nil {
		return service.MatchSummary{}, err
	}
	match.Result = "LOSS"
	if match.Team == winner {
		match.Result = "WIN"
	}
	match.Opponents = make([]service.Opponent, len(ids))
	for i := range ids {
		match.Opponents[i] = service.Opponent{UserID: ids[i], Username: names[i]}
	}

	return match, nil
}
