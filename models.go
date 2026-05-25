package main

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ─── Auth ────────────────────────────────────────────────────────────────────

type AuthProvider string

const (
	ProviderGuest  AuthProvider = "guest"
	ProviderGoogle AuthProvider = "google"
)

// User is stored in MongoDB collection "users".
// user_id is a stable UUID we generate — it stays the same even after
// a guest upgrades to Google login.
type User struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"      json:"id"`
	UserID      string             `bson:"user_id"            json:"user_id"`      // stable public ID (UUID)
	DisplayName string             `bson:"display_name"       json:"display_name"`
	Email       string             `bson:"email,omitempty"    json:"email,omitempty"`
	FirebaseUID string             `bson:"firebase_uid,omitempty" json:"-"`       // set after Google login
	Provider    AuthProvider       `bson:"provider"           json:"provider"`
	Rating      int                `bson:"rating"             json:"rating"`
	GamesPlayed int                `bson:"games_played"       json:"games_played"`
	Wins        int                `bson:"wins"               json:"wins"`
	Losses      int                `bson:"losses"             json:"losses"`
	Draws       int                `bson:"draws"              json:"draws"`
	CreatedAt   time.Time          `bson:"created_at"         json:"created_at"`
	UpdatedAt   time.Time          `bson:"updated_at"         json:"updated_at"`

	// Transient (not persisted): set by handlers from the in-memory session store.
	ActiveGameID    string `bson:"-" json:"active_game_id"`
	ActiveGameIsBot bool   `bson:"-" json:"active_game_is_bot"`
}

// ─── JWT Claims ──────────────────────────────────────────────────────────────

type JWTClaims struct {
	UserID   string       `json:"user_id"`
	Provider AuthProvider `json:"provider"`
	// standard fields added by jwt library (exp, iat)
}

// ─── Game Records ────────────────────────────────────────────────────────────

// GameRecord is written to MongoDB when a game finishes.
type GameRecord struct {
	ID                primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	GameID            string             `bson:"game_id"        json:"game_id"`
	WhiteUserID       string             `bson:"white_user_id"  json:"white_user_id"`
	BlackUserID       string             `bson:"black_user_id"  json:"black_user_id"`
	Winner            string             `bson:"winner"         json:"winner"` // "white","black","draw"
	WhiteRatingBefore int                `bson:"white_rb"       json:"white_rating_before"`
	BlackRatingBefore int                `bson:"black_rb"       json:"black_rating_before"`
	WhiteRatingAfter  int                `bson:"white_ra"       json:"white_rating_after"`
	BlackRatingAfter  int                `bson:"black_ra"       json:"black_rating_after"`
	IsBotGame         bool               `bson:"is_bot_game"    json:"is_bot_game"`
	Moves             int                `bson:"moves"          json:"moves"`
	CreatedAt         time.Time          `bson:"created_at"     json:"created_at"`
}

// ─── Leaderboard ─────────────────────────────────────────────────────────────

type LeaderboardEntry struct {
	Rank        int    `json:"rank"`
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Rating      int    `json:"rating"`
	GamesPlayed int    `json:"games_played"`
	Wins        int    `json:"wins"`
	Losses      int    `json:"losses"`
}

// ─── HTTP Request/Response shapes ────────────────────────────────────────────

type GuestLoginReq struct {
	DisplayName string `json:"display_name"` // optional; auto-generated if empty
}

type GuestLoginResp struct {
	Token           string `json:"token"`
	UserID          string `json:"user_id"`
	DisplayName     string `json:"display_name"`
	ActiveGameID    string `json:"active_game_id"`
	ActiveGameIsBot bool   `json:"active_game_is_bot"`
}

type GoogleLoginReq struct {
	FirebaseIDToken string `json:"firebase_id_token"` // from Firebase client SDK
	GuestToken      string `json:"guest_token"`        // optional: merge guest→google
}

type GoogleLoginResp struct {
	Token           string `json:"token"`
	UserID          string `json:"user_id"`
	DisplayName     string `json:"display_name"`
	Email           string `json:"email"`
	Merged          bool   `json:"merged"` // true if guest account was merged
	ActiveGameID    string `json:"active_game_id"`
	ActiveGameIsBot bool   `json:"active_game_is_bot"`
}

type UpdateNameReq struct {
	DisplayName string `json:"display_name"`
}

type JoinMatchmakingResp struct {
	Status  string `json:"status"`  // "queued" | "matched"
	GameID  string `json:"game_id,omitempty"`
	Color   string `json:"color,omitempty"` // "white"|"black"
	Message string `json:"message,omitempty"`
}

type StartBotGameResp struct {
	GameID string `json:"game_id"`
	Color  string `json:"color"` // player always gets white vs bot
}

// ─── WebSocket messages ───────────────────────────────────────────────────────

type WSMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
}

// Inbound from client
type MovePayload struct {
	FromRow int `json:"from_row"`
	FromCol int `json:"from_col"`
	ToRow   int `json:"to_row"`
	ToCol   int `json:"to_col"`
}

// Outbound state update
type GameStatePayload struct {
	GameID    string     `json:"game_id"`
	Board     [8][8]int  `json:"board"`
	Turn      string     `json:"turn"` // "white"|"black"
	Status    string     `json:"status"`
	LastMove  *MoveInfo  `json:"last_move,omitempty"`
	YourColor string     `json:"your_color"`
}

type MoveInfo struct {
	FromRow int    `json:"from_row"`
	FromCol int    `json:"from_col"`
	ToRow   int    `json:"to_row"`
	ToCol   int    `json:"to_col"`
	Jumped  bool   `json:"jumped"`
}

type GameOverPayload struct {
	Winner       string `json:"winner"`        // "white","black","draw"
	Reason       string `json:"reason"`        // "no_moves","resigned","draw_agreed"
	RatingChange int    `json:"rating_change"` // from this player's perspective
	NewRating    int    `json:"new_rating"`
}

type MatchedPayload struct {
	GameID    string `json:"game_id"`
	YourColor string `json:"your_color"`
	Opponent  string `json:"opponent_name"`
}

type ErrorPayload struct {
	Message string `json:"message"`
}
