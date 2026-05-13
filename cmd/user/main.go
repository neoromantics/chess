package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/metrics"
)

type UserServer struct {
	db db.Store
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	store, err := db.OpenPostgres(dsn)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}
	defer store.Close()

	s := &UserServer{db: store}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/signup", s.handleSignup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/user/me", auth.Middleware(http.HandlerFunc(s.handleMe)).ServeHTTP)
	mux.HandleFunc("GET /api/user/profile", auth.Middleware(http.HandlerFunc(s.handleGetProfile)).ServeHTTP)
	mux.HandleFunc("PUT /api/user/profile", auth.Middleware(http.HandlerFunc(s.handleUpdateProfile)).ServeHTTP)
	mux.HandleFunc("POST /api/user/password", auth.Middleware(http.HandlerFunc(s.handleChangePassword)).ServeHTTP)
	mux.HandleFunc("GET /api/user/stats", auth.Middleware(http.HandlerFunc(s.handleUserStats)).ServeHTTP)
	mux.HandleFunc("GET /api/users/search", auth.Middleware(http.HandlerFunc(s.handleUserSearch)).ServeHTTP)

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("OK"))
	})
	mux.Handle("/metrics", metrics.Handler())

	log.Printf("User Service starting on port %s...", port)
	log.Fatal(http.ListenAndServe(":"+port, metrics.HTTPMiddleware("user-service", mux)))
}

func (s *UserServer) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 32 {
		http.Error(w, "username must be 3-32 characters", 400)
		return
	}
	if len(req.Password) < 6 {
		http.Error(w, "password must be at least 6 characters", 400)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	user, err := s.db.CreateUser(req.Username, hash)
	if err != nil {
		slog.Error("signup failed", "username", req.Username, "error", err)
		http.Error(w, "username taken", 409)
		return
	}
	token, _ := auth.GenerateToken(user.ID, user.Username)
	http.SetCookie(w, s.secureCookie("token", token))
	writeJSON(w, map[string]any{"user": user, "token": token})
}

func (s *UserServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	user, err := s.db.GetUserByUsername(req.Username)
	if err != nil {
		slog.Error("login failed: user lookup error", "username", req.Username, "error", err)
		http.Error(w, "invalid credentials", 401)
		return
	}
	if !auth.CheckPasswordHash(req.Password, user.PasswordHash) {
		slog.Warn("login failed: password mismatch", "username", req.Username)
		http.Error(w, "invalid credentials", 401)
		return
	}
	// Bump last_login; non-fatal if it fails.
	if err := s.db.UpdateLastLogin(user.ID); err != nil {
		slog.Warn("update last_login failed", "user_id", user.ID, "error", err)
	}
	token, _ := auth.GenerateToken(user.ID, user.Username)
	http.SetCookie(w, s.secureCookie("token", token))
	writeJSON(w, map[string]any{"user": user, "token": token})
}

func (s *UserServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	c := s.secureCookie("token", "")
	c.MaxAge = -1
	http.SetCookie(w, c)
	w.WriteHeader(204)
}

func (s *UserServer) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	dbUser, err := s.db.GetUserByID(user.UserID)
	if err != nil {
		writeJSON(w, user)
		return
	}
	writeJSON(w, dbUser)
}

func (s *UserServer) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	dbUser, err := s.db.GetUserByID(user.UserID)
	if err != nil {
		http.Error(w, "user not found", 404)
		return
	}
	writeJSON(w, dbUser)
}

func (s *UserServer) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	var req struct {
		DisplayName string `json:"display_name"`
		Bio         string `json:"bio"`
		AvatarURL   string `json:"avatar_url"`
		Country     string `json:"country"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if len(req.DisplayName) > 64 {
		http.Error(w, "display name too long", 400)
		return
	}
	if len(req.Bio) > 500 {
		http.Error(w, "bio too long", 400)
		return
	}
	if err := s.db.UpdateUserProfile(user.UserID, req.DisplayName, req.Bio, req.AvatarURL, req.Country); err != nil {
		http.Error(w, "failed to update profile", 500)
		return
	}
	dbUser, _ := s.db.GetUserByID(user.UserID)
	writeJSON(w, dbUser)
}

func (s *UserServer) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	dbUser, _ := s.db.GetUserByID(user.UserID)
	if !auth.CheckPasswordHash(req.CurrentPassword, dbUser.PasswordHash) {
		http.Error(w, "invalid current password", 401)
		return
	}
	newHash, _ := auth.HashPassword(req.NewPassword)
	s.db.UpdatePassword(user.UserID, newHash)
	w.WriteHeader(204)
}

func (s *UserServer) handleUserStats(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.GetUser(r.Context())
	stats, err := s.db.GetUserStats(user.UserID)
	if err != nil {
		http.Error(w, "failed to fetch stats", 500)
		return
	}
	writeJSON(w, stats)
}

func (s *UserServer) handleUserSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if len(query) < 2 {
		writeJSON(w, []any{})
		return
	}
	users, err := s.db.SearchUsersByPrefix(query)
	if err != nil {
		http.Error(w, "search failed", 500)
		return
	}
	writeJSON(w, users)
}

func (s *UserServer) secureCookie(name, value string) *http.Cookie {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if os.Getenv("HTTPS_ENABLED") == "true" {
		c.Secure = true
	}
	return c
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
