package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ─── Chat Message Model ───────────────────────────────────────────────────────

type ChatMessage struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	GameID      string             `bson:"game_id" json:"game_id"`
	SenderID    string             `bson:"sender_id" json:"sender_id"`
	SenderColor string             `bson:"sender_color" json:"sender_color"` // "white" | "black"
	Text        string             `bson:"text" json:"text"`
	CreatedAt   time.Time          `bson:"created_at" json:"created_at"`
}

// ─── Chat WebSocket Payload ───────────────────────────────────────────────────

type ChatPayload struct {
	GameID   string `json:"game_id"`
	SenderID string `json:"sender_id"`
	Text     string `json:"text"`
}

type ChatMessageOut struct {
	SenderColor string    `json:"sender_color"`
	Text        string    `json:"text"`
	CreatedAt   time.Time `json:"created_at"`
}

// ─── Database Methods ─────────────────────────────────────────────────────────

// SaveChatMessage saves a chat message to MongoDB
func (db *DB) SaveChatMessage(msg *ChatMessage) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msg.CreatedAt = time.Now()
	_, err := db.client.Database(db.dbName).Collection("messages").InsertOne(ctx, msg)
	return err
}

// GetChatMessages retrieves all chat messages for a game, sorted by created_at
func (db *DB) GetChatMessages(gameID string) ([]ChatMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}})
	cursor, err := db.client.Database(db.dbName).Collection("messages").Find(
		ctx,
		bson.M{"game_id": gameID},
		opts,
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var messages []ChatMessage
	if err := cursor.All(ctx, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// DeleteChatMessages deletes all chat messages for a game
func (db *DB) DeleteChatMessages(gameID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := db.client.Database(db.dbName).Collection("messages").DeleteMany(
		ctx,
		bson.M{"game_id": gameID},
	)
	return err
}

// EnsureChatIndexes creates compound index on game_id + created_at
func (db *DB) EnsureChatIndexes() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	coll := db.client.Database(db.dbName).Collection("messages")
	
	// Compound index for fast retrieval
	indexModel1 := mongo.IndexModel{
		Keys: bson.D{
			{Key: "game_id", Value: 1},
			{Key: "created_at", Value: 1},
		},
	}
	
	// TTL index - auto-delete messages after 3 hours (safety net)
	indexModel2 := mongo.IndexModel{
		Keys: bson.D{{Key: "created_at", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(10800), // 3 hours
	}
	
	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{indexModel1, indexModel2})
	return err
}

// ─── HTTP Handler: GET /api/game/:gameId/messages ─────────────────────────────

func (s *Server) handleGetChatMessages(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)
	gameID := strings.TrimPrefix(r.URL.Path, "/game/")
	gameID = strings.TrimSuffix(gameID, "/messages")

	// Verify user is a participant
	sess, ok := s.sessions.Get(gameID)
	if !ok {
		writeError(w, http.StatusNotFound, "game not found")
		return
	}

	if sess.WhiteID != userID && sess.BlackID != userID {
		writeError(w, http.StatusForbidden, "you are not a participant in this game")
		return
	}

	messages, err := s.db.GetChatMessages(gameID)
	if err != nil {
		log.Printf("get chat messages: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to load messages")
		return
	}

	// Convert to output format (hide sender_id, only show sender_color)
	out := make([]ChatMessageOut, len(messages))
	for i, msg := range messages {
		out[i] = ChatMessageOut{
			SenderColor: msg.SenderColor,
			Text:        msg.Text,
			CreatedAt:   msg.CreatedAt,
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"messages": out,
	})
}

// ─── WebSocket Chat Handler (multiplexed over existing WS) ────────────────────

// Rate limiter for chat messages (per user per game)
var chatRateLimiters = make(map[string]*time.Ticker)

func (s *Server) handleChatMessage(pc *PlayerConn, sess *GameSession, userID, color string, rawPayload json.RawMessage) {
	var payload ChatPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "invalid chat payload"}})
		return
	}

	// Validate sender is a participant
	if sess.WhiteID != userID && sess.BlackID != userID {
		_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "not a participant"}})
		return
	}

	// Rate limit: max 2 messages per second
	rateLimitKey := userID + ":" + sess.ID
	if ticker, exists := chatRateLimiters[rateLimitKey]; exists {
		select {
		case <-ticker.C:
			// Allow message
		default:
			_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "chat rate limit exceeded"}})
			return
		}
	} else {
		chatRateLimiters[rateLimitKey] = time.NewTicker(500 * time.Millisecond) // 2 per second
	}

	// Validate message length
	text := strings.TrimSpace(payload.Text)
	if len(text) == 0 || len(text) > 200 {
		_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "message must be 1-200 characters"}})
		return
	}

	// Save to database
	msg := &ChatMessage{
		GameID:      sess.ID,
		SenderID:    userID,
		SenderColor: color,
		Text:        text,
		CreatedAt:   time.Now(),
	}

	if err := s.db.SaveChatMessage(msg); err != nil {
		log.Printf("save chat message: %v", err)
		_ = pc.Send(WSMessage{Type: "error", Payload: ErrorPayload{Message: "failed to save message"}})
		return
	}

	// Broadcast to both players (only sender_color exposed, not sender_id)
	outMsg := ChatMessageOut{
		SenderColor: color,
		Text:        text,
		CreatedAt:   msg.CreatedAt,
	}

	sess.mu.Lock()
	if sess.WhiteConn != nil {
		_ = sess.WhiteConn.Send(WSMessage{Type: "chat", Payload: outMsg})
	}
	if sess.BlackConn != nil {
		_ = sess.BlackConn.Send(WSMessage{Type: "chat", Payload: outMsg})
	}
	sess.mu.Unlock()
}

// ─── Cleanup on Game End ──────────────────────────────────────────────────────

func (s *Server) cleanupChatMessages(gameID string) {
	go func() {
		if err := s.db.DeleteChatMessages(gameID); err != nil {
			log.Printf("cleanup chat messages for game %s: %v", gameID, err)
		} else {
			log.Printf("cleaned up chat messages for game %s", gameID)
		}
	}()
}
