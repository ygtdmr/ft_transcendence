package postgres

import (
	"errors"

	"game-session/service"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db      *gorm.DB
	locking bool
}

func (s *Store) Transaction(fn func(tx service.RoomStore) error) error {
	if s.locking {
		return fn(s)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		return fn(&Store{db: tx, locking: true})
	})
}

func (s *Store) roomQuery() *gorm.DB {
	if s.locking {
		return s.db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return s.db
}

func Open(dsn string) (*Store, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func notFound(err error, domainErr error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainErr
	}
	return err
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	found := errors.As(err, &pgErr)
	if !found {
		return false
	}
	if pgErr.Code != "23505" {
		return false
	}
	return pgErr.ConstraintName == constraint
}

const (
	constraintActiveRoomCode = "idx_rooms_active_room_code"
	constraintOneActiveRoom  = "room_members_pkey"
	constraintOneSpymaster   = "idx_room_members_spymaster"
)

var _ service.RoomStore = (*Store)(nil)

func (s *Store) ResetForTests() error {
	for _, statement := range []string{
		"TRUNCATE rooms RESTART IDENTITY CASCADE",
		"TRUNCATE outbox_events",
		"TRUNCATE command_ledger",
	} {
		if err := s.db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
