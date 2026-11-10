package game

import "testing"

// hiddenColorCount: snapshot'taki tahtada rengi gizli (nil) kart sayısı.
func hiddenColorCount(t *testing.T, g *Game, role *Role) int {
	t.Helper()
	snapshot, err := g.SnapshotForRole(role)
	if err != nil {
		t.Fatalf("SnapshotForRole: %v", err)
	}
	hidden := 0
	for _, card := range snapshot.Board {
		if card.Color == nil {
			hidden++
		}
	}
	return hidden
}

func TestOperativeSeesNoColorsWhileInProgress(t *testing.T) {
	game := newTestGame()
	operative := RoleOperative

	if hidden := hiddenColorCount(t, game, &operative); hidden != 25 {
		t.Fatalf("operative must see no colors during play, %d hidden", hidden)
	}
	if hidden := hiddenColorCount(t, game, nil); hidden != 25 {
		t.Fatalf("unseated member must see no colors during play, %d hidden", hidden)
	}
}

func TestEveryoneSeesAllColorsWhenFinished(t *testing.T) {
	game := newTestGame()
	winner := TeamRed
	game.finish(&winner, EndReasonAllTeamCardsRevealed, testNow)

	operative := RoleOperative
	if hidden := hiddenColorCount(t, game, &operative); hidden != 0 {
		t.Fatalf("operative must see every color after a finished game, %d hidden", hidden)
	}
	if hidden := hiddenColorCount(t, game, nil); hidden != 0 {
		t.Fatalf("unseated member must see every color after a finished game, %d hidden", hidden)
	}
}

func TestEveryoneSeesAllColorsWhenCancelled(t *testing.T) {
	game := newTestGame()
	if err := game.cancelForInsufficientPlayers(testNow); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	operative := RoleOperative
	if hidden := hiddenColorCount(t, game, &operative); hidden != 0 {
		t.Fatalf("operative must see every color after cancellation, %d hidden", hidden)
	}
	spymaster := RoleSpymaster
	if hidden := hiddenColorCount(t, game, &spymaster); hidden != 0 {
		t.Fatalf("spymaster always sees colors, %d hidden", hidden)
	}
}
