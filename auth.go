package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/api/option"
)

// ─── Firebase ─────────────────────────────────────────────────────────────────

// FirebaseAuth wraps the Firebase Admin SDK auth client.
type FirebaseAuth struct {
	client *auth.Client
}

// NewFirebaseAuth initialises Firebase Admin SDK.
// credFile: path to service account JSON (set GOOGLE_APPLICATION_CREDENTIALS env var instead
// if deploying on a managed platform).
// projectID: your Firebase project ID.
func NewFirebaseAuth(projectID, credFile string) (*FirebaseAuth, error) {
	ctx := context.Background()
	var opts []option.ClientOption
	if credFile != "" {
		opts = append(opts, option.WithCredentialsFile(credFile))
	}
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID}, opts...)
	if err != nil {
		return nil, fmt.Errorf("firebase init: %w", err)
	}
	client, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("firebase auth: %w", err)
	}
	return &FirebaseAuth{client: client}, nil
}

// VerifyIDToken verifies a Firebase ID token and returns the token info.
func (fa *FirebaseAuth) VerifyIDToken(idToken string) (*auth.Token, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return fa.client.VerifyIDToken(ctx, idToken)
}

// ─── JWT (internal session tokens) ───────────────────────────────────────────

type TokenService struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenService(secret string) *TokenService {
	return &TokenService{
		secret: []byte(secret),
		ttl:    30 * 24 * time.Hour, // 30-day sessions
	}
}

type sessionClaims struct {
	UserID   string       `json:"uid"`
	Provider AuthProvider `json:"pvd"`
	jwt.RegisteredClaims
}

// Issue creates a signed JWT for a user.
func (ts *TokenService) Issue(userID string, provider AuthProvider) (string, error) {
	claims := sessionClaims{
		UserID:   userID,
		Provider: provider,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ts.ttl)),
			Issuer:    "checkers-server",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(ts.secret)
}

// Verify parses and validates a JWT. Returns (userID, provider, error).
func (ts *TokenService) Verify(tokenStr string) (string, AuthProvider, error) {
	tokenStr = strings.TrimPrefix(tokenStr, "Bearer ")
	tok, err := jwt.ParseWithClaims(tokenStr, &sessionClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return ts.secret, nil
	})
	if err != nil {
		return "", "", err
	}
	claims, ok := tok.Claims.(*sessionClaims)
	if !ok || !tok.Valid {
		return "", "", fmt.Errorf("invalid token claims")
	}
	return claims.UserID, claims.Provider, nil
}

// MaybeVerify attempts to verify a token but returns ("", "", nil) if empty.
// Used for optional guest_token field.
func (ts *TokenService) MaybeVerify(tokenStr string) (string, AuthProvider, error) {
	if strings.TrimSpace(tokenStr) == "" {
		return "", "", nil
	}
	return ts.Verify(tokenStr)
}
