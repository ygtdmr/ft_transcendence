package postgres

import (
	"time"

	"game-session/worker"
)

// SELECT room_id FROM rooms WHERE status = 'COUNTDOWN' AND countdown_ends_at <= now();
// SELECT room_id FROM rooms WHERE status <> 'CLOSED' AND post_game_ends_at <= now();
// SELECT room_id FROM rooms WHERE status <> 'CLOSED' AND close_at <= now();
// SELECT room_id, user_id FROM room_members WHERE grace_ends_at <= now();
// SELECT g.room_id FROM games g JOIN rooms r USING (room_id) WHERE r.status = 'IN_GAME' AND g.finished_at IS NULL AND g.phase IN ('WAITING_FOR_CLUE', 'GUESSING') AND g.turn_deadline_at <= now();
// SELECT g.room_id FROM games g JOIN rooms r USING (room_id) WHERE r.status = 'IN_GAME' AND g.finished_at IS NULL AND g.phase = 'PAUSED_FOR_PLAYERS' AND (g.red_staffing_deadline_at <= now() OR g.blue_staffing_deadline_at <= now());
func (s *Store) DueDeadlines(now time.Time) (worker.Due, error) {
	var due worker.Due
	queries := []struct {
		dest any
		sql  string
	}{
		{&due.CountdownRooms, "SELECT room_id FROM rooms WHERE status = 'COUNTDOWN' AND countdown_ends_at <= ?"},
		{&due.PostGameRooms, "SELECT room_id FROM rooms WHERE status <> 'CLOSED' AND post_game_ends_at <= ?"},
		{&due.CloseRooms, "SELECT room_id FROM rooms WHERE status <> 'CLOSED' AND close_at <= ?"},
		{&due.GraceMembers, "SELECT room_id, user_id FROM room_members WHERE grace_ends_at <= ?"},
		{&due.TurnRooms, "SELECT g.room_id FROM games g JOIN rooms r USING (room_id) WHERE r.status = 'IN_GAME' AND g.finished_at IS NULL AND g.phase IN ('WAITING_FOR_CLUE', 'GUESSING') AND g.turn_deadline_at <= ?"},
		{&due.StaffingRooms, "SELECT g.room_id FROM games g JOIN rooms r USING (room_id) WHERE r.status = 'IN_GAME' AND g.finished_at IS NULL AND g.phase = 'PAUSED_FOR_PLAYERS' AND (g.red_staffing_deadline_at <= ? OR g.blue_staffing_deadline_at <= ?)"},
	}
	for _, q := range queries {
		args := []any{now}
		if q.dest == &due.StaffingRooms {
			args = []any{now, now}
		}
		if err := s.db.Raw(q.sql, args...).Scan(q.dest).Error; err != nil {
			return worker.Due{}, err
		}
	}
	return due, nil
}

var _ worker.DeadlineStore = (*Store)(nil)
