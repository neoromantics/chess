package main

// Bot pool for the engine-fallback matchmaker.
//
// TODO(matchmaker-engine-fallback): remove this whole file (and the
// is_bot column + UpsertBot/ListBots queries) once organic pairing
// volume sustains itself. Search for the TODO tag in matchmaker.go.
//
// Why bots are real DB users (not synthetic IDs):
//   - GameRecord.WhiteUserID / BlackUserID are FK → users(id); the
//     existing PvP code path (ListGames, snapshotFromRecord, the SPA's
//     `isPvP = white_user_id && black_user_id` derivation) only works
//     when both sides have real user IDs. Faking the IDs would mean
//     forking that whole path; seeding real rows is cheaper.
//   - Their password_hash is a random bcrypt of an opaque secret so
//     no one can log in as them.
//   - SearchUsersByPrefix excludes is_bot=true so they don't appear
//     in invite autocomplete.
//
// Why the pool is small and hardcoded:
//   - 12 bots cover the ~1100–2100 rating band well enough that any
//     human matchmaker entry can find one within ±150 points.
//   - A larger pool means more "different opponents seen across
//     sessions", which would tip the user off if they noticed the
//     handles repeating across days. 12 is a balance between churn
//     and feeling-organic.

import (
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	mathrand "math/rand"
	"sort"
	"sync/atomic"
	"time"

	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/db"
)

// botSeed defines one entry in the bot pool. Usernames are deliberately
// chess-themed but non-obvious — nothing like "engine_bot_4" that would
// give it away in the game-list view.
type botSeed struct {
	Username string
	Rating   int
}

var botSeeds = []botSeed{
	{"QueenKnight42", 1100},
	{"PawnStorm91", 1250},
	{"RookRunner_J", 1350},
	{"BishopBlitz", 1450},
	{"Knightfall_88", 1500},
	{"EnPassantE4", 1550},
	{"CastleCrusher", 1600},
	{"MattedKing", 1700},
	{"ZugzwangZee", 1800},
	{"FianchettoFan", 1900},
	{"TacticalT", 2000},
	{"PrincessOfPawns", 2100},
}

// botPool is the in-memory cache of seeded bot users, populated once at
// game-service boot. atomic.Value so a future hot-reload can swap it
// without locking the matchmaker hot path.
var botPool atomic.Value // []db.BotUser, sorted by rating ASC

// seedBots upserts the configured bot pool and warms the in-memory
// cache. Idempotent across replicas — the underlying UpsertBot uses
// ON CONFLICT (username) so concurrent boots are safe. Each bot gets a
// fresh random bcrypt hash on first seed; subsequent boots leave the
// hash intact (UpsertBot only flips is_bot=TRUE).
func seedBots(store db.Store) {
	for _, seed := range botSeeds {
		// Random opaque secret → bcrypt. We never need the plaintext;
		// the row exists only as a FK target and a username.
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			slog.Error("bot seed: rand failed", "username", seed.Username, "error", err)
			continue
		}
		hash, err := auth.HashPassword(base64.RawStdEncoding.EncodeToString(buf))
		if err != nil {
			slog.Error("bot seed: hash failed", "username", seed.Username, "error", err)
			continue
		}
		if _, err := store.UpsertBot(seed.Username, hash, seed.Rating); err != nil {
			slog.Error("bot seed: upsert failed", "username", seed.Username, "error", err)
			continue
		}
	}
	bots, err := store.ListBots()
	if err != nil {
		slog.Error("bot seed: list failed", "error", err)
		return
	}
	sort.Slice(bots, func(i, j int) bool { return bots[i].Rating < bots[j].Rating })
	botPool.Store(bots)
	slog.Info("bot pool seeded", "count", len(bots))
}

// pickBot returns one bot from the pool whose rating is closest to
// targetRating, with a random pick within a ±150 window so the user
// doesn't always face the same opponent. Falls back to nearest-by-
// rating if the window is empty (e.g. very low / very high target).
// Returns ok=false if the pool is empty (seeding failed) so callers
// can decide whether to skip the fallback entirely.
func pickBot(targetRating int) (db.BotUser, bool) {
	v := botPool.Load()
	if v == nil {
		return db.BotUser{}, false
	}
	pool, _ := v.([]db.BotUser)
	if len(pool) == 0 {
		return db.BotUser{}, false
	}
	const window = 150
	candidates := pool[:0:0]
	for _, b := range pool {
		if abs(int(b.Rating)-targetRating) <= window {
			candidates = append(candidates, b)
		}
	}
	if len(candidates) > 0 {
		return candidates[mathrand.Intn(len(candidates))], true
	}
	// Nothing in window — fall back to the nearest single bot.
	best := pool[0]
	bestDist := abs(int(best.Rating) - targetRating)
	for _, b := range pool[1:] {
		if d := abs(int(b.Rating) - targetRating); d < bestDist {
			best = b
			bestDist = d
		}
	}
	return best, true
}

// isBotMatch reports whether a game record was created by the engine-
// fallback matchmaker. The defining feature is that a side has BOTH a
// real user_id AND an engine flag set — no other code path creates
// that combination (PvP has both user_ids + no engine flags; PvE has
// engine flag + nil user_id on that side).
//
// Used by snapshotFromRecord to lie about the engine flags so the SPA
// renders the game as PvP. The server-side engine trigger still uses
// the truthful rec.EngineWhite/EngineBlack so moves actually happen.
//
// TODO(matchmaker-engine-fallback): delete with the bot pool.
func isBotMatch(rec *db.GameRecord) bool {
	if rec == nil {
		return false
	}
	if rec.EngineWhite && rec.WhiteUserID != nil {
		return true
	}
	if rec.EngineBlack && rec.BlackUserID != nil {
		return true
	}
	return false
}

// pickBotReactionDelay returns one of the configured bot reaction
// times at random. Source is botReactionDelays in matchmaker.go;
// kept here so all bot helpers live in one place.
// TODO(matchmaker-engine-fallback): delete with the bot pool.
func pickBotReactionDelay() time.Duration {
	if len(botReactionDelays) == 0 {
		return 0
	}
	return botReactionDelays[mathrand.Intn(len(botReactionDelays))]
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
