package unit_test

import (
	"context"
	"errors"
	"maps"
	"sync"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// instanceStub is one Gateway instance holding sockets for its users.
type instanceStub struct {
	mu        sync.Mutex
	connected map[int64]bool
	err       error
	calls     [][]int64
}

func (s *instanceStub) Connected(_ context.Context, userIDs []int64) ([]int64, error) {
	s.mu.Lock()
	s.calls = append(s.calls, userIDs)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	var found []int64
	for _, id := range userIDs {
		if s.connected[id] {
			found = append(found, id)
		}
	}
	return found, nil
}

// TestPresenceStates checks Online, Offline and Unknown across instances.
//
// Expected output: any reporting instance makes a user online; all instances
// answering without a report makes them offline; a failed instance makes an
// unreported user unknown, returned as ErrUnavailable and never as offline.
func TestPresenceStates(t *testing.T) {
	outage := errors.New("registry down")
	game := &instanceStub{connected: map[int64]bool{1: true}}
	chat := &instanceStub{connected: map[int64]bool{2: true}}

	online, err := service.NewPresence(game, chat).Online(context.Background(), []int64{3, 2, 1, 2})
	if err != nil || !maps.Equal(online, map[int64]bool{1: true, 2: true, 3: false}) {
		t.Fatalf("all answer = %v, %v", online, err)
	}
	if len(game.calls) != 1 || len(game.calls[0]) != 3 {
		t.Fatalf("game calls = %v, want one call with distinct IDs", game.calls)
	}

	down := &instanceStub{err: outage}
	online, err = service.NewPresence(game, down).Online(context.Background(), []int64{1})
	if err != nil || !online[1] {
		t.Fatalf("online despite outage = %v, %v", online, err)
	}

	online, err = service.NewPresence(game, down).Online(context.Background(), []int64{1, 3})
	if !errors.Is(err, service.ErrUnavailable) || !errors.Is(err, outage) || online != nil {
		t.Fatalf("unknown = %v, %v", online, err)
	}

	if _, err := service.NewPresence().Online(context.Background(), []int64{1}); !errors.Is(err, service.ErrUnavailable) {
		t.Fatalf("no instance = %v", err)
	}
	online, err = service.NewPresence().Online(context.Background(), nil)
	if err != nil || len(online) != 0 {
		t.Fatalf("empty = %v, %v", online, err)
	}
}

// TestPresenceChunks checks that large reads stay within the per-call limit.
//
// Expected output: 250 IDs become three calls of at most 100 IDs each.
func TestPresenceChunks(t *testing.T) {
	instance := &instanceStub{connected: map[int64]bool{250: true}}
	ids := make([]int64, 250)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	online, err := service.NewPresence(instance).Online(context.Background(), ids)
	if err != nil || len(online) != 250 || !online[250] || online[1] {
		t.Fatalf("chunked = %d entries, %v", len(online), err)
	}
	if len(instance.calls) != 3 || len(instance.calls[0]) != 100 || len(instance.calls[2]) != 50 {
		t.Fatalf("calls = %d", len(instance.calls))
	}
}

// TestNoPresence checks the stand-in used without configured registries.
func TestNoPresence(t *testing.T) {
	online, err := service.NoPresence{}.Online(context.Background(), []int64{4, 5})
	if err != nil || !maps.Equal(online, map[int64]bool{4: false, 5: false}) {
		t.Fatalf("NoPresence = %v, %v", online, err)
	}
}
