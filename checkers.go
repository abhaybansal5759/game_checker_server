package main

// ─── Board constants ──────────────────────────────────────────────────────────
//
// Board is 8×8. Pieces:
//   0 = empty
//   1 = white regular
//   2 = black regular
//   3 = white king
//   4 = black king
//
// White pieces start at rows 5–7 (bottom), black at rows 0–2 (top).
// White moves toward lower row indices; black moves toward higher row indices.
// Pieces only exist on dark squares (row+col is odd in our convention).

const (
	Empty      = 0
	WhiteReg   = 1
	BlackReg   = 2
	WhiteKing  = 3
	BlackKing  = 4
)

// isWhite / isBlack helpers
func isWhite(p int) bool { return p == WhiteReg || p == WhiteKing }
func isBlack(p int) bool { return p == BlackReg || p == BlackKing }
func isKing(p int) bool  { return p == WhiteKing || p == BlackKing }

func isEnemy(a, b int) bool {
	if a == Empty || b == Empty {
		return false
	}
	return isWhite(a) != isWhite(b)
}

// ─── Board ───────────────────────────────────────────────────────────────────

type Board [8][8]int

// NewBoard returns a board in the standard checkers start position.
func NewBoard() Board {
	var b Board
	for row := 0; row < 8; row++ {
		for col := 0; col < 8; col++ {
			if (row+col)%2 == 1 { // dark squares only
				if row < 3 {
					b[row][col] = BlackReg
				} else if row > 4 {
					b[row][col] = WhiteReg
				}
			}
		}
	}
	return b
}

// Clone returns a deep copy of the board.
func (b Board) Clone() Board { return b }

// ─── Move ────────────────────────────────────────────────────────────────────

type Move struct {
	FromRow, FromCol int
	ToRow, ToCol     int
	// For multi-jump, JumpedSquares holds the intermediate captured squares.
	JumpedSquares [][2]int
}

func (m Move) IsJump() bool { return len(m.JumpedSquares) > 0 }

// ─── Game state ───────────────────────────────────────────────────────────────

type CheckersGame struct {
	Board     Board
	Turn      string // "white" | "black"
	Status    string // "in_progress" | "white_wins" | "black_wins" | "draw"
	MoveCount int
	// 40-move rule: if no capture in 40 moves → draw
	NoCaptureMoves int
}

func NewCheckersGame() *CheckersGame {
	return &CheckersGame{
		Board:  NewBoard(),
		Turn:   "white",
		Status: "in_progress",
	}
}

// ─── Move generation ─────────────────────────────────────────────────────────

// LegalMoves returns all legal moves for the current player.
// Forced-capture rule: if any jump is available, only jumps are returned.
func (g *CheckersGame) LegalMoves() []Move {
	jumps := g.allJumps(g.Turn)
	if len(jumps) > 0 {
		return jumps
	}
	return g.allSimpleMoves(g.Turn)
}

func (g *CheckersGame) allSimpleMoves(color string) []Move {
	var moves []Move
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			p := g.Board[r][c]
			if p == Empty {
				continue
			}
			if (color == "white" && !isWhite(p)) || (color == "black" && !isBlack(p)) {
				continue
			}
			dirs := dirsFor(p)
			for _, d := range dirs {
				nr, nc := r+d[0], c+d[1]
				if inBounds(nr, nc) && g.Board[nr][nc] == Empty {
					moves = append(moves, Move{FromRow: r, FromCol: c, ToRow: nr, ToCol: nc})
				}
			}
		}
	}
	return moves
}

func (g *CheckersGame) allJumps(color string) []Move {
	var jumps []Move
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			p := g.Board[r][c]
			if p == Empty {
				continue
			}
			if (color == "white" && !isWhite(p)) || (color == "black" && !isBlack(p)) {
				continue
			}
			b := g.Board
			jumps = append(jumps, g.jumpSequences(b, r, c, p, nil)...)
		}
	}
	return jumps
}

// jumpSequences recursively finds all multi-jump paths from (r,c).
func (g *CheckersGame) jumpSequences(b Board, r, c, piece int, soFar [][2]int) []Move {
	dirs := dirsFor(piece)
	var found []Move
	anyJump := false

	for _, d := range dirs {
		mr, mc := r+d[0], c+d[1] // middle (enemy)
		nr, nc := r+2*d[0], c+2*d[1] // landing
		if !inBounds(mr, mc) || !inBounds(nr, nc) {
			continue
		}
		mid := b[mr][mc]
		if !isEnemy(piece, mid) {
			continue
		}
		if b[nr][nc] != Empty {
			continue
		}
		// Check we haven't already jumped this square in this sequence
		if alreadyJumped(soFar, mr, mc) {
			continue
		}

		anyJump = true
		// Apply jump on copy
		nb := b
		nb[nr][nc] = piece
		nb[r][c] = Empty
		nb[mr][mc] = Empty
		// King promotion mid-jump (piece can continue as king)
		promotedPiece := piece
		if (piece == WhiteReg && nr == 0) || (piece == BlackReg && nr == 7) {
			if piece == WhiteReg {
				promotedPiece = WhiteKing
			} else {
				promotedPiece = BlackKing
			}
			nb[nr][nc] = promotedPiece
		}

		jumped := append(append([][2]int{}, soFar...), [2]int{mr, mc})

		// Try to continue jumping
		continuations := g.jumpSequences(nb, nr, nc, promotedPiece, jumped)
		if len(continuations) > 0 {
			found = append(found, continuations...)
		} else {
			// Terminal jump
			found = append(found, Move{
				FromRow:       r,
				FromCol:       c,
				ToRow:         nr,
				ToCol:         nc,
				JumpedSquares: jumped,
			})
		}
	}
	_ = anyJump
	return found
}

func alreadyJumped(jumped [][2]int, r, c int) bool {
	for _, sq := range jumped {
		if sq[0] == r && sq[1] == c {
			return true
		}
	}
	return false
}

// dirsFor returns the legal movement directions for a piece.
func dirsFor(p int) [][2]int {
	switch p {
	case WhiteReg:
		return [][2]int{{-1, -1}, {-1, 1}} // white moves up (decreasing row)
	case BlackReg:
		return [][2]int{{1, -1}, {1, 1}}   // black moves down
	default: // kings
		return [][2]int{{-1, -1}, {-1, 1}, {1, -1}, {1, 1}}
	}
}

func inBounds(r, c int) bool { return r >= 0 && r < 8 && c >= 0 && c < 8 }

// ─── Apply move ───────────────────────────────────────────────────────────────

// ApplyMove applies a move (must be legal) and updates game state.
// Returns error string if illegal; empty string on success.
func (g *CheckersGame) ApplyMove(m Move) string {
	legal := g.LegalMoves()
	if !matchesAny(m, legal) {
		return "illegal move"
	}

	piece := g.Board[m.FromRow][m.FromCol]
	g.Board[m.FromRow][m.FromCol] = Empty

	// Remove jumped pieces
	for _, sq := range m.JumpedSquares {
		g.Board[sq[0]][sq[1]] = Empty
	}

	// Place piece at destination
	g.Board[m.ToRow][m.ToCol] = piece

	// King promotion
	if piece == WhiteReg && m.ToRow == 0 {
		g.Board[m.ToRow][m.ToCol] = WhiteKing
	} else if piece == BlackReg && m.ToRow == 7 {
		g.Board[m.ToRow][m.ToCol] = BlackKing
	}

	// Track no-capture moves for draw rule
	if m.IsJump() {
		g.NoCaptureMoves = 0
	} else {
		g.NoCaptureMoves++
	}
	g.MoveCount++

	// Switch turn
	if g.Turn == "white" {
		g.Turn = "black"
	} else {
		g.Turn = "white"
	}

	// Check terminal conditions
	g.updateStatus()
	return ""
}

// matchesAny checks if move m matches any in the legal list (ignoring jump ordering details).
func matchesAny(m Move, legal []Move) bool {
	for _, l := range legal {
		if l.FromRow == m.FromRow && l.FromCol == m.FromCol &&
			l.ToRow == m.ToRow && l.ToCol == m.ToCol {
			return true
		}
	}
	return false
}

// updateStatus checks for win/draw conditions.
func (g *CheckersGame) updateStatus() {
	// 40-move rule (no captures)
	if g.NoCaptureMoves >= 80 { // 80 half-moves = 40 full moves each
		g.Status = "draw"
		return
	}

	moves := g.LegalMoves()
	if len(moves) == 0 {
		// Current player has no moves → they lose
		if g.Turn == "white" {
			g.Status = "black_wins"
		} else {
			g.Status = "white_wins"
		}
		return
	}

	// Check if any pieces remain
	whites, blacks := 0, 0
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			if isWhite(g.Board[r][c]) {
				whites++
			} else if isBlack(g.Board[r][c]) {
				blacks++
			}
		}
	}
	if whites == 0 {
		g.Status = "black_wins"
	} else if blacks == 0 {
		g.Status = "white_wins"
	}
}

func (g *CheckersGame) IsOver() bool {
	return g.Status != "in_progress"
}

func (g *CheckersGame) Winner() string {
	switch g.Status {
	case "white_wins":
		return "white"
	case "black_wins":
		return "black"
	case "draw":
		return "draw"
	}
	return ""
}

// ─── Evaluation (for bot) ────────────────────────────────────────────────────

// Evaluate returns a score from white's perspective.
func (g *CheckersGame) Evaluate() int {
	if g.Status == "white_wins" {
		return 10000
	}
	if g.Status == "black_wins" {
		return -10000
	}
	if g.Status == "draw" {
		return 0
	}

	score := 0
	for r := 0; r < 8; r++ {
		for c := 0; c < 8; c++ {
			switch g.Board[r][c] {
			case WhiteReg:
				score += 100
				// Advancement bonus
				score += (7 - r) * 3
				// Center control
				if c >= 2 && c <= 5 {
					score += 5
				}
			case WhiteKing:
				score += 175
				// Center bonus for kings
				if r >= 2 && r <= 5 && c >= 2 && c <= 5 {
					score += 10
				}
			case BlackReg:
				score -= 100
				score -= r * 3
				if c >= 2 && c <= 5 {
					score -= 5
				}
			case BlackKing:
				score -= 175
				if r >= 2 && r <= 5 && c >= 2 && c <= 5 {
					score -= 10
				}
			}
		}
	}
	return score
}
