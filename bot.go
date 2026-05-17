package main

import "math/rand"

// Bot implements a minimax checkers AI with alpha-beta pruning.
type Bot struct {
	Depth int // search depth (3=easy, 4=medium, 5=hard)
}

// BestMove returns the best move for the current player using minimax.
func (bot *Bot) BestMove(g *CheckersGame) *Move {
	moves := g.LegalMoves()
	if len(moves) == 0 {
		return nil
	}

	maximizing := g.Turn == "white"
	bestScore := 0
	var bestMoves []Move

	for i, m := range moves {
		child := cloneGame(g)
		child.ApplyMove(m)
		score := bot.minimax(child, bot.Depth-1, -1<<30, 1<<30, !maximizing)

		if i == 0 {
			bestScore = score
			bestMoves = []Move{m}
		} else if (maximizing && score > bestScore) || (!maximizing && score < bestScore) {
			bestScore = score
			bestMoves = []Move{m}
		} else if score == bestScore {
			bestMoves = append(bestMoves, m)
		}
	}

	// Pick randomly among equally-scored moves for variety
	chosen := bestMoves[rand.Intn(len(bestMoves))]
	return &chosen
}

func (bot *Bot) minimax(g *CheckersGame, depth, alpha, beta int, maximizing bool) int {
	if depth == 0 || g.IsOver() {
		return g.Evaluate()
	}

	moves := g.LegalMoves()
	if len(moves) == 0 {
		return g.Evaluate()
	}

	if maximizing {
		best := -1 << 30
		for _, m := range moves {
			child := cloneGame(g)
			child.ApplyMove(m)
			score := bot.minimax(child, depth-1, alpha, beta, false)
			if score > best {
				best = score
			}
			if score > alpha {
				alpha = score
			}
			if beta <= alpha {
				break // β cut-off
			}
		}
		return best
	}

	best := 1 << 30
	for _, m := range moves {
		child := cloneGame(g)
		child.ApplyMove(m)
		score := bot.minimax(child, depth-1, alpha, beta, true)
		if score < best {
			best = score
		}
		if score < beta {
			beta = score
		}
		if beta <= alpha {
			break // α cut-off
		}
	}
	return best
}

func cloneGame(g *CheckersGame) *CheckersGame {
	ng := *g
	return &ng
}
