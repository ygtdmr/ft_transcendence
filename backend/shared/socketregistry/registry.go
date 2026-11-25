// Package socketregistry keeps one gateway instance's live WebSocket
// connections in memory, indexed by user and by session, as the contract's
// "active socket registry" (workspace.yml, State Ownership) describes.
//
// It holds no domain data and makes no authorization decision. A gateway adds
// a socket after its handshake succeeded, delivers messages to every open
// socket of a user (multi-tab), closes every socket of one session when Auth
// reports a revocation, answers which users hold an open socket (presence),
// and closes everything at shutdown.
//
// The registry is transport-neutral: it never touches a network connection.
// Each Socket exposes a bounded send queue and a close signal; the gateway's
// own writer goroutine drains the queue and writes the close frame. A queue that
// is full is never waited on: that socket is closed as a slow client, so one
// stalled browser cannot block delivery to anybody else.
//
// The registry is per process. A restart drops it, and clients reconnect and
// authenticate again; nothing here survives or replays.
package socketregistry

import (
	"errors"
	"slices"
	"sync"
	"time"
)

var (
	// ErrClosed reports an Add after CloseAll: the instance is shutting down.
	ErrClosed = errors.New("socketregistry: registry is shut down")
	// ErrRevoked reports an Add for a session that was revoked recently, which
	// closes the race between a handshake that validated the session and a
	// revocation that arrived before the socket was registered.
	ErrRevoked = errors.New("socketregistry: session was revoked")
	// ErrInvalidSocket reports an Add without a positive user id or a session id.
	ErrInvalidSocket = errors.New("socketregistry: user id and session id are required")
)

// WebSocket close codes the registry uses by default (RFC 6455 and the IANA
// registry).
const (
	CloseGoingAway     = 1001
	CloseTryAgainLater = 1013
)

// maxReasonBytes is the longest close reason a close frame can carry
// (125-byte control payload minus the 2-byte code).
const maxReasonBytes = 123

// Closure says how a socket is closed.
type Closure struct {
	// Code is the WebSocket close code, such as 4401 for a revoked session.
	Code int
	// Reason is the close reason text; it is cut to 123 bytes.
	Reason string
	// Final, when set, is one last text message written before the close
	// frame, such as an UNAUTHORIZED error event.
	Final []byte
}

// Options configures a Registry. Zero values select the defaults.
type Options struct {
	// QueueSize bounds each socket's pending messages. Default 64.
	QueueSize int
	// SlowClient closes a socket whose queue is full. Default code 1013,
	// reason "SLOW_CONSUMER".
	SlowClient Closure
	// RevokedTTL is how long a revoked session stays refused by Add.
	// Default 15 minutes, longer than any handshake can take.
	RevokedTTL time.Duration
	// Now returns the current time; tests replace it. Default time.Now.
	Now func() time.Time
}

// Registry is safe for concurrent use. The zero value is not usable; call New.
type Registry struct {
	opts Options

	mu          sync.Mutex
	byUser      map[int64]map[*Socket]struct{}
	bySession   map[string]map[*Socket]struct{}
	byScopeUser map[scopeUser]map[*Socket]struct{}
	revoked     map[string]time.Time
	closed      bool
}

// scopeUser keys a user's sockets within one scope, such as one room.
type scopeUser struct {
	scope  string
	userID int64
}

// New returns an empty registry.
func New(opts Options) *Registry {
	if opts.QueueSize <= 0 {
		opts.QueueSize = 64
	}
	if opts.SlowClient.Code == 0 {
		opts.SlowClient = Closure{Code: CloseTryAgainLater, Reason: "SLOW_CONSUMER"}
	}
	if opts.RevokedTTL <= 0 {
		opts.RevokedTTL = 15 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	return &Registry{
		opts:        opts,
		byUser:      map[int64]map[*Socket]struct{}{},
		bySession:   map[string]map[*Socket]struct{}{},
		byScopeUser: map[scopeUser]map[*Socket]struct{}{},
		revoked:     map[string]time.Time{},
	}
}

// Add registers an open socket of userID's sessionID without a scope, as a
// gateway with one socket kind (notifications) does.
//
// Errors: ErrInvalidSocket, ErrClosed after CloseAll, ErrRevoked for a session
// closed by CloseSession within RevokedTTL. The caller then closes the
// connection itself (for a revoked session with 4401).
func (r *Registry) Add(userID int64, sessionID string) (*Socket, error) {
	return r.AddIn("", userID, sessionID)
}

// AddIn registers an open socket of userID's sessionID within scope, such as
// one room ("1001"), so SendToUserIn and CloseUserIn reach only that user's
// sockets of that scope. Errors are those of Add.
func (r *Registry) AddIn(scope string, userID int64, sessionID string) (*Socket, error) {
	if userID <= 0 || sessionID == "" {
		return nil, ErrInvalidSocket
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	r.pruneRevokedLocked()
	if _, revoked := r.revoked[sessionID]; revoked {
		return nil, ErrRevoked
	}
	s := &Socket{
		scope:     scope,
		userID:    userID,
		sessionID: sessionID,
		send:      make(chan []byte, r.opts.QueueSize),
		closing:   make(chan struct{}),
		slow:      r.opts.SlowClient,
	}
	insert(r.byUser, userID, s)
	insert(r.bySession, sessionID, s)
	insert(r.byScopeUser, scopeUser{scope, userID}, s)

	return s, nil
}

// Remove forgets s. The gateway calls it once the connection has ended. It is
// safe to call more than once.
func (r *Registry) Remove(s *Socket) {
	if s == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	remove(r.byUser, s.userID, s)
	remove(r.bySession, s.sessionID, s)
	remove(r.byScopeUser, scopeUser{s.scope, s.userID}, s)
}

// SendToUserIn queues msg on every open socket of userID within scope only,
// like SendToUser, and returns how many sockets accepted it.
func (r *Registry) SendToUserIn(scope string, userID int64, msg []byte) int {
	sent := 0
	for _, s := range snapshotOf(r, r.byScopeUser, scopeUser{scope, userID}) {
		if s.Offer(msg) {
			sent++
		}
	}

	return sent
}

// CloseUserIn closes every socket of userID within scope with c, for example
// every room socket of a kicked member, and returns how many it closed. The
// user's sockets of other scopes and sessions stay open.
func (r *Registry) CloseUserIn(scope string, userID int64, c Closure) int {
	closed := 0
	for _, s := range snapshotOf(r, r.byScopeUser, scopeUser{scope, userID}) {
		if s.Close(c) {
			closed++
		}
	}

	return closed
}

// SendToUser queues msg on every open socket of userID and returns how many
// sockets accepted it. A socket whose queue is full is closed as a slow client
// instead of being waited on. msg must not be modified afterwards.
func (r *Registry) SendToUser(userID int64, msg []byte) int {
	sockets := snapshotOf(r, r.byUser, userID)
	sent := 0
	for _, s := range sockets {
		if s.Offer(msg) {
			sent++
		}
	}

	return sent
}

// CloseSession closes every socket of sessionID with c and keeps the session
// refused by Add for RevokedTTL. It returns how many sockets it closed. A
// repeated call is harmless.
func (r *Registry) CloseSession(sessionID string, c Closure) int {
	r.mu.Lock()
	r.pruneRevokedLocked()
	r.revoked[sessionID] = r.opts.Now()
	sockets := collect(r.bySession[sessionID])
	r.mu.Unlock()

	closed := 0
	for _, s := range sockets {
		if s.Close(c) {
			closed++
		}
	}

	return closed
}

// CloseAll refuses every later Add and closes every socket with c, for
// shutdown. It returns how many sockets it closed.
func (r *Registry) CloseAll(c Closure) int {
	r.mu.Lock()
	r.closed = true
	var sockets []*Socket
	for _, set := range r.byUser {
		sockets = append(sockets, collect(set)...)
	}
	r.mu.Unlock()

	closed := 0
	for _, s := range sockets {
		if s.Close(c) {
			closed++
		}
	}

	return closed
}

// ConnectedUsers returns the distinct ids among ids that hold at least one
// open socket here, ascending. A socket that is already closing does not count.
func (r *Registry) ConnectedUsers(ids []int64) []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	connected := make([]int64, 0, len(ids))
	for _, id := range ids {
		for s := range r.byUser[id] {
			if !s.isClosing() {
				connected = append(connected, id)
				break
			}
		}
	}
	slices.Sort(connected)

	return slices.Compact(connected)
}

// Len returns how many sockets are registered, closing ones included.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, set := range r.byUser {
		n += len(set)
	}

	return n
}

func snapshotOf[K comparable](r *Registry, index map[K]map[*Socket]struct{}, key K) []*Socket {
	r.mu.Lock()
	defer r.mu.Unlock()

	return collect(index[key])
}

func (r *Registry) pruneRevokedLocked() {
	cutoff := r.opts.Now().Add(-r.opts.RevokedTTL)
	for id, at := range r.revoked {
		if at.Before(cutoff) {
			delete(r.revoked, id)
		}
	}
}

func insert[K comparable](index map[K]map[*Socket]struct{}, key K, s *Socket) {
	set := index[key]
	if set == nil {
		set = map[*Socket]struct{}{}
		index[key] = set
	}
	set[s] = struct{}{}
}

func remove[K comparable](index map[K]map[*Socket]struct{}, key K, s *Socket) {
	set := index[key]
	delete(set, s)
	if len(set) == 0 {
		delete(index, key)
	}
}

func collect(set map[*Socket]struct{}) []*Socket {
	sockets := make([]*Socket, 0, len(set))
	for s := range set {
		sockets = append(sockets, s)
	}

	return sockets
}

// Socket is one registered connection. Its writer goroutine reads Send until
// Closing is closed, then writes Closure().Final (if any) and the close frame.
type Socket struct {
	scope     string
	userID    int64
	sessionID string
	send      chan []byte
	closing   chan struct{}
	slow      Closure

	once    sync.Once
	closure Closure
}

// Scope returns the scope given to AddIn ("" for Add).
func (s *Socket) Scope() string { return s.scope }

// UserID returns the authenticated user of the socket.
func (s *Socket) UserID() int64 { return s.userID }

// SessionID returns the authenticated session of the socket.
func (s *Socket) SessionID() string { return s.sessionID }

// Send delivers queued messages in order. It is never closed; stop on Closing.
func (s *Socket) Send() <-chan []byte { return s.send }

// Closing is closed once the socket must close; Closure then says how.
func (s *Socket) Closing() <-chan struct{} { return s.closing }

// Closure returns how the socket closes. It is only meaningful after Closing
// has been closed.
func (s *Socket) Closure() Closure {
	<-s.closing

	return s.closure
}

// Offer queues msg on this socket only, for example the ready event or an
// error reply. It never blocks: a full queue closes the socket as a slow
// client. It reports whether msg was queued.
func (s *Socket) Offer(msg []byte) bool {
	if s.isClosing() {
		return false
	}
	select {
	case s.send <- msg:
		return true
	default:
		s.Close(s.slow)

		return false
	}
}

// Close starts closing the socket with c. Only the first call takes effect; it
// reports whether this call did.
func (s *Socket) Close(c Closure) bool {
	first := false
	s.once.Do(func() {
		if len(c.Reason) > maxReasonBytes {
			c.Reason = c.Reason[:maxReasonBytes]
		}
		s.closure = c
		close(s.closing)
		first = true
	})

	return first
}

func (s *Socket) isClosing() bool {
	select {
	case <-s.closing:
		return true
	default:
		return false
	}
}
