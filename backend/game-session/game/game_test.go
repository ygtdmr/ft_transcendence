package game

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// Game tests

// Game state tests

func TestCheckInitialGameState(t *testing.T) {
	wpOriginal := "wordpack.json"
	wordPack := loadTestWordPack(t, wpOriginal)
	var seed [32]byte

	for testseed := byte(1); testseed < 200; testseed++ {
		seed[0] = testseed

		game, err := NewGameWithSeed(wordPack.Words, testSettings, seed, testNow)
		if err != nil {
			t.Fatalf("NewGameWithSeed returned an error: %v", err)
		}
		if game.currentTurn.Team != game.board.startingTeam {
			t.Errorf("CurrentTurn.Team (%v) does not match Board.startingTeam (%v)", game.currentTurn.Team, game.board.startingTeam)
		}
		if game.currentTurn.Phase != PhaseWaitingForClue {
			t.Errorf("CurrentTurn.Phase is not PhaseWaitingForClue, got: %v", game.currentTurn.Phase)
		}
		if game.currentTurn.Clue != nil || game.currentTurn.GuessesRemaining != nil {
			t.Errorf("CurrentTurn Clue or GuessesRemaining are not nil, got: %v", game.currentTurn.Clue)
		}
		if game.winner != nil || game.endReason != nil {
			t.Errorf("Winner or EndReason are not nil, got: %v", game.winner)
		}
		if game.Score().Blue != 0 || game.Score().Red != 0 {
			t.Error("Team scores are not 0")
		}
	}
}

func TestScoreCalculation(t *testing.T) {
	wpOriginal := "wordpack.json"
	wordPack := loadTestWordPack(t, wpOriginal)
	seed := [32]byte{42}
	game, err := NewGameWithSeed(wordPack.Words, testSettings, seed, testNow)
	if err != nil {
		t.Fatalf("NewGameWithSeed returned an error: %v", err)
	}

	expectedRed, expectedBlue := 0, 0
	for i, card := range game.board.cards {
		if i < 9 {
			revealed, err := game.board.reveal(card.cardID)
			if err != nil {
				t.Fatalf("reveal returned an error: %v", err)
			}
			switch revealed.color {
			case CardColorRed:
				expectedRed++
			case CardColorBlue:
				expectedBlue++
			}
		}
		if game.Score().Red != expectedRed || game.Score().Blue != expectedBlue {
			t.Errorf("wrong score: expected Red %d, Blue %d, got Red %d, Blue %d", expectedRed, expectedBlue, game.Score().Red, game.Score().Blue)
		}
	}
}

func TestValidateGameState(t *testing.T) {
	wpOriginal := "wordpack.json"
	wordPack := loadTestWordPack(t, wpOriginal)
	seed := [32]byte{42}
	game, err := NewGameWithSeed(wordPack.Words, testSettings, seed, testNow)
	if err != nil {
		t.Fatalf("NewGameWithSeed returned an error: %v", err)
	}

	err = game.ValidateGameState()
	if err != nil {
		t.Fatalf("ValidateGameState returned an error: %v", err)
	}
	team := game.board.startingTeam
	game.winner = &team
	err = game.ValidateGameState()
	if !errors.Is(err, ErrInvalidGameState) {
		t.Fatalf("ValidateGameState should have returned an error for winner set while in waiting for clue phase, but got: %v", err)
	}
}

// Game flow tests

var testNow = time.Date(2026, 9, 10, 8, 43, 15, 0, time.UTC)

var testSettings = GameSettings{
	Language:        "en",
	TurnTimer:       nil,
	StaffingTimeout: 120 * time.Second,
	GameID:          5001,
}

func TestNewGameRejectsInvalidStaffingTimeout(t *testing.T) {
	wordPack := loadTestWordPack(t, "wordpack.json")
	seed := [32]byte{42}

	zeroSettings := testSettings
	zeroSettings.StaffingTimeout = 0
	if _, err := NewGameWithSeed(wordPack.Words, zeroSettings, seed, testNow); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for zero staffing timeout, got %v", err)
	}

	negativeSettings := testSettings
	negativeSettings.StaffingTimeout = -time.Second
	if _, err := NewGameWithSeed(wordPack.Words, negativeSettings, seed, testNow); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for negative staffing timeout, got %v", err)
	}
}

func newTestGame() *Game {
	game := &Game{
		gameID: 5000,
		board: &Board{
			startingTeam: TeamRed,
		},
		currentTurn: CurrentTurn{
			Team:  TeamRed,
			Phase: PhaseWaitingForClue,
		},
	}
	for i := range game.board.cards {
		color := CardColorAssassin

		if i >= 0 && i < 9 {
			color = CardColorRed
		} else if i >= 9 && i < 17 {
			color = CardColorBlue
		} else if i >= 17 && i < 24 {
			color = CardColorNeutral
		} else {
			color = CardColorAssassin
		}
		game.board.cards[i] = card{
			cardID: i + 1,
			word:   fmt.Sprintf("word%d", i+1),
			color:  color,
		}
	}
	return game
}

func TestGameFlow(t *testing.T) {
	game := newTestGame()
	PlayerRedSpy := &Player{UserID: 1, Team: TeamRed, Role: RoleSpymaster}
	PlayerRedOpr := &Player{UserID: 2, Team: TeamRed, Role: RoleOperative}
	PlayerBlueSpy := &Player{UserID: 3, Team: TeamBlue, Role: RoleSpymaster}
	PlayerBlueOpr := &Player{UserID: 4, Team: TeamBlue, Role: RoleOperative}

	turn1, err := game.GiveClue(PlayerRedSpy, &Clue{Word: "hint", Number: 2}, testNow)
	if err != nil {
		t.Fatalf("First GiveClue: %v", err)
	}
	if turn1.Phase != PhaseGuessing ||
		turn1.GuessesRemaining == nil || *turn1.GuessesRemaining != 3 {
		t.Fatalf("After GiveClue is wrong, %+v", turn1)
	}
	turn2, err := game.GuessCard(PlayerRedOpr, 2, testNow)
	if err != nil {
		t.Fatalf("First GuessCard: %v", err)
	}
	if turn2.CurrentTurn.Phase != PhaseGuessing ||
		turn2.CurrentTurn.GuessesRemaining == nil || *turn2.CurrentTurn.GuessesRemaining != 2 ||
		turn2.CurrentTurn.Team != TeamRed || turn2.IsCorrectGuess != true ||
		turn2.ChangeReason != nil || turn2.EndReason != nil {
		if turn2.CurrentTurn.Clue != nil {
			t.Logf("clue: %+v", turn2.CurrentTurn.Clue)
		}
		if turn2.CurrentTurn.GuessesRemaining != nil {
			t.Logf("GuessRemaining: %d", *turn2.CurrentTurn.GuessesRemaining)
		}
		t.Fatalf("After GuessCard is wrong, %+v", turn2)
	}
	turn3, err := game.PassTurn(PlayerRedOpr, testNow)
	if err != nil {
		t.Fatalf("After PassTurn is wrong, %+v", turn3)
	}
	if turn3.CurrentTurn.Team != TeamBlue ||
		turn3.CurrentTurn.Phase != PhaseWaitingForClue ||
		turn3.CurrentTurn.Clue != nil ||
		turn3.CurrentTurn.GuessesRemaining != nil {
		t.Fatalf("pass sonrası beklenmeyen tur: %+v", turn3.CurrentTurn)
	}

	before := cloneTestGame(game)
	turn4, err := game.GiveClue(PlayerRedSpy, &Clue{Word: "word1", Number: 1}, testNow)
	if err == nil {
		t.Fatalf("Wrong Team GiveClue (RedTeam) should be give an error, %+v", turn4)
	}
	checkGameUnchanged(t, before, game)

	before = cloneTestGame(game)
	turn5, err := game.GiveClue(PlayerBlueSpy, &Clue{Word: "word1", Number: 1}, testNow)
	if err == nil {
		t.Fatalf("Second GiveClue (BlueTeam) should be give an error, %+v", turn5)
	}
	checkGameUnchanged(t, before, game)

	turn6, err := game.GiveClue(PlayerBlueSpy, &Clue{Word: "hint", Number: 1}, testNow)
	if err != nil {
		t.Fatalf("Second GiveClue (BlueTeam) is wrong, %+v", turn6)
	}
	turn7, err := game.GuessCard(PlayerBlueOpr, 25, testNow)
	if err != nil {
		t.Fatalf("First GuessCard (Assassin): %v", err)
	}
	if turn7.CurrentTurn.Phase != PhaseGameOver ||
		turn7.CurrentTurn.GuessesRemaining != nil ||
		turn7.IsCorrectGuess != false ||
		turn7.ChangeReason != nil || turn7.EndReason == nil ||
		*turn7.EndReason != EndReasonAssassinRevealed {

		t.Fatalf("After GuessCard (BlueTeam Assassin Card) is wrong, %+v", turn2)
	}

	before = cloneTestGame(game)
	_, err = game.GiveClue(PlayerRedSpy, &Clue{Word: "hint", Number: 1}, testNow)
	if !errors.Is(err, ErrGameAlreadyFinished) {
		t.Fatalf("After game - clue: %v", err)
	}
	checkGameUnchanged(t, before, game)

	before = cloneTestGame(game)
	_, err = game.GuessCard(PlayerBlueOpr, 10, testNow)
	if !errors.Is(err, ErrGameAlreadyFinished) {
		t.Fatalf("After game - guess: %v", err)
	}
	checkGameUnchanged(t, before, game)

	before = cloneTestGame(game)
	_, err = game.PassTurn(PlayerBlueOpr, testNow)
	if !errors.Is(err, ErrGameAlreadyFinished) {
		t.Fatalf("After game - pass: %v", err)
	}
	checkGameUnchanged(t, before, game)
}

// Give clue tests

func TestGiveClue(t *testing.T) {
	game := &Game{
		board: &Board{},
		currentTurn: CurrentTurn{
			Team:  TeamRed,
			Phase: PhaseWaitingForClue,
		},
	}
	player := &Player{UserID: 1, Team: TeamRed, Role: RoleSpymaster}
	clue := &Clue{Word: "validclue", Number: 3}

	currTurn, err := game.GiveClue(player, clue, testNow)
	if err != nil {
		t.Fatalf("GiveClue returned an error: %v", err)
	}
	if currTurn.Phase != PhaseGuessing ||
		currTurn.Clue == nil ||
		currTurn.Clue.Word != clue.Word ||
		currTurn.Clue.Number != clue.Number ||
		currTurn.GuessesRemaining == nil ||
		*currTurn.GuessesRemaining != clue.Number+1 {
		t.Errorf("unexpected turn after GiveClue: %+v", currTurn)
	}
}

func TestGiveClueErrors(t *testing.T) {
	game := &Game{
		board: &Board{},
		currentTurn: CurrentTurn{
			Team:  TeamRed,
			Phase: PhaseWaitingForClue,
		},
	}
	player := &Player{UserID: 1, Team: TeamRed, Role: RoleSpymaster}
	clue := &Clue{Word: "wrong clue", Number: 3}

	before := cloneTestGame(game)
	_, err := game.GiveClue(player, clue, testNow)
	if err == nil || !errors.Is(err, ErrInvalidClue) || !errors.Is(err, ErrClueInvalidFormat) {
		t.Fatalf("expected ErrInvalidClue with ErrClueInvalidFormat but got: %v", err)
	}
	checkGameUnchanged(t, before, game)

	clue = &Clue{Word: "exceeedlengththirtyexceeedlengththirty", Number: 3}
	before = cloneTestGame(game)
	_, err = game.GiveClue(player, clue, testNow)
	if err == nil || !errors.Is(err, ErrInvalidClue) || !errors.Is(err, ErrClueInvalidFormat) {
		t.Fatalf("expected ErrInvalidClue with ErrClueInvalidFormat but got: %v", err)
	}
	checkGameUnchanged(t, before, game)

	clue = &Clue{Word: "", Number: 3}
	before = cloneTestGame(game)
	_, err = game.GiveClue(player, clue, testNow)
	if err == nil || !errors.Is(err, ErrInvalidClue) || !errors.Is(err, ErrClueInvalidFormat) {
		t.Fatalf("expected ErrInvalidClue with ErrClueInvalidFormat but got: %v", err)
	}
	checkGameUnchanged(t, before, game)

	clue = &Clue{Word: "word", Number: 12}
	before = cloneTestGame(game)
	_, err = game.GiveClue(player, clue, testNow)
	if err == nil || !errors.Is(err, ErrInvalidClueNumber) {
		t.Fatalf("expected ErrInvalidClueNumber but got: %v", err)
	}
	checkGameUnchanged(t, before, game)

	game.board.cards[0].word = "existingword"
	clue = &Clue{Word: "ExistinGwOrd", Number: 3}
	before = cloneTestGame(game)
	_, err = game.GiveClue(player, clue, testNow)
	if err == nil || !errors.Is(err, ErrInvalidClue) || !errors.Is(err, ErrClueWordOnBoard) {
		t.Fatalf("expected ErrInvalidClue with ErrClueWordOnBoard but got: %v", err)
	}
	checkGameUnchanged(t, before, game)

	game.board.cards[1].word = "AFRİKA"
	before = cloneTestGame(game)
	_, err = game.GiveClue(player, &Clue{Word: "afrika", Number: 3}, testNow)
	if err == nil || !errors.Is(err, ErrInvalidClue) || !errors.Is(err, ErrClueWordOnBoard) {
		t.Fatalf("expected ErrInvalidClue with ErrClueWordOnBoard for Turkish board word but got: %v", err)
	}
	checkGameUnchanged(t, before, game)

	player = &Player{UserID: 2, Team: TeamBlue, Role: RoleSpymaster}
	before = cloneTestGame(game)
	_, err = game.GiveClue(player, clue, testNow)
	if err == nil || !errors.Is(err, ErrNotYourTurn) {
		t.Fatalf("expected ErrNotYourTurn but got: %v", err)
	}
	checkGameUnchanged(t, before, game)

	player = &Player{UserID: 1, Team: TeamRed, Role: RoleOperative}
	before = cloneTestGame(game)
	_, err = game.GiveClue(player, clue, testNow)
	if err == nil || !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("expected ErrRoleForbidden but got: %v", err)
	}
	checkGameUnchanged(t, before, game)
}

// Guess card and pass tests

func cloneTestGame(g *Game) *Game {
	copied := *g

	if g.board != nil {
		board := *g.board
		copied.board = &board
	}

	copied.currentTurn = g.currentTurn.clone()

	if g.winner != nil {
		winner := *g.winner
		copied.winner = &winner
	}
	if g.endReason != nil {
		reason := *g.endReason
		copied.endReason = &reason
	}
	return &copied
}

func checkGameUnchanged(t *testing.T, before, after *Game) {
	t.Helper()

	if !reflect.DeepEqual(before, after) {
		t.Fatal("Rejected action changed the game state")
	}
}

func initGuessingTestGame(t *testing.T, clueNumber int, guessRemaining int) *Game {
	t.Helper()
	game := newTestGame()

	game.currentTurn.Phase = PhaseGuessing
	game.currentTurn.Clue = &Clue{
		Word:   "hint",
		Number: clueNumber,
	}
	game.currentTurn.GuessesRemaining = &guessRemaining

	if _, err := game.board.reveal(2); err != nil {
		t.Fatalf("guessing preparing - reveal: %v", err)
	}
	return game
}

func TestGuessCard(t *testing.T) {
	game := initGuessingTestGame(t, 2, 1)
	player := &Player{UserID: 1, Team: TeamRed, Role: RoleOperative}

	result, err := game.GuessCard(player, 5, testNow)
	if err != nil {
		t.Fatalf("GuessCard returned an error: %v", err)
	}
	if *result.Card.Color != CardColorRed || !result.Card.Revealed ||
		result.Card.CardID != 5 || result.Card.Word != "word5" {
		t.Fatal("GuessCard-Cardview returned an error")
	}
	if result.GuessingTeam != player.Team || !result.IsCorrectGuess {
		t.Fatal("Score calculation returned an error")
	}
	if game.Score().Red != 2 {
		t.Fatal("Score calculation returned an error")
	}
	if result.ChangeReason == nil || *result.ChangeReason != ChangeReasonGuessesExhausted {
		t.Fatal("expected GUESSES_EXHAUSTED reason")
	}
	if result.CurrentTurn.Phase != PhaseWaitingForClue || result.CurrentTurn.Team != TeamBlue || result.CurrentTurn.GuessesRemaining != nil {
		t.Fatal("GuessCard - CurrentTurn and GuessRemaining returned an error")
	}
}

func TestRejectedGameActions(t *testing.T) {
	game := initGuessingTestGame(t, 2, 1)
	player := &Player{
		UserID: 1,
		Team:   TeamRed,
		Role:   RoleOperative,
	}

	before := cloneTestGame(game)
	_, err := game.GuessCard(player, 35, testNow)
	if err == nil {
		t.Fatal("Invalid CardID should give an error")
	}
	if !errors.Is(err, ErrInvalidCard) {
		t.Fatalf("wating ErrInvalidCard but got : %v", err)
	}
	checkGameUnchanged(t, before, game)

	before = cloneTestGame(game)
	spymaster := &Player{Team: TeamRed, Role: RoleSpymaster}
	_, err = game.GiveClue(spymaster, &Clue{Word: "hint", Number: 2}, testNow)
	if !errors.Is(err, ErrInvalidRoomState) {
		t.Fatalf("Waiting ErrInvalidRoomState but got: %v", err)
	}
	checkGameUnchanged(t, before, game)

	before = cloneTestGame(game)
	_, err = game.GuessCard(player, 2, testNow)
	if !errors.Is(err, ErrCardAlreadyRevealed) {
		t.Fatalf("waiting ErrCardAlreadyRevealed but got: %v", err)
	}
	checkGameUnchanged(t, before, game)
}

func TestGuessOtherTeamCard(t *testing.T) {
	game := initGuessingTestGame(t, 2, 1)
	player := &Player{UserID: 1, Team: TeamRed, Role: RoleOperative}

	result, err := game.GuessCard(player, 15, testNow)
	if err != nil {
		t.Fatalf("Other Team: GuessCard returned an error: %v", err)
	}
	if result.ChangeReason == nil {
		t.Fatal("expected ChangeReason ChangeReasonOpponentCardRevealed but got nil")
	}
	if result.GuessingTeam != TeamRed || *result.ChangeReason != ChangeReasonOpponentCardRevealed ||
		result.CurrentTurn.Team != TeamBlue || result.IsCorrectGuess == true ||
		result.CurrentTurn.Phase != PhaseWaitingForClue ||
		result.Score.Blue != 1 || result.CurrentTurn.Clue != nil ||
		result.CurrentTurn.GuessesRemaining != nil {
		t.Fatalf("GuessOtherTeamCard returned an error: \n%+v", result)
	}
}

func TestGuessCardAssassinNeutral(t *testing.T) {
	game := initGuessingTestGame(t, 2, 1)
	player := &Player{UserID: 1, Team: TeamRed, Role: RoleOperative}

	result, err := game.GuessCard(player, 25, testNow)
	if err != nil {
		t.Fatalf("Assassin: GuessCard returned an error: %v", err)
	}
	if result.ChangeReason != nil {
		t.Fatal("terminal result must not have a turn change reason")
	}
	if *result.EndReason != EndReasonAssassinRevealed || *result.Winner != TeamBlue || result.CurrentTurn.Phase != PhaseGameOver {
		t.Fatal("GuessCard - EndReason, Winner, Phase returned an error")
	}

	game = initGuessingTestGame(t, 2, 1)
	result, err = game.GuessCard(player, 20, testNow)
	if err != nil {
		t.Fatalf("Neutral: GuessCard returned an error: %v", err)
	}
	if result.ChangeReason == nil || *result.ChangeReason != ChangeReasonNeutralCardRevealed {
		t.Fatal("expected NEUTRAL_CARD_REVEALED reason")
	}
	if result.CurrentTurn.Phase != PhaseWaitingForClue || result.CurrentTurn.Team != TeamBlue {
		t.Fatal("GuessCard - CurrentTeam returned an error")
	}
}

func TestPassTurn(t *testing.T) {
	game := initGuessingTestGame(t, 2, 2)
	player := &Player{UserID: 5, Team: TeamRed, Role: RoleOperative}

	passTurn, err := game.PassTurn(player, testNow)
	if err != nil {
		t.Fatalf("PassTurn returned an error: %v", err)
	}
	if game.currentTurn.Team != TeamBlue || game.currentTurn.GuessesRemaining != nil || game.currentTurn.Phase != PhaseWaitingForClue || game.currentTurn.Clue != nil || passTurn.ChangeReason != ChangeReasonPassed {
		t.Fatal("PassTurn func is not working correctly.")
	}

	game = initGuessingTestGame(t, 2, 2)
	player = &Player{UserID: 5, Team: TeamRed, Role: RoleOperative}
	*game.currentTurn.GuessesRemaining = 0
	before := cloneTestGame(game)
	passTurn, err = game.PassTurn(player, testNow)
	if err == nil {
		t.Fatal("Expected an error, got none")
	}
	if !errors.Is(err, ErrNoGuessesRemaining) {
		t.Fatalf("Wrong error: expected %v, got %v", ErrNoGuessesRemaining, err)
	}
	checkGameUnchanged(t, before, game)

	game = initGuessingTestGame(t, 2, 2)
	before = cloneTestGame(game)
	player = &Player{UserID: 5, Team: TeamBlue, Role: RoleOperative}
	passTurn, err = game.PassTurn(player, testNow)

	if err == nil {
		t.Fatal("Expected an error, got none")
	}
	if !errors.Is(err, ErrNotYourTurn) {
		t.Fatalf("Wrong error: expected %v, got %v", ErrNotYourTurn, err)
	}
	checkGameUnchanged(t, before, game)
}

func TestGuessFinalTeamCard(t *testing.T) {
	game := newTestGame()
	spymaster := &Player{UserID: 1, Team: TeamRed, Role: RoleSpymaster}
	operative := &Player{UserID: 2, Team: TeamRed, Role: RoleOperative}

	if _, err := game.GiveClue(spymaster, &Clue{Word: "hint", Number: 9}, testNow); err != nil {
		t.Fatalf("GiveClue: %v", err)
	}

	var result GuessResult
	for cardID := 1; cardID <= 9; cardID++ {
		var err error
		result, err = game.GuessCard(operative, cardID, testNow)
		if err != nil {
			t.Fatalf("Card %d Guess: %v", cardID, err)
		}
	}

	if result.Winner == nil || *result.Winner != TeamRed ||
		result.EndReason == nil || *result.EndReason != EndReasonAllTeamCardsRevealed ||
		result.Score != (Score{Red: 9, Blue: 0}) ||
		result.CurrentTurn.Phase != PhaseGameOver ||
		result.CurrentTurn.Clue != nil || result.CurrentTurn.GuessesRemaining != nil ||
		result.ChangeReason != nil {
		t.Fatalf("Unexpected result when all RED cards are revealed: %+v", result)
	}
}

// Game time tests

func TestNewGameStartedAt(t *testing.T) {
	wordPack := loadTestWordPack(t, "wordpack.json")
	seed := [32]byte{42}

	game, err := NewGameWithSeed(wordPack.Words, testSettings, seed, testNow)
	if err != nil {
		t.Fatalf("NewGameWithSeed: %v", err)
	}
	if !game.startedAt.Equal(testNow) {
		t.Fatalf("expected startedAt %v, got %v", testNow, game.startedAt)
	}
}

func TestAssassinSetsFinishedAt(t *testing.T) {
	game := newTestGame()
	spymaster := &Player{UserID: 1, Team: TeamRed, Role: RoleSpymaster}
	operative := &Player{UserID: 2, Team: TeamRed, Role: RoleOperative}

	if _, err := game.GiveClue(spymaster, &Clue{Word: "hint", Number: 2}, testNow); err != nil {
		t.Fatalf("GiveClue: %v", err)
	}
	guessAt := testNow.Add(30 * time.Second)
	if _, err := game.GuessCard(operative, 25, guessAt); err != nil {
		t.Fatalf("GuessCard: %v", err)
	}
	if game.finishedAt == nil || !game.finishedAt.Equal(guessAt) {
		t.Fatalf("expected finishedAt %v, got %v", guessAt, game.finishedAt)
	}
}

func TestCancelSetsFinishedAt(t *testing.T) {
	game := newTestGame()

	game.pauseForPlayers(Player{UserID: 1, Username: "player_one", Team: TeamRed, Role: RoleSpymaster}, testNow)

	cancelAt := testNow.Add(2 * time.Minute)
	if err := game.cancelForInsufficientPlayers(cancelAt); err != nil {
		t.Fatalf("CancelForInsufficientPlayers: %v", err)
	}
	if game.finishedAt == nil || !game.finishedAt.Equal(cancelAt) {
		t.Fatalf("expected finishedAt %v, got %v", cancelAt, game.finishedAt)
	}
}
