package auth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	jwtSecret       = []byte(os.Getenv("JWT_SECRET"))
	ErrUnauthorized = errors.New("unauthorized")
)

func init() {
	if len(jwtSecret) == 0 {
		// SECURITY: In production, JWT_SECRET MUST be set via environment/K8s Secrets.
		// Without it, we generate a random ephemeral secret per process start.
		// This means tokens will NOT survive pod restarts — by design.
		jwtSecret = []byte(uuid.New().String())
		println("⚠️  WARNING: JWT_SECRET not set. Using ephemeral random secret.")
		println("   All user sessions will be invalidated on restart.")
		println("   Set JWT_SECRET via 'just secrets-init' or K8s Secrets for production.")
	}
}

type Claims struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

func GenerateToken(userID int64, username string) (string, error) {
	expirationTime := time.Now().Add(72 * time.Hour)
	claims := &Claims{
		UserID:   userID,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func ValidateToken(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})

	if err != nil || !token.Valid {
		return nil, ErrUnauthorized
	}

	return claims, nil
}

type contextKey string

const (
	UserContextKey    contextKey = "user"
	SessionContextKey contextKey = "session"
)

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Check for anonymous session cookie
		sessionID := ""
		cookie, err := r.Cookie("session_id")
		if err == nil {
			sessionID = cookie.Value
		} else {
			// Generate a new session ID for guests
			sessionID = uuid.New().String()
			http.SetCookie(w, &http.Cookie{
				Name:     "session_id",
				Value:    sessionID,
				Path:     "/",
				HttpOnly: true,
				Expires:  time.Now().Add(365 * 24 * time.Hour), // 1 year
			})
		}
		ctx := context.WithValue(r.Context(), SessionContextKey, sessionID)

		// 2. Check for auth token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			cookie, err := r.Cookie("token")
			if err == nil {
				authHeader = "Bearer " + cookie.Value
			}
		}

		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := ValidateToken(tokenString)
		if err != nil {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		ctx = context.WithValue(ctx, UserContextKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func GetUser(ctx context.Context) (*Claims, bool) {
	claims, ok := ctx.Value(UserContextKey).(*Claims)
	return claims, ok
}

func GetSessionID(ctx context.Context) string {
	id, _ := ctx.Value(SessionContextKey).(string)
	return id
}
