package integration_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/service"
	"github.com/melmut-42/ft_transcendence/backend/auth-service/internal/testutil"
)

const concurrentRegistrations = 8

// registerConcurrently runs one registration per input and returns each error.
func registerConcurrently(auth *service.AuthService, inputs []service.RegisterInput) []error {
	results := make([]error, len(inputs))
	var group sync.WaitGroup
	start := make(chan struct{})
	for i, input := range inputs {
		group.Go(func() {
			<-start
			_, results[i] = auth.Register(context.Background(), input)
		})
	}
	close(start)
	group.Wait()

	return results
}

func countRegistrationRows(t *testing.T, pool *pgxpool.Pool) (users, operations, sessions int) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM users),
		(SELECT count(*) FROM provisioning_operations), (SELECT count(*) FROM sessions)`).
		Scan(&users, &operations, &sessions)
	if err != nil {
		t.Fatal(err)
	}

	return users, operations, sessions
}

// TestConcurrentUsernameConflictPostgres races registrations for one name in
// different letter case.
//
// Expected output:
//
//   - One registration succeeds and every other one answers ErrUsernameTaken.
//   - Losing accounts leave no row, operation or session behind.
//   - A losing email can register again with a free name.
func TestConcurrentUsernameConflictPostgres(t *testing.T) {
	pool := registrationTestPool(t)
	stub := newUserStub(t)
	stub.names = map[string]int64{}
	auth := provisioningAuth(t, pool, stub)

	inputs := make([]service.RegisterInput, concurrentRegistrations)
	for i := range inputs {
		inputs[i] = testutil.ValidInput()
		inputs[i].Email = fmt.Sprintf("player%d@example.com", i)
		if i%2 == 1 {
			inputs[i].Username = strings.ToUpper(inputs[i].Username)
		}
	}
	loser, won := -1, 0
	for i, err := range registerConcurrently(auth, inputs) {
		switch {
		case err == nil:
			won++
		case errors.Is(err, service.ErrUsernameTaken):
			loser = i
		default:
			t.Fatalf("registration %d = %v", i, err)
		}
	}
	if won != 1 || loser < 0 {
		t.Fatalf("won = %d, loser = %d", won, loser)
	}
	if users, operations, sessions := countRegistrationRows(t, pool); users != 1 || operations != 1 || sessions != 1 {
		t.Fatalf("rows = %d users, %d operations, %d sessions", users, operations, sessions)
	}

	retry := inputs[loser]
	retry.Username = "free_name"
	if _, err := auth.Register(context.Background(), retry); err != nil {
		t.Fatalf("retry with a free name = %v", err)
	}
}

// TestConcurrentEmailConflictPostgres races identical registrations whose
// email differs only in letter case.
//
// Expected output:
//
//   - Every answer is a success or ErrEmailTaken, and at least one succeeds.
//   - One account and one provisioning operation exist, sent under one id.
func TestConcurrentEmailConflictPostgres(t *testing.T) {
	pool := registrationTestPool(t)
	stub := newUserStub(t)
	auth := provisioningAuth(t, pool, stub)

	inputs := make([]service.RegisterInput, concurrentRegistrations)
	for i := range inputs {
		inputs[i] = testutil.ValidInput()
		if i%2 == 1 {
			inputs[i].Email = strings.ToUpper(inputs[i].Email)
		}
	}
	won := 0
	for i, err := range registerConcurrently(auth, inputs) {
		if err == nil {
			won++
		} else if !errors.Is(err, service.ErrEmailTaken) {
			t.Fatalf("registration %d = %v", i, err)
		}
	}
	users, operations, _ := countRegistrationRows(t, pool)
	if won == 0 || users != 1 || operations != 1 {
		t.Fatalf("won = %d, users = %d, operations = %d", won, users, operations)
	}
	state, _ := readAccount(t, pool)
	if state.status != "ACTIVE" || state.operationState != "COMPLETED" {
		t.Fatalf("state = %+v", state)
	}
	for _, sent := range stub.sent() {
		if sent != state.operationID {
			t.Fatalf("sent operation %s, want only %s", sent, state.operationID)
		}
	}
}
