package unit_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/shared/socketregistry"
)

func mustAdd(t *testing.T, r *socketregistry.Registry, userID int64, sessionID string) *socketregistry.Socket {
	t.Helper()
	s, err := r.Add(userID, sessionID)
	if err != nil {
		t.Fatalf("Add(%d, %q): %v", userID, sessionID, err)
	}

	return s
}

func received(t *testing.T, s *socketregistry.Socket) []string {
	t.Helper()
	var got []string
	for {
		select {
		case msg := <-s.Send():
			got = append(got, string(msg))
		default:
			return got
		}
	}
}

func closingNow(s *socketregistry.Socket) bool {
	select {
	case <-s.Closing():
		return true
	default:
		return false
	}
}

func TestSocketRegistryDeliversToEveryTabOfTheUserOnly(t *testing.T) {
	r := socketregistry.New(socketregistry.Options{})
	tab1 := mustAdd(t, r, 42, "session-a")
	tab2 := mustAdd(t, r, 42, "session-a")
	otherSession := mustAdd(t, r, 42, "session-b")
	stranger := mustAdd(t, r, 43, "session-c")

	if n := r.SendToUser(42, []byte("hello")); n != 3 {
		t.Fatalf("SendToUser returned %d, want 3", n)
	}
	for name, s := range map[string]*socketregistry.Socket{"tab1": tab1, "tab2": tab2, "other session": otherSession} {
		if got := received(t, s); len(got) != 1 || got[0] != "hello" {
			t.Errorf("%s received %q, want [hello]", name, got)
		}
	}
	if got := received(t, stranger); len(got) != 0 {
		t.Errorf("another user received %q", got)
	}
	if n := r.SendToUser(99, []byte("nobody")); n != 0 {
		t.Errorf("SendToUser for a user without sockets returned %d", n)
	}
}

func TestSocketRegistryKeepsMessageOrderPerSocket(t *testing.T) {
	r := socketregistry.New(socketregistry.Options{QueueSize: 8})
	s := mustAdd(t, r, 1, "s")
	for _, m := range []string{"1", "2", "3"} {
		r.SendToUser(1, []byte(m))
	}
	if got := strings.Join(received(t, s), ","); got != "1,2,3" {
		t.Fatalf("order %q, want 1,2,3", got)
	}
}

func TestSocketRegistryCloseSessionClosesOnlyThatSession(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	r := socketregistry.New(socketregistry.Options{RevokedTTL: time.Minute, Now: func() time.Time { return now }})
	tab1 := mustAdd(t, r, 42, "revoked")
	tab2 := mustAdd(t, r, 42, "revoked")
	kept := mustAdd(t, r, 42, "kept")
	revocation := socketregistry.Closure{Code: 4401, Reason: "SESSION_REVOKED", Final: []byte(`{"type":"error"}`)}

	if n := r.CloseSession("revoked", revocation); n != 2 {
		t.Fatalf("CloseSession closed %d sockets, want 2", n)
	}
	for name, s := range map[string]*socketregistry.Socket{"tab1": tab1, "tab2": tab2} {
		if !closingNow(s) {
			t.Fatalf("%s is not closing", name)
		}
		c := s.Closure()
		if c.Code != 4401 || c.Reason != "SESSION_REVOKED" || string(c.Final) != `{"type":"error"}` {
			t.Errorf("%s closure %+v", name, c)
		}
	}
	if closingNow(kept) {
		t.Fatal("a socket of another session of the same user was closed")
	}
	if n := r.CloseSession("revoked", revocation); n != 0 {
		t.Errorf("a repeated revocation closed %d sockets", n)
	}
	if got := r.ConnectedUsers([]int64{42}); len(got) != 1 {
		t.Errorf("the user's other session should keep them connected, got %v", got)
	}

	// A handshake that validated the session before the revocation arrived
	// must not register it afterwards.
	if _, err := r.Add(42, "revoked"); !errors.Is(err, socketregistry.ErrRevoked) {
		t.Fatalf("Add after revocation: %v, want ErrRevoked", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := r.Add(42, "revoked"); err != nil {
		t.Fatalf("Add after RevokedTTL: %v", err)
	}
}

func TestSocketRegistryClosesASlowClientInsteadOfWaiting(t *testing.T) {
	r := socketregistry.New(socketregistry.Options{QueueSize: 2})
	slow := mustAdd(t, r, 7, "slow")
	fast := mustAdd(t, r, 8, "fast")

	done := make(chan int, 1)
	go func() {
		total := 0
		for i := 0; i < 5; i++ {
			total += r.SendToUser(7, []byte("x"))
		}
		done <- total
	}()
	select {
	case total := <-done:
		if total != 2 {
			t.Errorf("a full queue accepted %d messages, want 2", total)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendToUser blocked on a full queue")
	}
	if !closingNow(slow) {
		t.Fatal("the slow socket was not closed")
	}
	if c := slow.Closure(); c.Code != socketregistry.CloseTryAgainLater || c.Reason != "SLOW_CONSUMER" {
		t.Errorf("slow closure %+v", c)
	}
	if slow.Offer([]byte("late")) {
		t.Error("a closing socket accepted a message")
	}
	if r.SendToUser(8, []byte("y")) != 1 || closingNow(fast) {
		t.Error("another user's socket was affected by the slow client")
	}
}

func TestSocketRegistryConnectedUsersIsDistinctAscendingAndLive(t *testing.T) {
	r := socketregistry.New(socketregistry.Options{})
	mustAdd(t, r, 30, "a")
	mustAdd(t, r, 10, "b")
	mustAdd(t, r, 10, "c")
	closing := mustAdd(t, r, 20, "d")
	closing.Close(socketregistry.Closure{Code: 1000})
	removed := mustAdd(t, r, 40, "e")
	r.Remove(removed)
	r.Remove(removed)

	got := r.ConnectedUsers([]int64{40, 30, 20, 10, 30, 50})
	if want := []int64{10, 30}; !equalIDs(got, want) {
		t.Fatalf("ConnectedUsers = %v, want %v", got, want)
	}
	if got := r.ConnectedUsers(nil); len(got) != 0 {
		t.Errorf("ConnectedUsers(nil) = %v", got)
	}
}

func TestSocketRegistryCloseAllClosesEverythingAndRefusesNewSockets(t *testing.T) {
	r := socketregistry.New(socketregistry.Options{})
	a := mustAdd(t, r, 1, "a")
	b := mustAdd(t, r, 2, "b")
	if n := r.CloseAll(socketregistry.Closure{Code: socketregistry.CloseGoingAway, Reason: "GOING_AWAY"}); n != 2 {
		t.Fatalf("CloseAll closed %d, want 2", n)
	}
	for _, s := range []*socketregistry.Socket{a, b} {
		if c := s.Closure(); c.Code != socketregistry.CloseGoingAway {
			t.Errorf("closure %+v", c)
		}
	}
	if _, err := r.Add(3, "c"); !errors.Is(err, socketregistry.ErrClosed) {
		t.Fatalf("Add after CloseAll: %v, want ErrClosed", err)
	}
}

func TestSocketRegistryRejectsAnonymousSocketsAndKeepsTheFirstClosure(t *testing.T) {
	r := socketregistry.New(socketregistry.Options{})
	for _, tc := range []struct {
		user    int64
		session string
	}{{0, "s"}, {-1, "s"}, {1, ""}} {
		if _, err := r.Add(tc.user, tc.session); !errors.Is(err, socketregistry.ErrInvalidSocket) {
			t.Errorf("Add(%d, %q): %v, want ErrInvalidSocket", tc.user, tc.session, err)
		}
	}
	s := mustAdd(t, r, 1, "s")
	if !s.Close(socketregistry.Closure{Code: 4401, Reason: strings.Repeat("r", 200)}) {
		t.Fatal("the first Close did not take effect")
	}
	if s.Close(socketregistry.Closure{Code: 1000}) {
		t.Fatal("a second Close took effect")
	}
	if c := s.Closure(); c.Code != 4401 || len(c.Reason) != 123 {
		t.Errorf("closure code %d, reason length %d; want 4401 and 123", c.Code, len(c.Reason))
	}
	if s.UserID() != 1 || s.SessionID() != "s" {
		t.Errorf("identity %d %q", s.UserID(), s.SessionID())
	}
}

// TestSocketRegistryConcurrentUse runs every operation at once; run with -race.
func TestSocketRegistryConcurrentUse(t *testing.T) {
	r := socketregistry.New(socketregistry.Options{QueueSize: 4})
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			user := int64(g%4 + 1)
			session := string(rune('a' + g%3))
			for i := 0; i < 200; i++ {
				s, err := r.Add(user, session)
				if err != nil {
					continue
				}
				r.SendToUser(user, []byte("m"))
				r.ConnectedUsers([]int64{1, 2, 3, 4})
				received(t, s)
				if i%50 == 0 {
					r.CloseSession("z", socketregistry.Closure{Code: 4401})
				}
				r.Remove(s)
			}
		}(g)
	}
	wg.Wait()
	if n := r.Len(); n != 0 {
		t.Fatalf("Len = %d after every socket was removed", n)
	}
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestSocketRegistryScopesKeepRoomsApart(t *testing.T) {
	r := socketregistry.New(socketregistry.Options{})
	room1Tab1 := mustAddIn(t, r, "1001", 42, "a")
	room1Tab2 := mustAddIn(t, r, "1001", 42, "b")
	room2 := mustAddIn(t, r, "2002", 42, "a")
	otherMember := mustAddIn(t, r, "1001", 43, "c")
	unscoped := mustAdd(t, r, 42, "d")

	if room1Tab1.Scope() != "1001" || unscoped.Scope() != "" {
		t.Fatalf("scopes %q %q", room1Tab1.Scope(), unscoped.Scope())
	}
	if n := r.SendToUserIn("1001", 42, []byte("evt")); n != 2 {
		t.Fatalf("SendToUserIn reached %d sockets, want 2", n)
	}
	for name, s := range map[string]*socketregistry.Socket{"room 2": room2, "other member": otherMember, "unscoped": unscoped} {
		if got := received(t, s); len(got) != 0 {
			t.Errorf("%s received %q", name, got)
		}
	}
	received(t, room1Tab1)
	received(t, room1Tab2)

	kicked := socketregistry.Closure{Code: 4403, Reason: "KICKED_FROM_ROOM"}
	if n := r.CloseUserIn("1001", 42, kicked); n != 2 {
		t.Fatalf("CloseUserIn closed %d, want 2", n)
	}
	if !closingNow(room1Tab1) || !closingNow(room1Tab2) || room1Tab2.Closure().Code != 4403 {
		t.Fatal("the member's sockets of that room were not closed with 4403")
	}
	for name, s := range map[string]*socketregistry.Socket{"room 2": room2, "other member": otherMember, "unscoped": unscoped} {
		if closingNow(s) {
			t.Errorf("%s was closed", name)
		}
	}
	// The unscoped index and the user index still see every socket.
	if n := r.SendToUser(42, []byte("all")); n != 2 {
		t.Errorf("SendToUser after the room closure reached %d open sockets, want 2 (room 2 and unscoped)", n)
	}
	r.Remove(room1Tab1)
	r.Remove(room1Tab2)
	if n := r.CloseUserIn("1001", 42, kicked); n != 0 {
		t.Errorf("removed sockets were closed again: %d", n)
	}
}

func mustAddIn(t *testing.T, r *socketregistry.Registry, scope string, userID int64, sessionID string) *socketregistry.Socket {
	t.Helper()
	s, err := r.AddIn(scope, userID, sessionID)
	if err != nil {
		t.Fatalf("AddIn(%q, %d, %q): %v", scope, userID, sessionID, err)
	}

	return s
}
