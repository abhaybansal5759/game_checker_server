package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"
)

// ─── Game Session ─────────────────────────────────────────────────────────────

// GameSession holds one live game in memory.
type GameSession struct {
	mu sync.Mutex

	ID        string
	Game      *CheckersGame
	WhiteID   string // user_id
	BlackID   string
	IsBot     bool   // true if black is a bot
	BotEngine *Bot

	// WebSocket connections (nil if not connected)
	WhiteConn *PlayerConn
	BlackConn *PlayerConn

	CreatedAt time.Time
}

// PlayerConn wraps a websocket connection with a rate limiter.
type PlayerConn struct {
	conn    *websocket.Conn
	limiter *rate.Limiter
	mu      sync.Mutex
}

func newPlayerConn(conn *websocket.Conn, rPerSec float64, burst int) *PlayerConn {
	return &PlayerConn{
		conn:    conn,
		limiter: rate.NewLimiter(rate.Limit(rPerSec), burst),
	}
}

func (pc *PlayerConn) Send(msg interface{}) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.conn.WriteJSON(msg)
}

func (pc *PlayerConn) Allow() bool {
	return pc.limiter.Allow()
}

func (pc *PlayerConn) Close() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.conn.Close()
}

// broadcast sends msg to both players.
func (s *GameSession) broadcast(msg interface{}) {
	if s.WhiteConn != nil {
		_ = s.WhiteConn.Send(msg)
	}
	if s.BlackConn != nil && !s.IsBot {
		_ = s.BlackConn.Send(msg)
	}
}

// stateFor returns the GameStatePayload from a specific player's perspective.
func (s *GameSession) stateFor(color string) GameStatePayload {
	return GameStatePayload{
		GameID:    s.ID,
		Board:     s.Game.Board,
		Turn:      s.Game.Turn,
		Status:    s.Game.Status,
		YourColor: color,
	}
}

// ─── Session Store ────────────────────────────────────────────────────────────

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*GameSession // game_id → session
	byUser   map[string]string       // user_id → game_id (current active game)
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*GameSession),
		byUser:   make(map[string]string),
	}
}

func (ss *SessionStore) Add(s *GameSession) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.sessions[s.ID] = s
	ss.byUser[s.WhiteID] = s.ID
	if !s.IsBot {
		ss.byUser[s.BlackID] = s.ID
	}
}

func (ss *SessionStore) Get(gameID string) (*GameSession, bool) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	s, ok := ss.sessions[gameID]
	return s, ok
}

func (ss *SessionStore) GetByUser(userID string) (*GameSession, bool) {
	ss.mu.RLock()
	gameID, ok := ss.byUser[userID]
	ss.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return ss.Get(gameID)
}

func (ss *SessionStore) Remove(gameID string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.sessions[gameID]
	if !ok {
		return
	}
	delete(ss.sessions, gameID)
	delete(ss.byUser, s.WhiteID)
	if !s.IsBot {
		delete(ss.byUser, s.BlackID)
	}
}

func (ss *SessionStore) HasActiveGame(userID string) bool {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	_, ok := ss.byUser[userID]
	return ok
}

// ─── Matchmaking Queue ────────────────────────────────────────────────────────

type QueueEntry struct {
	UserID    string
	Notify    chan *GameSession // closed/sent when matched
	JoinedAt  time.Time
}

type Matchmaker struct {
	mu      sync.Mutex
	queue   []*QueueEntry
	sessions *SessionStore
	gameIDFn func() string
}

func NewMatchmaker(sessions *SessionStore, gameIDFn func() string) *Matchmaker {
	m := &Matchmaker{
		sessions: sessions,
		gameIDFn: gameIDFn,
	}
	go m.gcLoop()
	return m
}

// Join adds a user to the matchmaking queue.
// If a match is found immediately, returns the session.
// Otherwise returns nil — client should poll or wait on the channel.
func (m *Matchmaker) Join(userID string) (chan *GameSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Remove existing entry for this user if any
	m.removeUserLocked(userID)

	// Try to match with the first waiting opponent
	for i, entry := range m.queue {
		if entry.UserID == userID {
			continue
		}
		// Match found!
		m.queue = append(m.queue[:i], m.queue[i+1:]...)

		sess := m.createSession(entry.UserID, userID)

		// Notify the waiting player
		entry.Notify <- sess
		close(entry.Notify)

		// Return session to caller via a pre-filled channel
		ch := make(chan *GameSession, 1)
		ch <- sess
		return ch, true
	}

	// No match yet — add to queue
	ch := make(chan *GameSession, 1)
	m.queue = append(m.queue, &QueueEntry{
		UserID:   userID,
		Notify:   ch,
		JoinedAt: time.Now(),
	})
	return ch, false
}

func (m *Matchmaker) Leave(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeUserLocked(userID)
}

func (m *Matchmaker) removeUserLocked(userID string) {
	for i, e := range m.queue {
		if e.UserID == userID {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return
		}
	}
}

func (m *Matchmaker) createSession(whiteID, blackID string) *GameSession {
	sess := &GameSession{
		ID:        m.gameIDFn(),
		Game:      NewCheckersGame(),
		WhiteID:   whiteID,
		BlackID:   blackID,
		IsBot:     false,
		CreatedAt: time.Now(),
	}
	m.sessions.Add(sess)
	return sess
}

// CreateBotGame creates a new session vs the bot for userID.
func (m *Matchmaker) CreateBotGame(userID string, bot *Bot, gameIDFn func() string) *GameSession {
	sess := &GameSession{
		ID:        gameIDFn(),
		Game:      NewCheckersGame(),
		WhiteID:   userID,
		BlackID:   "bot",
		IsBot:     true,
		BotEngine: bot,
		CreatedAt: time.Now(),
	}
	m.sessions.Add(sess)
	return sess
}

// gcLoop removes queue entries older than 2 minutes (player abandoned search).
func (m *Matchmaker) gcLoop() {
	for range time.Tick(30 * time.Second) {
		m.mu.Lock()
		cutoff := time.Now().Add(-2 * time.Minute)
		filtered := m.queue[:0]
		for _, e := range m.queue {
			if e.JoinedAt.After(cutoff) {
				filtered = append(filtered, e)
			}
		}
		m.queue = filtered
		m.mu.Unlock()
	}
}

// QueueLength returns how many players are waiting.
func (m *Matchmaker) QueueLength() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queue)
}

// ─── ID generation ────────────────────────────────────────────────────────────

func newGameID() string {
	return fmt.Sprintf("game-%d", time.Now().UnixNano())
}
