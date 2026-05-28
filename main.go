package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load() // load .env in dev (ignored if missing)

	cfg := LoadConfig()
	log.Printf("starting checkers server on :%s", cfg.Port)

	// ─── Database ────────────────────────────────────────────────────────────
	db, err := NewDB(cfg.MongoURI, cfg.DBName)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	// ─── Firebase (optional) ─────────────────────────────────────────────────
	var fb *FirebaseAuth
	if cfg.FirebaseProjectID != "" {
		credFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		fb, err = NewFirebaseAuth(cfg.FirebaseProjectID, credFile)
		if err != nil {
			log.Printf("warning: Firebase init failed: %v (Google login disabled)", err)
		} else {
			log.Println("firebase: Google auth enabled")
		}
	} else {
		log.Println("firebase: disabled (set FIREBASE_PROJECT_ID to enable Google login)")
	}

	// ─── Server ───────────────────────────────────────────────────────────────
	srv := NewServer(cfg, db, fb)

	// ─── Rate limiters ────────────────────────────────────────────────────────
	globalRL := NewRateLimiter(cfg.GlobalRatePerSec, cfg.GlobalBurstSize)
	authRL := NewRateLimiter(cfg.AuthRatePerSec, cfg.AuthBurstSize)

	globalLimit := RateLimitMiddleware(globalRL)
	authLimit := RateLimitMiddleware(authRL)
	requireAuth := authMiddleware(srv.tokens)

	// ─── Routes ───────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Auth routes (stricter rate limit)
	mux.Handle("/auth/guest", authLimit(http.HandlerFunc(methodOnly("POST", srv.handleGuestLogin))))
	mux.Handle("/auth/google", authLimit(http.HandlerFunc(methodOnly("POST", srv.handleGoogleLogin))))
	mux.Handle("/auth/me", globalLimit(requireAuth(http.HandlerFunc(methodOnly("GET", srv.handleMe)))))

	// Profile
	mux.Handle("/profile/name", globalLimit(requireAuth(http.HandlerFunc(methodOnly("PUT", srv.handleUpdateName)))))

	// Leaderboard (public)
	mux.Handle("/leaderboard", globalLimit(http.HandlerFunc(methodOnly("GET", srv.handleLeaderboard))))

	// Matchmaking
	mux.Handle("/matchmaking/join", globalLimit(requireAuth(http.HandlerFunc(methodOnly("POST", srv.handleJoinMatchmaking)))))
	mux.Handle("/matchmaking/leave", globalLimit(requireAuth(http.HandlerFunc(methodOnly("POST", srv.handleLeaveMatchmaking)))))

	// Game
	mux.Handle("/game/bot", globalLimit(requireAuth(http.HandlerFunc(methodOnly("POST", srv.handleStartBotGame)))))
	mux.Handle("/game/", globalLimit(requireAuth(http.HandlerFunc(srv.routeGame))))

	// Chat
	mux.Handle("/api/game/", globalLimit(requireAuth(http.HandlerFunc(srv.routeGameAPI))))

	// WebSocket (auth via query param)
	mux.Handle("/ws", globalLimit(http.HandlerFunc(srv.handleWS)))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))


	// ─── HTTP server with graceful shutdown ────────────────────────────────────
	httpSrv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      corsMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 35 * time.Second, // > matchmaking long-poll 30s
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("server listening on :%s", cfg.Port)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-stop
	log.Println("shutdown signal received, draining...")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("server stopped")
}

// ─── Route helpers ────────────────────────────────────────────────────────────

// methodOnly returns a handler that only accepts the given HTTP method.
func methodOnly(method string, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		fn(w, r)
	}
}

// routeGame dispatches /game/<id> and /game/<id>/resign.
func (s *Server) routeGame(w http.ResponseWriter, r *http.Request) {
	switch {
	case len(r.URL.Path) > 7 && r.Method == http.MethodGet:
		// GET /game/<id>
		s.handleGetGame(w, r)
	case len(r.URL.Path) > 7 && r.Method == http.MethodPost &&
		len(r.URL.Path) > len("/game//resign") && r.URL.Path[len(r.URL.Path)-7:] == "/resign":
		s.handleResign(w, r)
	default:
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}
}

// routeGameAPI dispatches /api/game/<id>/messages
func (s *Server) routeGameAPI(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/messages") && r.Method == http.MethodGet {
		s.handleGetChatMessages(w, r)
	} else {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}
}

// ─── Auth middleware (proper context injection) ───────────────────────────────

func authMiddleware(ts *TokenService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := r.Header.Get("Authorization")
			if tokenStr == "" {
				tokenStr = r.URL.Query().Get("token")
			}
			if tokenStr == "" {
				writeError(w, http.StatusUnauthorized, "missing authorization token")
				return
			}
			userID, provider, err := ts.Verify(tokenStr)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			next.ServeHTTP(w, withUserID(r, userID, string(provider)))
		})
	}
}

// ─── CORS middleware ──────────────────────────────────────────────────────────

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*") // tighten for production
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
