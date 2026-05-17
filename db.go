package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DB wraps the MongoDB client and exposes typed helpers.
type DB struct {
	client *mongo.Client
	db     *mongo.Database
	users  *mongo.Collection
	games  *mongo.Collection
}

// NewDB connects to MongoDB and creates indexes.
func NewDB(uri, dbName string) (*DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	opts := options.Client().ApplyURI(uri).
		SetConnectTimeout(10 * time.Second).
		SetServerSelectionTimeout(10 * time.Second)

	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("mongo ping: %w", err)
	}

	database := client.Database(dbName)
	d := &DB{
		client: client,
		db:     database,
		users:  database.Collection("users"),
		games:  database.Collection("games"),
	}
	if err := d.createIndexes(); err != nil {
		return nil, fmt.Errorf("indexes: %w", err)
	}
	log.Println("db: connected to MongoDB")
	return d, nil
}

func (d *DB) createIndexes() error {
	ctx := context.Background()

	// users: unique on user_id
	_, err := d.users.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "user_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys: bson.D{{Key: "firebase_uid", Value: 1}},
			// not unique because empty string for guests
		},
		{
			// leaderboard queries: sort by rating desc
			Keys: bson.D{{Key: "rating", Value: -1}},
		},
	})
	if err != nil {
		return err
	}

	// games: index on game_id and user IDs
	_, err = d.games.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "game_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "white_user_id", Value: 1}}},
		{Keys: bson.D{{Key: "black_user_id", Value: 1}}},
	})
	return err
}

// ─── User helpers ─────────────────────────────────────────────────────────────

func (d *DB) CreateUser(user *User) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	user.ID = primitive.NewObjectID()
	_, err := d.users.InsertOne(ctx, user)
	return err
}

func (d *DB) GetUserByID(userID string) (*User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var u User
	err := d.users.FindOne(ctx, bson.M{"user_id": userID}).Decode(&u)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	return &u, err
}

func (d *DB) GetUserByFirebaseUID(uid string) (*User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var u User
	err := d.users.FindOne(ctx, bson.M{"firebase_uid": uid}).Decode(&u)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	return &u, err
}

// MergeGuestToGoogle upgrades a guest account: sets firebase_uid, email, provider.
func (d *DB) MergeGuestToGoogle(userID, firebaseUID, email, displayName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	update := bson.M{
		"$set": bson.M{
			"firebase_uid": firebaseUID,
			"email":        email,
			"provider":     ProviderGoogle,
			"display_name": displayName,
			"updated_at":   time.Now(),
		},
	}
	_, err := d.users.UpdateOne(ctx, bson.M{"user_id": userID}, update)
	return err
}

func (d *DB) UpdateDisplayName(userID, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := d.users.UpdateOne(ctx,
		bson.M{"user_id": userID},
		bson.M{"$set": bson.M{"display_name": name, "updated_at": time.Now()}},
	)
	return err
}

func (d *DB) UpdateRating(userID string, newRating int, result string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	incFields := bson.M{"games_played": 1}
	switch result {
	case "win":
		incFields["wins"] = 1
	case "loss":
		incFields["losses"] = 1
	case "draw":
		incFields["draws"] = 1
	}
	_, err := d.users.UpdateOne(ctx,
		bson.M{"user_id": userID},
		bson.M{
			"$set": bson.M{"rating": newRating, "updated_at": time.Now()},
			"$inc": incFields,
		},
	)
	return err
}

func (d *DB) GetLeaderboard(limit int) ([]LeaderboardEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := options.Find().
		SetSort(bson.D{{Key: "rating", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{"user_id": 1, "display_name": 1, "rating": 1,
			"games_played": 1, "wins": 1, "losses": 1})

	cur, err := d.users.Find(ctx, bson.M{"games_played": bson.M{"$gt": 0}}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var entries []LeaderboardEntry
	rank := 1
	for cur.Next(ctx) {
		var u User
		if err := cur.Decode(&u); err != nil {
			continue
		}
		entries = append(entries, LeaderboardEntry{
			Rank:        rank,
			UserID:      u.UserID,
			DisplayName: u.DisplayName,
			Rating:      u.Rating,
			GamesPlayed: u.GamesPlayed,
			Wins:        u.Wins,
			Losses:      u.Losses,
		})
		rank++
	}
	return entries, cur.Err()
}

// ─── Game record helpers ──────────────────────────────────────────────────────

func (d *DB) SaveGameRecord(rec *GameRecord) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rec.ID = primitive.NewObjectID()
	_, err := d.games.InsertOne(ctx, rec)
	return err
}

func (d *DB) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.client.Disconnect(ctx)
}
