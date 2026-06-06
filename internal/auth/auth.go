package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
	"golang.org/x/time/rate"
)

// Argon2 parameters matching RFC 9106 recommendations.
const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64MB
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 16
)

// HashPassword creates an Argon2id hash of the password.
// Returns encoded string: $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func HashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	encoded := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argon2Memory, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash))

	return encoded, nil
}

// VerifyPassword compares a password against an Argon2id hash.
func VerifyPassword(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return false, fmt.Errorf("invalid hash format")
	}

	var memory, timeCost, threads int
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false, fmt.Errorf("parse params: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}

	computedHash := argon2.IDKey([]byte(password), salt, uint32(timeCost), uint32(memory), uint8(threads), uint32(len(expectedHash)))

	return subtle.ConstantTimeCompare(computedHash, expectedHash) == 1, nil
}

// JWT Claims
type Claims struct {
	Role string `json:"role"` // "host" or "viewer"
	jwt.RegisteredClaims
}

// TokenManager handles JWT creation and validation.
type TokenManager struct {
	secret  []byte
	timeout time.Duration
}

func NewTokenManager(secret string, timeoutSeconds int) *TokenManager {
	return &TokenManager{
		secret:  []byte(secret),
		timeout: time.Duration(timeoutSeconds) * time.Second,
	}
}

func (tm *TokenManager) Generate(role string) (string, error) {
	now := time.Now()
	claims := &Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(tm.timeout)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(tm.secret)
}

func (tm *TokenManager) Validate(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return tm.secret, nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}

// RateLimiter wraps golang.org/x/time/rate.
type RateLimiter struct {
	mu          sync.RWMutex
	limiters    map[string]*rate.Limiter
	lastAccess  map[string]time.Time
	stopCleanup chan struct{}
}

func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		limiters:    make(map[string]*rate.Limiter),
		lastAccess:  make(map[string]time.Time),
		stopCleanup: make(chan struct{}),
	}
	// Periodically clean up stale limiters (every 10 minutes, TTL 30 min)
	go rl.cleanupLoop(10*time.Minute, 30*time.Minute)
	return rl
}

func (rl *RateLimiter) cleanupLoop(interval, ttl time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for key, last := range rl.lastAccess {
				if now.Sub(last) > ttl {
					delete(rl.limiters, key)
					delete(rl.lastAccess, key)
				}
			}
			rl.mu.Unlock()
		case <-rl.stopCleanup:
			return
		}
	}
}

// Allow checks if the given key is allowed to proceed.
// rateLimit is attempts per minute.
func (rl *RateLimiter) Allow(key string, rateLimit int) bool {
	rl.mu.RLock()
	limiter, ok := rl.limiters[key]
	rl.mu.RUnlock()

	if !ok {
		// Allow 'rateLimit' requests per minute with burst of rateLimit*2
		burst := rateLimit * 2
		if burst < 5 {
			burst = 5
		}
		rl.mu.Lock()
		limiter, ok = rl.limiters[key]
		if !ok {
			limiter = rate.NewLimiter(rate.Limit(float64(rateLimit)/60.0), burst)
			rl.limiters[key] = limiter
		}
		rl.lastAccess[key] = time.Now()
		rl.mu.Unlock()
	} else {
		rl.mu.Lock()
		rl.lastAccess[key] = time.Now()
		rl.mu.Unlock()
	}
	return limiter.Allow()
}
