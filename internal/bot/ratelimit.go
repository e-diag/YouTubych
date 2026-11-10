package bot

import (
	"sync"
	"time"
)

// RateLimiter manages rate limiting for users and the overall group.
type RateLimiter struct {
	mu            sync.Mutex
	userCooldown  time.Duration
	groupCooldown time.Duration
	userLastSeen  map[int64]time.Time
	lastGroupPost time.Time
}

// NewRateLimiter creates a new thread-safe rate limiter.
func NewRateLimiter(userCooldown, groupCooldown time.Duration) *RateLimiter {
	rl := &RateLimiter{
		userCooldown:  userCooldown,
		groupCooldown: groupCooldown,
		userLastSeen:  make(map[int64]time.Time),
	}

	// Periodically cleanup stale user entries every 10 minutes
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			rl.cleanupStale()
		}
	}()

	return rl
}

// CanRespond checks if responding to this user and in the group is permitted by cooldowns.
func (rl *RateLimiter) CanRespond(userID int64) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	// Check group cooldown
	if now.Sub(rl.lastGroupPost) < rl.groupCooldown {
		return false
	}

	// Check user cooldown
	if lastUserTime, exists := rl.userLastSeen[userID]; exists {
		if now.Sub(lastUserTime) < rl.userCooldown {
			return false
		}
	}

	return true
}

// RecordResponse marks that the bot responded to a user and updated the group timestamp.
func (rl *RateLimiter) RecordResponse(userID int64) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	rl.lastGroupPost = now
	rl.userLastSeen[userID] = now
}

func (rl *RateLimiter) cleanupStale() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	threshold := time.Now().Add(-1 * time.Hour)
	for id, t := range rl.userLastSeen {
		if t.Before(threshold) {
			delete(rl.userLastSeen, id)
		}
	}
}
