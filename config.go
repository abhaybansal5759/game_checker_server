package main

import (
	"os"
	"strconv"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	// Server
	Port string

	// MongoDB
	MongoURI string
	DBName   string

	// Firebase (Google OAuth)
	// Set FIREBASE_PROJECT_ID + GOOGLE_APPLICATION_CREDENTIALS (path to service account JSON)
	FirebaseProjectID string

	// JWT for internal session tokens (guest + authed users)
	JWTSecret string

	// Rate limiting
	GlobalRatePerSec  float64 // requests per second per IP (token bucket)
	GlobalBurstSize   int     // burst allowance per IP
	AuthRatePerSec    float64 // stricter limit for /auth/* routes
	AuthBurstSize     int
	WSMsgRatePerSec   float64 // WS messages per second per connection
	WSMsgBurstSize    int

	// Bot
	BotDepth int // minimax search depth (3-5 recommended)
}

// LoadConfig reads config from env, falling back to safe defaults.
func LoadConfig() Config {
	return Config{
		Port:              getEnv("PORT", "8080"),
		MongoURI:          getEnv("MONGO_URI", "mongodb://localhost:27017"),
		DBName:            getEnv("DB_NAME", "checkers"),
		FirebaseProjectID: getEnv("FIREBASE_PROJECT_ID", ""), //checkers-server-73fa0
		JWTSecret:         getEnv("JWT_SECRET", "CHANGE_ME_BEFORE_DEPLOY"),
		GlobalRatePerSec:  getEnvFloat("RATE_PER_SEC", 10),
		GlobalBurstSize:   getEnvInt("RATE_BURST", 30),
		AuthRatePerSec:    getEnvFloat("AUTH_RATE_PER_SEC", 2),
		AuthBurstSize:     getEnvInt("AUTH_BURST", 5),
		WSMsgRatePerSec:   getEnvFloat("WS_RATE_PER_SEC", 5),
		WSMsgBurstSize:    getEnvInt("WS_BURST", 10),
		BotDepth:          getEnvInt("BOT_DEPTH", 4),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}
