package game

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math/rand/v2"
)

type Word struct {
	Key          string            `json:"key"`
	Translations map[string]string `json:"translations"`
}

type WordPack struct {
	Words []Word `json:"words"`
}

//go:embed wordpack.json
var defaultWordPackJSON []byte

func DefaultWordPack() (WordPack, error) {
	var pack WordPack
	if err := json.Unmarshal(defaultWordPackJSON, &pack); err != nil {
		return WordPack{}, err
	}
	return pack, nil
}

func validateWordPool(pool []Word, language string) error {
	if len(pool) < BoardSize {
		return fmt.Errorf("%w: have %d, need %d", ErrNotEnoughWords, len(pool), BoardSize)
	}

	keys := make(map[string]struct{}, len(pool))

	for _, word := range pool {
		if word.Key == "" {
			return fmt.Errorf("%w: empty key", ErrInvalidWordPack)
		}
		if _, exists := keys[word.Key]; exists {
			return fmt.Errorf("%w: duplicate key %q", ErrInvalidWordPack, word.Key)
		}
		keys[word.Key] = struct{}{}

		text := word.Translations[language]
		if text == "" {
			return fmt.Errorf("%w: key %q has no %q translation", ErrInvalidWordPack, word.Key, language)
		}
	}

	return nil
}

func pickWords(pool []Word, language string, r *rand.Rand) ([]Word, error) {
	indexes := r.Perm(len(pool))
	seen := make(map[string]struct{}, BoardSize)
	selectedWords := make([]Word, 0, BoardSize)

	for _, index := range indexes {
		word := pool[index]
		text := word.Translations[language]
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		selectedWords = append(selectedWords, word)

		if len(selectedWords) == BoardSize {
			return selectedWords, nil
		}
	}

	return nil, fmt.Errorf("%w, only %d unique texts for %q", ErrNotEnoughWords, len(selectedWords), language)
}

func randomTeam(r *rand.Rand) Team {
	if r.IntN(2) == 0 {
		return TeamRed
	}
	return TeamBlue
}

func appendColors(colors []CardColor, color CardColor, count int) []CardColor {
	for i := 0; i < count; i++ {
		colors = append(colors, color)
	}
	return colors
}

func buildColors(startingTeam Team, r *rand.Rand) []CardColor {
	startColor := CardColorRed
	otherColor := CardColorBlue

	if startingTeam == TeamBlue {
		startColor = CardColorBlue
		otherColor = CardColorRed
	}

	colors := make([]CardColor, 0, BoardSize)

	colors = appendColors(colors, startColor, StartTeamCardsSize)
	colors = appendColors(colors, otherColor, OtherTeamCardsSize)
	colors = appendColors(colors, CardColorNeutral, NeutralCardsSize)
	colors = appendColors(colors, CardColorAssassin, AssassinCardsSize)

	r.Shuffle(len(colors), func(i, j int) {
		colors[i], colors[j] = colors[j], colors[i]
	})

	return colors
}

func generateCards(wordPool []Word, language string, r *rand.Rand) ([]card, Team, error) {
	if err := validateWordPool(wordPool, language); err != nil {
		return nil, "", err
	}

	words, err := pickWords(wordPool, language, r)
	if err != nil {
		return nil, "", err
	}

	startingTeam := randomTeam(r)
	colors := buildColors(startingTeam, r)
	cards := make([]card, BoardSize)

	for i, word := range words {
		cards[i] = card{
			cardID:   i + 1,
			word:     word.Translations[language],
			color:    colors[i],
			revealed: false,
		}
	}
	return cards, startingTeam, nil
}
