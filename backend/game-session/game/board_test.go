package game

import (
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"reflect"
	"testing"
)

// Board tests

// Generator board tests

func loadTestWordPack(t *testing.T, filename string) WordPack {
	t.Helper()

	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("could not read wordpack.json: %v", err)
	}

	var wordPack WordPack

	if err := json.Unmarshal(data, &wordPack); err != nil {
		t.Fatalf("could not parse wordpack.json: %v", err)
	}

	return wordPack
}

func TestNewBoardWithRealWordPackSameSeed(t *testing.T) {
	wpOriginal := "wordpack.json"
	wordPack := loadTestWordPack(t, wpOriginal)
	seed := [32]byte{42}
	r := rand.New(rand.NewChaCha8(seed))

	board, err := NewBoard(wordPack.Words, "en", r)
	if err != nil {
		t.Fatalf("NewBoard returned an error: %v", err)
	}

	cards := board.cards

	t.Logf("BOARD1\nStarting team: %v", board.startingTeam)
	for _, card := range cards {
		t.Logf(
			"CardID: %2d | Word: %-15s | Color: %-7v | Revealed: %t",
			card.cardID,
			card.word,
			card.color,
			card.revealed,
		)
	}

	wordPack = loadTestWordPack(t, wpOriginal)
	seed = [32]byte{42}
	r = rand.New(rand.NewChaCha8(seed))

	board2, err := NewBoard(wordPack.Words, "en", r)
	if err != nil {
		t.Fatalf("NewBoard returned an error: %v", err)
	}

	cards = board2.cards

	t.Logf("BOARD2\nStarting team: %v", board2.startingTeam)
	for _, card := range cards {
		t.Logf(
			"CardID: %2d | Word: %-15s | Color: %-7v | Revealed: %t",
			card.cardID,
			card.word,
			card.color,
			card.revealed,
		)
	}

	if !reflect.DeepEqual(board, board2) {
		t.Errorf("Boards generated with the same seed are not equal")
	}
}

func TestNewBoardWithDiffVariants(t *testing.T) {
	wordPack := loadTestWordPack(t, "wordpack.json")
	var seed [32]byte

	for testseed := byte(1); testseed <= 200; testseed++ {
		seed[0] = testseed
		r := rand.New(rand.NewChaCha8(seed))

		board, err := NewBoard(wordPack.Words, "en", r)
		if err != nil {
			t.Fatalf("seed %d: NewBoard returned an error: %v", testseed, err)
		}

		cards := board.cards

		if len(cards) != BoardSize {
			t.Fatalf("seed %d: expected %d cards, got %d", testseed, BoardSize, len(cards))
		}

		counts := make(map[CardColor]int)
		seen := make(map[string]struct{}, BoardSize)
		for i, card := range cards {
			expectedID := i + 1
			if card.cardID != expectedID {
				t.Fatalf(
					"seed %d: card at index %d: expected CardID %d, got %d",
					testseed, i, expectedID, card.cardID,
				)
			}
			if card.word == "" {
				t.Fatalf("seed %d: card %d has an empty word", testseed, card.cardID)
			}

			if card.revealed {
				t.Fatalf("seed %d: card %d should be unrevealed", testseed, card.cardID)
			}
			counts[card.color]++

			if _, exists := seen[card.word]; exists {
				t.Fatalf("seed %d: duplicate word on board: %s", testseed, card.word)
			}
			seen[card.word] = struct{}{}
		}

		if (board.startingTeam == TeamRed && (counts[CardColorRed] != StartTeamCardsSize || counts[CardColorBlue] != OtherTeamCardsSize)) ||
			(board.startingTeam == TeamBlue && (counts[CardColorBlue] != StartTeamCardsSize || counts[CardColorRed] != OtherTeamCardsSize)) ||
			counts[CardColorNeutral] != NeutralCardsSize || counts[CardColorAssassin] != AssassinCardsSize {
			t.Fatalf("seed %d: unexpected card distribution", testseed)
		}
	}
}

func TestNewBoardWithDuplicateKey(t *testing.T) {
	wpDpKey := "wp_test/wp_duplicateKey.json"

	wordPack := loadTestWordPack(t, wpDpKey)
	seed := [32]byte{42}
	r := rand.New(rand.NewChaCha8(seed))

	_, err := NewBoard(wordPack.Words, "en", r)
	if err == nil {
		t.Fatal("expected an error, got none")
	}
	if !errors.Is(err, ErrInvalidWordPack) {
		t.Fatalf("\nwrong error: expected %v, got %v\n", ErrInvalidWordPack, err)
	}
	t.Logf("expected error received: %v", err)
}

func TestNewBoardDuplicateText(t *testing.T) {
	wpDpText := "wp_test/wp_duplicateText.json"

	wordPack := loadTestWordPack(t, wpDpText)
	seed := [32]byte{42}
	r := rand.New(rand.NewChaCha8(seed))

	_, err := NewBoard(wordPack.Words, "en", r)
	if err == nil {
		t.Fatal("expected an error, got none")
	}
	if !errors.Is(err, ErrNotEnoughWords) {
		t.Fatalf("\nwrong error: expected: %v, got: %v\n", ErrNotEnoughWords, err)
	}
	t.Logf("expected error received: %v", err)
}

func TestNewBoardWithNotEnoughWords(t *testing.T) {
	wpShort := "wp_test/wp_notEnoughWords.json"

	wordPack := loadTestWordPack(t, wpShort)
	seed := [32]byte{42}
	r := rand.New(rand.NewChaCha8(seed))

	_, err := NewBoard(wordPack.Words, "en", r)
	if err == nil {
		t.Fatal("expected an error, got none")
	}
	if !errors.Is(err, ErrNotEnoughWords) {
		t.Fatalf("\nwrong error: expected %v, got %v\n", ErrNotEnoughWords, err)
	}
	t.Logf("\nexpected error received: %v\n", err)
}

func TestNewBoardSkipsDuplicateTexts(t *testing.T) {
	wordPack := loadTestWordPack(t, "wp_test/wp_skipSameText.json")

	seed := [32]byte{42}
	for testseed := byte(1); testseed <= 50; testseed++ {
		seed[0] = testseed
		r := rand.New(rand.NewChaCha8(seed))

		board, err := NewBoard(wordPack.Words, "en", r)
		if err != nil {
			t.Fatalf("seed %d: unexpected error: %v", testseed, err)
		}

		cards := board.cards
		if len(cards) != BoardSize {
			t.Fatalf("seed %d: expected %d cards, got %d", testseed, BoardSize, len(cards))
		}

		seen := make(map[string]struct{}, BoardSize)
		for _, card := range cards {
			if _, exists := seen[card.word]; exists {
				t.Fatalf("seed %d: duplicate word on board: %s", testseed, card.word)
			}
			seen[card.word] = struct{}{}
		}
	}
}

func TestDiffSeedDiffBoard(t *testing.T) {
	seed1 := [32]byte{42}
	seed2 := [32]byte{24}
	r1 := rand.New(rand.NewChaCha8(seed1))
	r2 := rand.New(rand.NewChaCha8(seed2))

	wordPack := loadTestWordPack(t, "wordpack.json")
	board1, err := NewBoard(wordPack.Words, "en", r1)
	if err != nil {
		t.Fatalf("NewBoard returned an error: %v", err)
	}

	board2, err := NewBoard(wordPack.Words, "en", r2)
	if err != nil {
		t.Fatalf("NewBoard returned an error: %v", err)
	}

	if reflect.DeepEqual(board1, board2) {
		t.Errorf("Boards generated with different seeds are equal")
	}
}

func TestCardIDDoesNotRevealColors(t *testing.T) {
	wordPack := loadTestWordPack(t, "wordpack.json")
	var seed [32]byte

	colorsByCardID := make(map[int]map[CardColor]struct{}, BoardSize)

	for testSeed := byte(1); testSeed <= 200; testSeed++ {
		seed[0] = testSeed

		r := rand.New(rand.NewChaCha8(seed))
		board, err := NewBoard(wordPack.Words, "en", r)
		if err != nil {
			t.Fatalf("seed %d: NewBoard returned an error: %v", testSeed, err)
		}
		for _, card := range board.cards {
			if colorsByCardID[card.cardID] == nil {
				colorsByCardID[card.cardID] = make(map[CardColor]struct{})
			}
			colorsByCardID[card.cardID][card.color] = struct{}{}
		}
	}

	for cardID := 1; cardID <= BoardSize; cardID++ {
		if len(colorsByCardID[cardID]) < 4 {
			t.Fatalf(
				"CardID %d did not receive all card colors across 200 seeds",
				cardID,
			)
		}
	}
}
