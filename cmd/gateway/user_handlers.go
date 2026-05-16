package main

// Auth + user-profile handlers, folded in from the former cmd/user
// service in the 6→3 pod consolidation. The gateway already ran
// auth.Middleware on every request to populate the user context; it
// always had to know about JWTs anyway, so owning the entire auth
// surface (signup/login/profile/search) eliminates one cross-service
// boundary without changing scaling behaviour — user-service was a
// thin wrapper around the same DB the gateway now opens directly.
//
// Routes registered in cmd/gateway/main.go:
//   POST   /api/auth/signup
//   POST   /api/auth/login
//   POST   /api/auth/logout
//   GET    /api/user/me
//   GET    /api/user/profile
//   PUT    /api/user/profile
//   POST   /api/user/password
//   GET    /api/user/stats
//   GET    /api/users/search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/neoromantics/chess/pkg/auth"
)

func (gw *Gateway) handleSignup(w http.ResponseWriter, r *http.Request) {
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
	if err := auth.ValidatePassword(req.Password, req.Username); err != nil {
		// PasswordError messages are written for end users — pass
		// through verbatim. The frontend Signup form surfaces them
		// next to the password field.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	user, err := gw.db.CreateUser(req.Username, hash)
	if err != nil {
		slog.Error("signup failed", "username", req.Username, "error", err)
		http.Error(w, "username taken", 409)
		return
	}
	token, _ := auth.GenerateToken(user.ID, user.Username)
	http.SetCookie(w, secureCookie("token", token))

	// Carry-over: if the signup request came from an anonymous tab
	// with a live temp game, upgrade it into a durable game owned by
	// the new user and return the new game_id so the SPA can land
	// the user back in their game (not on a fresh dashboard).
	upgradedGameID := ""
	if anonID := readAnonCookie(r); anonID != "" {
		if gid, err := gw.upgradeAnonGame(r.Context(), anonID, user.ID); err != nil {
			slog.Warn("anon→user upgrade failed", "anon_id", anonID, "user_id", user.ID, "error", err)
		} else {
			upgradedGameID = gid
		}
	}

	writeJSONGW(w, map[string]any{"user": user, "token": token, "upgraded_game_id": upgradedGameID})
}

// upgradeAnonGame asks game-service to copy the temp game tied to
// this anon cookie into a durable row for the freshly-signed-up
// user. Errors are non-fatal — we'd rather succeed signup without
// the carry-over than fail signup over a Redis hiccup.
func (gw *Gateway) upgradeAnonGame(ctx context.Context, anonID string, userID int64) (string, error) {
	target := gw.gameSvcURL.String() + "/api/temp/upgrade"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Anon-ID", anonID)
	req.Header.Set("X-User-ID", strconv.FormatInt(userID, 10))
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upgrade returned %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		GameID string `json:"game_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.GameID, nil
}

func (gw *Gateway) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	user, err := gw.db.GetUserByUsername(req.Username)
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
	if err := gw.db.UpdateLastLogin(user.ID); err != nil {
		slog.Warn("update last_login failed", "user_id", user.ID, "error", err)
	}
	token, _ := auth.GenerateToken(user.ID, user.Username)
	http.SetCookie(w, secureCookie("token", token))
	writeJSONGW(w, map[string]any{"user": user, "token": token})
}

func (gw *Gateway) handleLogout(w http.ResponseWriter, r *http.Request) {
	c := secureCookie("token", "")
	c.MaxAge = -1
	http.SetCookie(w, c)
	w.WriteHeader(204)
}

func (gw *Gateway) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	dbUser, err := gw.db.GetUserByID(user.UserID)
	if err != nil {
		writeJSONGW(w, user)
		return
	}
	writeJSONGW(w, dbUser)
}

func (gw *Gateway) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	dbUser, err := gw.db.GetUserByID(user.UserID)
	if err != nil {
		http.Error(w, "user not found", 404)
		return
	}
	writeJSONGW(w, dbUser)
}

func (gw *Gateway) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
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
	if err := gw.db.UpdateUserProfile(user.UserID, req.DisplayName, req.Bio, req.AvatarURL, req.Country); err != nil {
		http.Error(w, "failed to update profile", 500)
		return
	}
	dbUser, _ := gw.db.GetUserByID(user.UserID)
	writeJSONGW(w, dbUser)
}

func (gw *Gateway) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	dbUser, _ := gw.db.GetUserByID(user.UserID)
	if dbUser == nil || !auth.CheckPasswordHash(req.CurrentPassword, dbUser.PasswordHash) {
		http.Error(w, "invalid current password", 401)
		return
	}
	if err := auth.ValidatePassword(req.NewPassword, dbUser.Username); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newHash, _ := auth.HashPassword(req.NewPassword)
	_ = gw.db.UpdatePassword(user.UserID, newHash)
	w.WriteHeader(204)
}

func (gw *Gateway) handleUserStats(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "not logged in", 401)
		return
	}
	stats, err := gw.db.GetUserStats(user.UserID)
	if err != nil {
		http.Error(w, "failed to fetch stats", 500)
		return
	}
	writeJSONGW(w, stats)
}

func (gw *Gateway) handleUserSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if len(query) < 2 {
		writeJSONGW(w, []any{})
		return
	}
	users, err := gw.db.SearchUsersByPrefix(query)
	if err != nil {
		http.Error(w, "search failed", 500)
		return
	}
	writeJSONGW(w, users)
}

// secureCookie + writeJSONGW are file-local helpers; renamed with the
// GW suffix on the JSON one to avoid colliding with any future
// gateway helper of the same name.

func secureCookie(name, value string) *http.Cookie {
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

func writeJSONGW(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
