package main

import "math"

const (
	DefaultRating = 1200
	// K-factor tiers (like chess.com)
	kFactorNewPlayer  = 40 // < 30 games
	kFactorMidPlayer  = 20 // 30–100 games
	kFactorHighRating = 10 // rating ≥ 2400
	kFactorNormal     = 20
)

// kFactor returns the K-factor for a player given their stats.
func kFactor(gamesPlayed, rating int) float64 {
	if gamesPlayed < 30 {
		return kFactorNewPlayer
	}
	if rating >= 2400 {
		return kFactorHighRating
	}
	return kFactorNormal
}

// expectedScore calculates the expected score for player A against player B.
// Returns a value between 0 and 1.
func expectedScore(ratingA, ratingB int) float64 {
	return 1.0 / (1.0 + math.Pow(10, float64(ratingB-ratingA)/400.0))
}

// EloResult holds the updated ratings after a game.
type EloResult struct {
	WhiteNew int
	BlackNew int
	WhiteDelta int
	BlackDelta int
}

// CalculateElo computes new ratings after a game.
// winner: "white", "black", or "draw"
func CalculateElo(whiteRating, blackRating, whiteGames, blackGames int, winner string) EloResult {
	kW := kFactor(whiteGames, whiteRating)
	kB := kFactor(blackGames, blackRating)

	expectedW := expectedScore(whiteRating, blackRating)
	expectedB := expectedScore(blackRating, whiteRating)

	var scoreW, scoreB float64
	switch winner {
	case "white":
		scoreW, scoreB = 1.0, 0.0
	case "black":
		scoreW, scoreB = 0.0, 1.0
	default: // draw
		scoreW, scoreB = 0.5, 0.5
	}

	deltaW := int(math.Round(kW * (scoreW - expectedW)))
	deltaB := int(math.Round(kB * (scoreB - expectedB)))

	newW := maxInt(100, whiteRating+deltaW) // floor at 100 so rating never goes negative
	newB := maxInt(100, blackRating+deltaB)

	return EloResult{
		WhiteNew:   newW,
		BlackNew:   newB,
		WhiteDelta: newW - whiteRating,
		BlackDelta: newB - blackRating,
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
