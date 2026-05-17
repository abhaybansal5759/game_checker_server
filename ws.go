package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	CheckOrigin: func(r *http.Request) bool {
		// In production, restrict to your frontend domain:
		// origin := r.Header.Get("Origin")
		// return origin == "https://yourdomain.com"
		return true
	},
}

// GET /ws?token=<jwt>&game_id=<id>
// Upgrades to WebSocket for real-time game play.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// Auth (token from query param since WS can't set headers easily)
	tokenStr := r.URL.Query().Get("token")
	userID, _, err := s.tokens.Verify(tokenStr)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	gameID := r.URL.Query().Get("game_id")
	sess, ok := s.sessions.Get(gameID)
	if !ok {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	// Determine player color
	var playerColor string
	if sess.WhiteID == userID {
		playerColor = "white"
	} else if sess.BlackID == userID {
		playerColor = "black"
	} else {
		http.Error(w, "you are not a participant in this game", http.StatusForbidden)
		return
	}

	// Upgrade connection
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	pc := newPlayerConn(conn, s.cfg.WSMsgRatePerSec, s.cfg.WSMsgBurstSize)

	// Register connection in session
	sess.mu.Lock()
	if playerColor == "white" {
		sess.WhiteConn = pc
	} else {
		sess.BlackConn = pc
	}

	// Send current state immediately on connect
	state := sess.stateFor(playerColor)
	sess.mu.Unlock()

	_ = pc.Send(WSMessage{Type: "state", Payload: state})

	// If bot game and it's bot's turn, trigger immediately
	if sess.IsBot && sess.Game.Turn == "black" {
		go s.doBotMove(sess)
	}

	// Read loop
	s.wsReadLoop(pc, sess, userID, playerColor)
}

// wsReadLoop reads messages from the player's WebSocket connection.
func (s *Server) wsReadLoop(pc *PlayerConn, sess *GameSession, userID, color string) {
	defer func() {
		pc.Close()
		sess.mu.Lock()
		if color == "white" && sess.WhiteConn == pc {
			sess.WhiteConn = nil
		} else if color == "black" && sess.BlackConn == pc {
			sess.BlackConn = nil
		}
		sess.mu.Unlock()
	}()

	pc.conn.SetReadDeadline(time.Now().Add(60 * time.Minute))
	pc.conn.SetPongHandler(func(string) error {
		pc.conn.SetReadDeadline(time.Now().Add(60 * time.Minute))
		return nil
	})

	// Ping ticker
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			pc.mu.Lock()
			err := pc.conn.WriteMessage(websocket.PingMessage, nil)
			pc.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	for {
		_, raw, err := pc.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("ws read error [%s]: %v", userID, err)
			}
			return
		}

		// Rate limit incoming messages
		if !pc.Allow() {
			_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "message rate limit exceeded"}})
			continue
		}

		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "invalid message format"}})
			continue
		}

		switch msg.Type {
		case "move":
			s.handleWSMove(pc, sess, userID, color, msg.Payload)

		case "resign":
			sess.mu.Lock()
			if !sess.Game.IsOver() {
				var winner string
				if color == "white" {
					winner = "black"
					sess.Game.Status = "black_wins"
				} else {
					winner = "white"
					sess.Game.Status = "white_wins"
				}
				sess.mu.Unlock()

				sess.broadcast(WSMessage{
					Type:    "game_over",
					Payload: GameOverPayload{Winner: winner, Reason: "resigned"},
				})
				go s.finalizeGame(sess)
			} else {
				sess.mu.Unlock()
			}

		case "ping":
			_ = pc.Send(WSMessage{Type: "pong"})

		default:
			_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "unknown message type: " + msg.Type}})
		}
	}
}

// handleWSMove processes a move message from a player.
func (s *Server) handleWSMove(pc *PlayerConn, sess *GameSession, userID, color string, rawPayload json.RawMessage) {
	var mv MovePayload
	if err := json.Unmarshal(rawPayload, &mv); err != nil {
		_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "invalid move payload"}})
		return
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.Game.IsOver() {
		_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "game is already over"}})
		return
	}
	if sess.Game.Turn != color {
		_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "not your turn"}})
		return
	}

	move := Move{
		FromRow: mv.FromRow,
		FromCol: mv.FromCol,
		ToRow:   mv.ToRow,
		ToCol:   mv.ToCol,
	}

	// Fill in JumpedSquares from legal moves (client doesn't need to send them)
	legal := sess.Game.LegalMoves()
	for _, lm := range legal {
		if lm.FromRow == move.FromRow && lm.FromCol == move.FromCol &&
			lm.ToRow == move.ToRow && lm.ToCol == move.ToCol {
			move.JumpedSquares = lm.JumpedSquares
			break
		}
	}

	errStr := sess.Game.ApplyMove(move)
	if errStr != "" {
		_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: errStr}})
		return
	}

	// Broadcast new state to both players
	lastMoveInfo := &MoveInfo{
		FromRow: mv.FromRow,
		FromCol: mv.FromCol,
		ToRow:   mv.ToRow,
		ToCol:   mv.ToCol,
		Jumped:  len(move.JumpedSquares) > 0,
	}

	whiteState := sess.stateFor("white")
	whiteState.LastMove = lastMoveInfo
	blackState := sess.stateFor("black")
	blackState.LastMove = lastMoveInfo

	if sess.WhiteConn != nil {
		_ = sess.WhiteConn.Send(WSMessage{Type: "state", Payload: whiteState})
	}
	if !sess.IsBot && sess.BlackConn != nil {
		_ = sess.BlackConn.Send(WSMessage{Type: "state", Payload: blackState})
	}

	// Check game over
	if sess.Game.IsOver() {
		winner := sess.Game.Winner()
		overMsg := WSMessage{
			Type:    "game_over",
			Payload: GameOverPayload{Winner: winner, Reason: reasonFor(sess.Game.Status)},
		}
		sess.broadcast(overMsg)
		go s.finalizeGame(sess)
		return
	}

	// If bot game and now bot's turn, trigger bot move
	if sess.IsBot && sess.Game.Turn == "black" {
		go s.doBotMove(sess)
	}
}

// doBotMove computes and applies the bot's move, then broadcasts the result.
func (s *Server) doBotMove(sess *GameSession) {
	// Small delay so it doesn't feel instant
	time.Sleep(500 * time.Millisecond)

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.Game.IsOver() || sess.Game.Turn != "black" {
		return
	}

	botMove := sess.BotEngine.BestMove(sess.Game)
	if botMove == nil {
		return
	}

	sess.Game.ApplyMove(*botMove)

	state := sess.stateFor("white")
	state.LastMove = &MoveInfo{
		FromRow: botMove.FromRow,
		FromCol: botMove.FromCol,
		ToRow:   botMove.ToRow,
		ToCol:   botMove.ToCol,
		Jumped:  len(botMove.JumpedSquares) > 0,
	}

	if sess.WhiteConn != nil {
		_ = sess.WhiteConn.Send(WSMessage{Type: "state", Payload: state})
	}

	if sess.Game.IsOver() {
		winner := sess.Game.Winner()
		if sess.WhiteConn != nil {
			_ = sess.WhiteConn.Send(WSMessage{
				Type:    "game_over",
				Payload: GameOverPayload{Winner: winner, Reason: reasonFor(sess.Game.Status)},
			})
		}
		go s.finalizeGame(sess)
	}
}

func reasonFor(status string) string {
	if status == "draw" {
		return "no_captures_rule"
	}
	return "no_moves"
}
