package service

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// MaxPresenceUsers is the most user IDs one presence read accepts.
const MaxPresenceUsers = 100

// PresenceReader answers which users have an open authenticated connection.
type PresenceReader interface {
	// Online returns, for every distinct ID in userIDs, whether the user is
	// online. It returns an error wrapping ErrUnavailable when any of them is
	// unknown, and never reports an unknown user as offline.
	Online(ctx context.Context, userIDs []int64) (map[int64]bool, error)
}

// NoPresence reports every user as offline. It serves only deployments outside
// production that have no Gateway registry configured.
type NoPresence struct{}

// Online implements PresenceReader.
func (NoPresence) Online(_ context.Context, userIDs []int64) (map[int64]bool, error) {
	online := make(map[int64]bool, len(userIDs))
	for _, id := range userIDs {
		online[id] = false
	}

	return online, nil
}

// ConnectionReader reads one Gateway instance's active socket registry.
type ConnectionReader interface {
	// Connected returns which of userIDs hold an open authenticated socket on
	// the instance. An error means nothing is known about any of them.
	Connected(ctx context.Context, userIDs []int64) ([]int64, error)
}

// Presence derives presence from every running Gateway instance. It stores no
// presence state.
type Presence struct {
	instances []ConnectionReader
}

// NewPresence returns presence over instances, which must list every running
// Game Gateway and Chat Gateway instance.
func NewPresence(instances ...ConnectionReader) *Presence {
	return &Presence{instances: instances}
}

// Online implements PresenceReader. It queries all instances concurrently, at
// most MaxPresenceUsers IDs per call. A user is online when any instance
// reports them, offline when every instance answered without reporting them,
// and unknown otherwise.
//
// Errors:
//
//   - ErrUnavailable: a user is unknown, or no instance is configured.
func (p *Presence) Online(ctx context.Context, userIDs []int64) (map[int64]bool, error) {
	ids := slices.Clone(userIDs)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	online := make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return online, nil
	}
	if len(p.instances) == 0 {
		return nil, fmt.Errorf("%w: no gateway registry is configured", ErrUnavailable)
	}

	for chunk := range slices.Chunk(ids, MaxPresenceUsers) {
		if err := p.readChunk(ctx, chunk, online); err != nil {
			return nil, err
		}
	}

	return online, nil
}

func (p *Presence) readChunk(ctx context.Context, ids []int64, online map[int64]bool) error {
	type answer struct {
		connected []int64
		err       error
	}
	answers := make([]answer, len(p.instances))
	var wait sync.WaitGroup
	for i, instance := range p.instances {
		wait.Go(func() {
			connected, err := instance.Connected(ctx, ids)
			answers[i] = answer{connected: connected, err: err}
		})
	}
	wait.Wait()

	var failure error
	for _, id := range ids {
		online[id] = false
	}
	for _, a := range answers {
		if a.err != nil {
			if failure == nil {
				failure = a.err
			}
			continue
		}
		for _, id := range a.connected {
			online[id] = true
		}
	}
	if failure == nil {
		return nil
	}
	for _, id := range ids {
		if !online[id] {
			return fmt.Errorf("%w: presence of user %d is unknown: %w", ErrUnavailable, id, failure)
		}
	}

	return nil
}
