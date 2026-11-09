package postgres

import (
	"errors"

	"game-session/game"
	"game-session/room"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SELECT * FROM games WHERE room_id = 1 ORDER BY game_id DESC LIMIT 1;
// SELECT * FROM game_cards WHERE game_id = 7 ORDER BY card_id;
// SELECT * FROM game_history WHERE game_id = 7 ORDER BY seq;
func (s *Store) LoadCurrentGame(roomID int64) (*game.Game, error) {
	var row gameRow
	err := s.db.Where("room_id = ?", roomID).Order("game_id DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cards, history, err := s.gameDetails(row.GameID)
	if err != nil {
		return nil, err
	}
	return game.FromRecord(fromGameRows(row, cards, history))
}

func (s *Store) gameDetails(gameID int64) ([]gameCardRow, []gameHistoryRow, error) {
	var cards []gameCardRow
	if err := s.db.Where("game_id = ?", gameID).Order("card_id").Find(&cards).Error; err != nil {
		return nil, nil, err
	}
	var history []gameHistoryRow
	if err := s.db.Where("game_id = ?", gameID).Order("seq").Find(&history).Error; err != nil {
		return nil, nil, err
	}
	return cards, history, nil
}

// SELECT last_game_id FROM rooms WHERE room_id = 1;
// SELECT * FROM games WHERE game_id = 7;
// SELECT * FROM game_players WHERE game_id = 7 ORDER BY user_id;
func (s *Store) loadLastGameRows(roomID int64) (*gameRow, []gamePlayerRow, error) {
	var lastGameID *int64
	if err := s.db.Table("rooms").Select("last_game_id").Where("room_id = ?", roomID).Scan(&lastGameID).Error; err != nil {
		return nil, nil, err
	}
	if lastGameID == nil {
		return nil, nil, nil
	}
	var row gameRow
	if err := s.db.First(&row, *lastGameID).Error; err != nil {
		return nil, nil, notFound(err, room.ErrGameNotFound)
	}
	if row.Winner == nil || row.EndReason == nil || row.FinishedAt == nil {
		return nil, nil, game.ErrInvalidGameState
	}
	var players []gamePlayerRow
	if err := s.db.Where("game_id = ?", row.GameID).Order("user_id").Find(&players).Error; err != nil {
		return nil, nil, err
	}

	return &row, players, nil
}

func (s *Store) LoadLastGameResult(roomID int64) (*room.LastGameView, error) {
	row, players, err := s.loadLastGameRows(roomID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	cards, history, err := s.gameDetails(row.GameID)
	if err != nil {
		return nil, err
	}
	g, err := game.FromRecord(fromGameRows(*row, cards, history))
	if err != nil {
		return nil, err
	}

	loser := game.TeamRed
	if *row.Winner == game.TeamRed {
		loser = game.TeamBlue
	}
	view := &room.LastGameView{
		GameID:     row.GameID,
		Status:     game.GameStatusFinished,
		Winner:     *row.Winner,
		Loser:      loser,
		EndReason:  *row.EndReason,
		Score:      g.Score(),
		FinishedAt: *row.FinishedAt,
		RoomID:     roomID,
		StartedAt:  row.StartedAt,
		Players:    make([]room.FinalizedPlayer, len(players)),
	}
	for i, p := range players {
		view.Players[i] = room.FinalizedPlayer{UserID: p.UserID, Team: p.Team, Role: p.Role}
	}
	return view, nil
}

// SELECT nextval(pg_get_serial_sequence('games', 'game_id'));
func (s *Store) NextGameID() (int64, error) {
	var id int64
	err := s.db.Raw("SELECT nextval(pg_get_serial_sequence('games', 'game_id'))").Scan(&id).Error
	return id, err
}

// INSERT INTO games (game_id, room_id, starting_team, phase, current_team, staffing_timeout_seconds, started_at) VALUES (7, 1, 'RED', 'WAITING_FOR_CLUE', 'RED', 120, now());
// INSERT INTO game_cards (game_id, card_id, word, color) VALUES (7, 1, 'AFRICA', 'RED'), (7, 2, 'AGENT', 'BLUE');
// INSERT INTO game_history (game_id, seq, type, at) VALUES (7, 1, 'GAME_STARTED', now());
func insertGame(tx *gorm.DB, roomID int64, g *game.Game) error {
	rec := g.ToRecord()
	row := toGameRow(roomID, rec)
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	if err := tx.CreateInBatches(toCardRows(rec.GameID, rec.Cards), 25).Error; err != nil {
		return err
	}
	return insertHistory(tx, rec)
}

// INSERT INTO game_history (game_id, seq, type, at, team, user_id, clue_word, clue_number) VALUES (7, 2, 'CLUE_GIVEN', now(), 'RED', 42, 'hint', 2) ON CONFLICT (game_id, seq) DO NOTHING;
func insertHistory(tx *gorm.DB, rec game.GameRecord) error {
	rows := toHistoryRows(rec.GameID, rec.History)
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 50).Error
}

// UPDATE games SET phase = 'GUESSING', current_team = 'RED', clue_word = 'hint', clue_number = 2, guesses_remaining = 3, turn_deadline_at = now() + interval '60 seconds' WHERE game_id = 7;
// UPDATE game_cards SET revealed = true WHERE game_id = 7 AND card_id IN (3, 11);
// INSERT INTO game_history (game_id, seq, type, at, team, card_id, word, color) VALUES (7, 3, 'CARD_REVEALED', now(), 'RED', 3, 'AFRICA', 'RED') ON CONFLICT (game_id, seq) DO NOTHING;
func updateGame(tx *gorm.DB, g *game.Game) error {
	rec := g.ToRecord()
	row := toGameRow(0, rec)
	if err := tx.Model(&gameRow{GameID: rec.GameID}).Select(gameUpdateColumns).Updates(&row).Error; err != nil {
		return err
	}
	revealed := make([]int, 0, len(rec.Cards))
	for _, c := range rec.Cards {
		if c.Revealed {
			revealed = append(revealed, c.CardID)
		}
	}
	if len(revealed) > 0 {
		if err := tx.Model(&gameCardRow{}).Where("game_id = ? AND card_id IN ?", rec.GameID, revealed).Update("revealed", true).Error; err != nil {
			return err
		}
	}
	return insertHistory(tx, rec)
}

// UPDATE room_members SET state = 'IN_GAME', ready = true WHERE user_id = 42;
func saveMembers(tx *gorm.DB, members []room.RoomMember) error {
	for i := range members {
		if err := tx.Save(&members[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveGame(g *game.Game) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		return updateGame(tx, g)
	})
}

// UPDATE rooms SET status = 'IN_GAME', countdown_ends_at = NULL WHERE room_id = 1;
// UPDATE room_members SET state = 'IN_GAME', ready = true WHERE user_id = 42;
// INSERT INTO games (game_id, room_id, starting_team, phase, current_team, staffing_timeout_seconds, started_at) VALUES (7, 1, 'RED', 'WAITING_FOR_CLUE', 'RED', 120, now());
// INSERT INTO game_cards (game_id, card_id, word, color) VALUES (7, 1, 'AFRICA', 'RED');
// INSERT INTO game_history (game_id, seq, type, at) VALUES (7, 1, 'GAME_STARTED', now());
func (s *Store) SaveGameStart(r *room.Room, members []room.RoomMember, g *game.Game) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(r).Error; err != nil {
			return err
		}
		if err := saveMembers(tx, members); err != nil {
			return err
		}
		return insertGame(tx, r.RoomID, g)
	})
}

// UPDATE rooms SET status = 'POST_GAME', post_game_ends_at = now() + interval '60 seconds' WHERE room_id = 1;
// UPDATE room_members SET state = 'POST_GAME', ready = false WHERE user_id = 42;
// UPDATE games SET phase = 'GAME_OVER', winner = 'RED', end_reason = 'ALL_TEAM_CARDS_REVEALED', finished_at = now() WHERE game_id = 7;
// DELETE FROM room_members WHERE room_id = 1;
// UPDATE rooms SET last_game_id = 7 WHERE room_id = 1;
// INSERT INTO game_players (game_id, user_id, team, role) VALUES (7, 42, 'RED', 'SPYMASTER'), (7, 43, 'RED', 'OPERATIVE');
// INSERT INTO outbox_events (room_id, event_type, outbox_content, room_version) VALUES (1, 'match.finalized', '{"game_id": 7, "winner": "RED"}', 12);
func (s *Store) SaveGameEnd(r *room.Room, members []room.RoomMember, g *game.Game, lastGame *room.LastGameView) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(r).Error; err != nil {
			return err
		}
		if err := saveMembers(tx, members); err != nil {
			return err
		}
		if g != nil {
			if err := updateGame(tx, g); err != nil {
				return err
			}
		}
		if r.Status == room.StatusClosed {
			if err := tx.Where("room_id = ?", r.RoomID).Delete(&room.RoomMember{}).Error; err != nil {
				return err
			}
		}
		if lastGame == nil {
			if g == nil {
				return nil
			}
			return insertOutbox(tx, r, "game.cancelled", map[string]any{"room_id": r.RoomID, "game_id": g.GameID()})
		}
		if err := tx.Table("rooms").Where("room_id = ?", r.RoomID).Update("last_game_id", lastGame.GameID).Error; err != nil {
			return err
		}
		players := make([]gamePlayerRow, len(lastGame.Players))
		for i, p := range lastGame.Players {
			players[i] = gamePlayerRow{GameID: lastGame.GameID, UserID: p.UserID, Team: p.Team, Role: p.Role}
		}
		if len(players) > 0 {
			if err := tx.CreateInBatches(players, 20).Error; err != nil {
				return err
			}
		}
		return insertOutbox(tx, r, "match.finalized", toMatchFinalizedEvent(lastGame))
	})
}

// UPDATE rooms SET countdown_ends_at = NULL WHERE room_id = 1;
// UPDATE room_members SET team = 'RED', role = 'SPYMASTER', ready = false WHERE user_id = 43;
// UPDATE games SET phase = 'WAITING_FOR_CLUE', phase_before_pause = NULL, red_missing_roles = '{}' WHERE game_id = 7;
func (s *Store) SaveSeatInGame(r *room.Room, member room.RoomMember, g *game.Game) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(r).Error; err != nil {
			return err
		}
		if err := tx.Save(&member).Error; err != nil {
			return err
		}
		return updateGame(tx, g)
	})
	if isUniqueViolation(err, constraintOneSpymaster) {
		return room.ErrRoleConflict
	}
	return err
}

// UPDATE rooms SET host_user_id = 44, countdown_ends_at = NULL WHERE room_id = 1;
// DELETE FROM room_members WHERE room_id = 1 AND user_id = 43;
// UPDATE games SET phase = 'PAUSED_FOR_PLAYERS', phase_before_pause = 'GUESSING', red_missing_roles = '{SPYMASTER}' WHERE game_id = 7;
// INSERT INTO outbox_events (room_id, event_type, outbox_content, room_version) VALUES (1, 'player.penalized', '{"user_id": 43, "role": "SPYMASTER"}', 12);
func (s *Store) SaveLeave(r *room.Room, departedUserID int64, g *game.Game, penalty *room.Penalty) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(r).Error; err != nil {
			return err
		}
		if err := tx.Where("room_id = ? AND user_id = ?", r.RoomID, departedUserID).Delete(&room.RoomMember{}).Error; err != nil {
			return err
		}
		if g != nil {
			if err := updateGame(tx, g); err != nil {
				return err
			}
		}
		if penalty != nil {
			return insertOutbox(tx, r, "player.penalized", penalty)
		}
		return nil
	})
}
