package database

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps database operations with thread-safety for SQLite.
type DB struct {
	db *sql.DB
	mu sync.RWMutex
}

// UserProfile represents known information about a creator in the chat.
type UserProfile struct {
	UserID       int64     `json:"user_id"`
	Username     string    `json:"username"`
	FirstName    string    `json:"first_name"`
	ChannelName  string    `json:"channel_name"`
	ChannelTheme string    `json:"channel_theme"`
	Subscribers  string    `json:"subscribers"`
	Notes        string    `json:"notes"`
	LastSeen     time.Time `json:"last_seen"`
}

// LearnedFact represents knowledge added by admins or learned by YouTubych.
type LearnedFact struct {
	ID        int64     `json:"id"`
	Topic     string    `json:"topic"`
	Fact      string    `json:"fact"`
	AddedBy   int64     `json:"added_by"`
	CreatedAt time.Time `json:"created_at"`
}

// New initializes the SQLite database connection and runs migrations.
func New(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database at %s: %w", dbPath, err)
	}

	// Optimize SQLite for concurrent reading and safe writing
	if _, err := conn.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA foreign_keys=ON;"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to set pragmas: %w", err)
	}

	db := &DB{db: conn}
	if err := db.migrate(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("database migration failed: %w", err)
	}

	return db, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.db.Close()
}

func (d *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		user_id INTEGER PRIMARY KEY,
		username TEXT DEFAULT '',
		first_name TEXT DEFAULT '',
		channel_name TEXT DEFAULT '',
		channel_theme TEXT DEFAULT '',
		subscribers TEXT DEFAULT '',
		notes TEXT DEFAULT '',
		last_seen DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS learned_facts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		topic TEXT NOT NULL,
		fact TEXT NOT NULL,
		added_by INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS bot_stats (
		key TEXT PRIMARY KEY,
		value INTEGER DEFAULT 0
	);
	`
	_, err := d.db.Exec(schema)
	return err
}
