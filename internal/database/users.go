package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GetUser retrieves a user profile by Telegram UserID.
func (d *DB) GetUser(userID int64) (*UserProfile, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query := `SELECT user_id, username, first_name, channel_name, channel_theme, subscribers, notes, last_seen 
	          FROM users WHERE user_id = ?`

	row := d.db.QueryRow(query, userID)
	var u UserProfile
	err := row.Scan(&u.UserID, &u.Username, &u.FirstName, &u.ChannelName, &u.ChannelTheme, &u.Subscribers, &u.Notes, &u.LastSeen)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // user not found
		}
		return nil, fmt.Errorf("failed to query user: %w", err)
	}
	return &u, nil
}

// SaveOrUpdateUser creates or updates basic user data and bumps last_seen.
func (d *DB) SaveOrUpdateUser(userID int64, username, firstName string) (*UserProfile, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	query := `
	INSERT INTO users (user_id, username, first_name, last_seen)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(user_id) DO UPDATE SET
		username = excluded.username,
		first_name = excluded.first_name,
		last_seen = excluded.last_seen;
	`
	_, err := d.db.Exec(query, userID, username, firstName, now)
	if err != nil {
		return nil, fmt.Errorf("failed to upsert user: %w", err)
	}

	// Retrieve full profile
	var u UserProfile
	fetchQuery := `SELECT user_id, username, first_name, channel_name, channel_theme, subscribers, notes, last_seen 
	               FROM users WHERE user_id = ?`
	row := d.db.QueryRow(fetchQuery, userID)
	if err := row.Scan(&u.UserID, &u.Username, &u.FirstName, &u.ChannelName, &u.ChannelTheme, &u.Subscribers, &u.Notes, &u.LastSeen); err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateUserProfile updates creator-specific details for a user.
func (d *DB) UpdateUserProfile(userID int64, channelName, channelTheme, subscribers, notes string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	query := `
	UPDATE users SET 
		channel_name = CASE WHEN ? != '' THEN ? ELSE channel_name END,
		channel_theme = CASE WHEN ? != '' THEN ? ELSE channel_theme END,
		subscribers = CASE WHEN ? != '' THEN ? ELSE subscribers END,
		notes = CASE WHEN ? != '' THEN ? ELSE notes END,
		last_seen = CURRENT_TIMESTAMP
	WHERE user_id = ?;
	`
	_, err := d.db.Exec(query,
		channelName, channelName,
		channelTheme, channelTheme,
		subscribers, subscribers,
		notes, notes,
		userID,
	)
	return err
}

// GetAllUsers retrieves all recorded creators (up to limit).
func (d *DB) GetAllUsers(limit int) ([]*UserProfile, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}
	query := `SELECT user_id, username, first_name, channel_name, channel_theme, subscribers, notes, last_seen 
	          FROM users ORDER BY last_seen DESC LIMIT ?`

	rows, err := d.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*UserProfile
	for rows.Next() {
		var u UserProfile
		if err := rows.Scan(&u.UserID, &u.Username, &u.FirstName, &u.ChannelName, &u.ChannelTheme, &u.Subscribers, &u.Notes, &u.LastSeen); err != nil {
			return nil, err
		}
		users = append(users, &u)
	}
	return users, nil
}

// AddFact records a new piece of learned knowledge.
func (d *DB) AddFact(topic, fact string, addedBy int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	query := `INSERT INTO learned_facts (topic, fact, added_by, created_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`
	_, err := d.db.Exec(query, topic, fact, addedBy)
	return err
}

// GetFacts retrieves recent learned facts.
func (d *DB) GetFacts(limit int) ([]LearnedFact, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if limit <= 0 {
		limit = 20
	}
	query := `SELECT id, topic, fact, added_by, created_at FROM learned_facts ORDER BY id DESC LIMIT ?`
	rows, err := d.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []LearnedFact
	for rows.Next() {
		var f LearnedFact
		if err := rows.Scan(&f.ID, &f.Topic, &f.Fact, &f.AddedBy, &f.CreatedAt); err != nil {
			return nil, err
		}
		facts = append(facts, f)
	}
	return facts, nil
}

// IncrementStat atomically increments a named counter.
func (d *DB) IncrementStat(key string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	query := `
	INSERT INTO bot_stats (key, value) VALUES (?, 1)
	ON CONFLICT(key) DO UPDATE SET value = value + 1;
	`
	_, err := d.db.Exec(query, key)
	return err
}

// GetStat retrieves a named counter value.
func (d *DB) GetStat(key string) (int64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var val int64
	err := d.db.QueryRow(`SELECT value FROM bot_stats WHERE key = ?`, key).Scan(&val)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return val, nil
}
