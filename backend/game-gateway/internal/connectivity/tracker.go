// Package connectivity counts each member's room sockets on this gateway and
// orders that member's connectivity reports to Game Session.
//
// Game Session keeps one connected/disconnected state per member. The gateway
// reports "socket opened" during each handshake and "last socket closed" when
// the member's last socket for the room ends (multi-tab: earlier closes are
// not reported). Reports of one member are serialized and decided under that
// member's lock, so a late "last socket closed" can never overtake a newer
// "socket opened" and leave a connected member marked disconnected. Different
// members never wait for each other.
package connectivity

import "sync"

type key struct{ room, user int64 }

type entry struct {
	mu    sync.Mutex
	count int // sockets counted: open, or past a successful report in a handshake
	refs  int // callers holding the entry; guarded by Tracker.mu
}

// Tracker is safe for concurrent use. The zero value is ready to use.
type Tracker struct {
	mu      sync.Mutex
	entries map[key]*entry
}

// Open runs report (socket opened) under the member's lock and, if it
// succeeds, counts one more socket for the member. A failed report counts
// nothing and is returned.
func (t *Tracker) Open(room, user int64, report func() error) error {
	k := key{room, user}
	e := t.acquire(k)
	defer t.release(k, e)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := report(); err != nil {
		return err
	}
	e.count++

	return nil
}

// Close uncounts one socket of the member, counted by a successful Open, and
// runs report (last socket closed) under the member's lock when it was the
// last one.
func (t *Tracker) Close(room, user int64, report func()) {
	k := key{room, user}
	e := t.acquire(k)
	defer t.release(k, e)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.count == 0 {
		return
	}
	e.count--
	if e.count == 0 {
		report()
	}
}

// Count returns how many sockets are counted for the member.
func (t *Tracker) Count(room, user int64) int {
	k := key{room, user}
	e := t.acquire(k)
	defer t.release(k, e)
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.count
}

func (t *Tracker) acquire(k key) *entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = map[key]*entry{}
	}
	e := t.entries[k]
	if e == nil {
		e = &entry{}
		t.entries[k] = e
	}
	e.refs++

	return e
}

// release drops the entry once nobody holds it and it counts no socket. With
// refs at zero no caller can be changing count, and every earlier change
// happened before this caller acquired Tracker.mu.
func (t *Tracker) release(k key, e *entry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e.refs--
	if e.refs == 0 && e.count == 0 {
		delete(t.entries, k)
	}
}

// Members returns how many members have an entry, for tests.
func (t *Tracker) Members() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return len(t.entries)
}
