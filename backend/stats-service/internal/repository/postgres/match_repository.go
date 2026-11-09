package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/internal/service"
)

var _ service.MatchStore = (*MatchRepository)(nil)

// MatchRepository stores finalized matches and their counters.
type MatchRepository struct {
	pool *pgxpool.Pool
}

// NewMatches returns a match store; pool ownership stays with the caller.
func NewMatches(pool *pgxpool.Pool) *MatchRepository {
	return &MatchRepository{pool: pool}
}

// RecordFinalizedMatch implements service.MatchStore.
//
// The primary key on game_id decides the winner of a race: a second delivery
// waits for the first to commit and then inserts nothing. Counters are updated
// in user_id order, so two matches sharing players cannot deadlock.
func (r *MatchRepository) RecordFinalizedMatch(ctx context.Context, match service.MatchFinalized) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, classify(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	recorded, err := applyMatch(ctx, tx, match)
	if err != nil {
		return false, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, classify(err)
	}

	return recorded, nil
}

func applyMatch(ctx context.Context, tx pgx.Tx, match service.MatchFinalized) (bool, error) {
	inserted, err := tx.Exec(ctx, `
		INSERT INTO matches (game_id, room_id, winner, loser, end_reason, score_red, score_blue, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (game_id) DO NOTHING`,
		match.GameID, match.RoomID, match.Winner, match.Loser, match.EndReason,
		match.Score.Red, match.Score.Blue, match.StartedAt, match.FinishedAt)
	if err != nil {
		return false, err
	}
	if inserted.RowsAffected() == 0 {
		return false, nil
	}

	players := slices.Clone(match.Players)
	slices.SortFunc(players, func(a, b service.FinalizedPlayer) int { return cmp.Compare(a.UserID, b.UserID) })
	for _, player := range players {
		if err := lockUser(ctx, tx, player.UserID); err != nil {
			return false, err
		}
		deleted, err := isDeleted(ctx, tx, player.UserID)
		if err != nil {
			return false, err
		}
		if deleted {
			if _, err := tx.Exec(ctx, `INSERT INTO match_participants (game_id, user_id, username, team, role)
				VALUES ($1, NULL, $2, $3, $4)`, match.GameID, deletedUsername, player.Team, player.Role); err != nil {
				return false, err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO match_participants (game_id, user_id, username, team, role)
			VALUES ($1, $2, $3, $4, $5)`, match.GameID, player.UserID, player.Username, player.Team, player.Role); err != nil {
			return false, err
		}
		wins, losses := 0, 1
		if player.Team == match.Winner {
			wins, losses = 1, 0
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_stats (user_id, wins, losses) VALUES ($1, $2, $3)
			ON CONFLICT (user_id) DO UPDATE
			SET wins = user_stats.wins + EXCLUDED.wins, losses = user_stats.losses + EXCLUDED.losses, updated_at = now()`,
			player.UserID, wins, losses); err != nil {
			return false, err
		}
	}

	return true, nil
}

// classify marks connection failures as service.ErrUnavailable.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case len(pgErr.Code) == 5 && pgErr.Code[:2] == "08",
			pgErr.Code == "53300", pgErr.Code == "57P01", pgErr.Code == "57P02", pgErr.Code == "57P03":
			return fmt.Errorf("%w: %w", service.ErrUnavailable, err)
		}
		return err
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	if errors.As(err, &connectErr) || errors.As(err, &netErr) || pgconn.SafeToRetry(err) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", service.ErrUnavailable, err)
	}

	return err
}
