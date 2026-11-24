package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/avatarfiles"
	userpostgres "github.com/melmut-42/ft_transcendence/backend/user-service/internal/repository/postgres"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

// TestAvatarSweepPostgres checks which files the sweep removes.
//
// Expected output: an old unreferenced upload and an old abandoned temporary
// file are removed; a referenced upload, an upload younger than the grace
// period and a file the store did not write are kept; a second sweep removes
// nothing.
func TestAvatarSweepPostgres(t *testing.T) {
	pool := schemaPool(t)
	seedProfiles(t, pool, 3)
	referenced := "3-0123456789abcdef0123456789abcdef.png"
	orphan := "3-fedcba9876543210fedcba9876543210.png"
	fresh := "3-00112233445566778899aabbccddeeff.png"
	temporary := ".upload-0011223344556677"
	foreign := "notes.txt"
	mustExec(t, pool, `UPDATE profiles SET avatar_kind = 'upload', avatar_ref = $1 WHERE user_id = 3`, referenced)

	dir := t.TempDir()
	store, err := avatarfiles.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now()
	old := now.Add(-2 * service.AvatarSweepGrace)
	for name, modified := range map[string]time.Time{
		referenced: old, orphan: old, fresh: now, temporary: old, foreign: old,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}

	sweep := service.NewAvatarSweep(store, userpostgres.NewAvatars(pool))
	var failures []string
	sweep.OnError = func(name string, _ error) { failures = append(failures, name) }
	removed, err := sweep.Sweep(context.Background(), now)
	if err != nil || removed != 2 || len(failures) != 0 {
		t.Fatalf("Sweep() = %d, %v, failures %v", removed, err, failures)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, entry := range entries {
		left = append(left, entry.Name())
	}
	want := []string{fresh, referenced, foreign}
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Fatalf("files left = %v, want %v", left, want)
	}
	if removed, err := sweep.Sweep(context.Background(), now); err != nil || removed != 0 {
		t.Fatalf("second Sweep() = %d, %v", removed, err)
	}
}
