package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Server is the central application struct.
type Server struct {
	cfg        Config
	db         *DB
	tokens     *TokenService
	firebase   *FirebaseAuth // nil if Firebase not configured
	sessions   *SessionStore
	matchmaker *Matchmaker
	bot        *Bot
}

func NewServer(cfg Config, db *DB, fb *FirebaseAuth) *Server {
	sessions := NewSessionStore()
	bot := &Bot{Depth: cfg.BotDepth}
	s := &Server{
		cfg:      cfg,
		db:       db,
		tokens:   NewTokenService(cfg.JWTSecret),
		firebase: fb,
		sessions: sessions,
		bot:      bot,
	}
	s.matchmaker = NewMatchmaker(sessions, newGameID)
	return s
}

// ─── JSON helpers ─────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decode(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// ─── Context helpers (proper implementation) ──────────────────────────────────

func withUserID(r *http.Request, userID, provider string) *http.Request {
	ctx := context.WithValue(r.Context(), ctxUserID, userID)
	ctx = context.WithValue(ctx, ctxProvider, provider)
	return r.WithContext(ctx)
}

func userIDFromCtx(r *http.Request) string {
	v, _ := r.Context().Value(ctxUserID).(string)
	return v
}

// ─── Auth Handlers ────────────────────────────────────────────────────────────

// POST /auth/guest
// Creates a guest user and returns a JWT.
func (s *Server) handleGuestLogin(w http.ResponseWriter, r *http.Request) {
	var req GuestLoginReq
	_ = decode(r, &req)

	name := strings.TrimSpace(req.DisplayName)
	if name == "" {
		name = randomGuestName()
	}

	userID := newUUID()
	user := &User{
		UserID:      userID,
		DisplayName: name,
		Provider:    ProviderGuest,
		Rating:      DefaultRating,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := s.db.CreateUser(user); err != nil {
		log.Printf("create guest user: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	token, err := s.tokens.Issue(userID, ProviderGuest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	writeJSON(w, http.StatusCreated, GuestLoginResp{
		Token:       token,
		UserID:      userID,
		DisplayName: name,
	})
}

// POST /auth/google
// Verifies a Firebase ID token. If guest_token is provided, merges the guest account.
func (s *Server) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if s.firebase == nil {
		writeError(w, http.StatusServiceUnavailable, "Google login not configured (set FIREBASE_PROJECT_ID)")
		return
	}

	var req GoogleLoginReq
	if err := decode(r, &req); err != nil || req.FirebaseIDToken == "" {
		writeError(w, http.StatusBadRequest, "firebase_id_token required")
		return
	}

	// Verify with Firebase
	tok, err := s.firebase.VerifyIDToken(req.FirebaseIDToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid Firebase token: "+err.Error())
		return
	}

	firebaseUID := tok.UID
	email, _ := tok.Claims["email"].(string)
	name, _ := tok.Claims["name"].(string)
	if name == "" {
		name = strings.Split(email, "@")[0]
	}

	merged := false
	var userID string

	// Check if guest token provided → merge guest account
	if req.GuestToken != "" {
		guestUserID, _, verifyErr := s.tokens.Verify(req.GuestToken)
		if verifyErr == nil && guestUserID != "" {
			// Merge: upgrade the guest account
			if err := s.db.MergeGuestToGoogle(guestUserID, firebaseUID, email, name); err != nil {
				log.Printf("merge guest: %v", err)
			} else {
				userID = guestUserID
				merged = true
			}
		}
	}

	if !merged {
		// Check if Firebase UID already exists
		existing, _ := s.db.GetUserByFirebaseUID(firebaseUID)
		if existing != nil {
			userID = existing.UserID
		} else {
			// New Google user
			userID = newUUID()
			u := &User{
				UserID:      userID,
				DisplayName: name,
				Email:       email,
				FirebaseUID: firebaseUID,
				Provider:    ProviderGoogle,
				Rating:      DefaultRating,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}
			if err := s.db.CreateUser(u); err != nil {
				log.Printf("create google user: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to create user")
				return
			}
		}
	}

	// Fetch final user for response
	user, _ := s.db.GetUserByID(userID)
	if user == nil {
		writeError(w, http.StatusInternalServerError, "user not found after auth")
		return
	}

	token, err := s.tokens.Issue(userID, ProviderGoogle)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token issue failed")
		return
	}

	writeJSON(w, http.StatusOK, GoogleLoginResp{
		Token:       token,
		UserID:      userID,
		DisplayName: user.DisplayName,
		Email:       user.Email,
		Merged:      merged,
	})
}

// GET /auth/me
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	user, err := s.db.GetUserByID(userID)
	if err != nil || user == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	  // Auto-refresh: if guest token expires within 7 days, silently issue a new one
    provider, _ := r.Context().Value(ctxProvider).(string)
    if provider == string(ProviderGuest) {
        if newToken, ok := s.tokens.MaybeRefresh(r.Header.Get("Authorization")); ok {
            w.Header().Set("X-New-Token", newToken)
        }
    }
	writeJSON(w, http.StatusOK, user)
}

// ─── Profile Handlers ─────────────────────────────────────────────────────────

// PUT /profile/name
func (s *Server) handleUpdateName(w http.ResponseWriter, r *http.Request) {
	var req UpdateNameReq
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(req.DisplayName)
	if len(name) < 2 || len(name) > 20 {
		writeError(w, http.StatusBadRequest, "display_name must be 2–20 characters")
		return
	}

	userID := userIDFromCtx(r)
	if err := s.db.UpdateDisplayName(userID, name); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update name")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"display_name": name})
}

// GET /leaderboard?limit=50
func (s *Server) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	entries, err := s.db.GetLeaderboard(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "leaderboard query failed")
		return
	}
	if entries == nil {
		entries = []LeaderboardEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"leaderboard": entries,
		"total":       len(entries),
	})
}

// ─── Matchmaking & Game Handlers ──────────────────────────────────────────────

// POST /matchmaking/join
// Adds user to the queue. Long-polls for up to 30 seconds waiting for a match.
func (s *Server) handleJoinMatchmaking(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	// Reject if already in a game
	if s.sessions.HasActiveGame(userID) {
		writeError(w, http.StatusConflict, "already in an active game; resign or finish first")
		return
	}

	ch, immediate := s.matchmaker.Join(userID)
	if immediate {
		sess := <-ch
		var color string
		if sess.WhiteID == userID {
			color = "white"
		} else {
			color = "black"
		}
		writeJSON(w, http.StatusOK, JoinMatchmakingResp{
			Status: "matched",
			GameID: sess.ID,
			Color:  color,
		})
		return
	}

	// Long-poll: wait up to 30 seconds
	select {
	case sess := <-ch:
		var color string
		if sess.WhiteID == userID {
			color = "white"
		} else {
			color = "black"
		}
		writeJSON(w, http.StatusOK, JoinMatchmakingResp{
			Status: "matched",
			GameID: sess.ID,
			Color:  color,
		})
	case <-time.After(30 * time.Second):
		// Still queued — client should call again
		writeJSON(w, http.StatusOK, JoinMatchmakingResp{
			Status:  "queued",
			Message: fmt.Sprintf("%d player(s) in queue", s.matchmaker.QueueLength()),
		})
	case <-r.Context().Done():
		s.matchmaker.Leave(userID)
	}
}

// POST /matchmaking/leave
func (s *Server) handleLeaveMatchmaking(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	s.matchmaker.Leave(userID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "left_queue"})
}

// POST /game/bot
func (s *Server) handleStartBotGame(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	if s.sessions.HasActiveGame(userID) {
		writeError(w, http.StatusConflict, "already in an active game")
		return
	}

	sess := s.matchmaker.CreateBotGame(userID, s.bot, newGameID)
	writeJSON(w, http.StatusCreated, StartBotGameResp{
		GameID: sess.ID,
		Color:  "white",
	})
}

// POST /game/:id/resign
func (s *Server) handleResign(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	gameID := strings.TrimPrefix(r.URL.Path, "/game/")
	gameID = strings.TrimSuffix(gameID, "/resign")

	sess, ok := s.sessions.Get(gameID)
	if !ok {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.Game.IsOver() {
		writeError(w, http.StatusBadRequest, "game already over")
		return
	}

	var winner string
	if sess.WhiteID == userID {
		winner = "black"
		sess.Game.Status = "black_wins"
	} else if sess.BlackID == userID {
		winner = "white"
		sess.Game.Status = "white_wins"
	} else {
		writeError(w, http.StatusForbidden, "not your game")
		return
	}

	// Notify via WS if connected
	sess.broadcast(WSMessage{
		Type: "game_over",
		Payload: map[string]string{"winner": winner, "reason": "resigned"},
	})

	go s.finalizeGame(sess)
	writeJSON(w, http.StatusOK, map[string]string{"winner": winner})
}

// GET /game/:id
func (s *Server) handleGetGame(w http.ResponseWriter, r *http.Request) {
	gameID := strings.TrimPrefix(r.URL.Path, "/game/")
	sess, ok := s.sessions.Get(gameID)
	if !ok {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}

	userID := userIDFromCtx(r)
	var color string
	if sess.WhiteID == userID {
		color = "white"
	} else {
		color = "black"
	}

	sess.mu.Lock()
	state := sess.stateFor(color)
	sess.mu.Unlock()

	writeJSON(w, http.StatusOK, state)
}

// ─── Game finalization (called after game ends) ───────────────────────────────

// finalizeGame updates ratings, saves the game record, and removes the session.
func (s *Server) finalizeGame(sess *GameSession) {
	winner := sess.Game.Winner()

	whiteUser, _ := s.db.GetUserByID(sess.WhiteID)
	var blackUser *User
	if !sess.IsBot {
		blackUser, _ = s.db.GetUserByID(sess.BlackID)
	}

	whiteRatingBefore := DefaultRating
	blackRatingBefore := DefaultRating
	if whiteUser != nil {
		whiteRatingBefore = whiteUser.Rating
	}
	if blackUser != nil {
		blackRatingBefore = blackUser.Rating
	}

	whiteRatingAfter := whiteRatingBefore
	blackRatingAfter := blackRatingBefore

	if !sess.IsBot && whiteUser != nil && blackUser != nil {
		elo := CalculateElo(
			whiteUser.Rating, blackUser.Rating,
			whiteUser.GamesPlayed, blackUser.GamesPlayed,
			winner,
		)
		whiteRatingAfter = elo.WhiteNew
		blackRatingAfter = elo.BlackNew

		var whiteResult, blackResult string
		switch winner {
		case "white":
			whiteResult, blackResult = "win", "loss"
		case "black":
			whiteResult, blackResult = "loss", "win"
		default:
			whiteResult, blackResult = "draw", "draw"
		}

		if err := s.db.UpdateRating(sess.WhiteID, whiteRatingAfter, whiteResult); err != nil {
			log.Printf("update white rating: %v", err)
		}
		if err := s.db.UpdateRating(sess.BlackID, blackRatingAfter, blackResult); err != nil {
			log.Printf("update black rating: %v", err)
		}

		// Send rating-change WS messages
		if sess.WhiteConn != nil {
			var delta int
			if whiteUser != nil {
				delta = whiteRatingAfter - whiteUser.Rating
			}
			_ = sess.WhiteConn.Send(WSMessage{
				Type: "rating_update",
				Payload: map[string]int{"delta": delta, "new_rating": whiteRatingAfter},
			})
		}
		if sess.BlackConn != nil {
			var delta int
			if blackUser != nil {
				delta = blackRatingAfter - blackUser.Rating
			}
			_ = sess.BlackConn.Send(WSMessage{
				Type: "rating_update",
				Payload: map[string]int{"delta": delta, "new_rating": blackRatingAfter},
			})
		}
	}

	blackIDForRecord := sess.BlackID
	if sess.IsBot {
		blackIDForRecord = "bot"
	}

	rec := &GameRecord{
		GameID:            sess.ID,
		WhiteUserID:       sess.WhiteID,
		BlackUserID:       blackIDForRecord,
		Winner:            winner,
		WhiteRatingBefore: whiteRatingBefore,
		BlackRatingBefore: blackRatingBefore,
		WhiteRatingAfter:  whiteRatingAfter,
		BlackRatingAfter:  blackRatingAfter,
		IsBotGame:         sess.IsBot,
		Moves:             sess.Game.MoveCount,
		CreatedAt:         time.Now(),
	}
	if err := s.db.SaveGameRecord(rec); err != nil {
		log.Printf("save game record: %v", err)
	}

	// Remove from session store after short delay (let WS messages drain)
	time.AfterFunc(5*time.Second, func() {
		s.sessions.Remove(sess.ID)
	})
}

// ─── Utilities ────────────────────────────────────────────────────────────────

func randomGuestName() string {
	adjectives := []string{"Swift", "Brave", "Clever", "Lucky", "Bold", "Sharp", "Quick", "Calm"}
	nouns := []string{"Pawn", "King", "Rook", "Knight", "Bishop", "Queen", "Player", "Mover"}
	return fmt.Sprintf("%s%s%d",
		adjectives[rand.Intn(len(adjectives))],
		nouns[rand.Intn(len(nouns))],
		rand.Intn(9999),
	)
}

func newUUID() string {
	// Simple UUID v4 using crypto/rand would be better; using time-based for simplicity here.
	// In production, use github.com/google/uuid
	return fmt.Sprintf("%x-%x-%x-%x",
		rand.Int63n(0xffffffff),
		rand.Int63n(0xffff),
		rand.Int63n(0xffff),
		time.Now().UnixNano(),
	)
}
