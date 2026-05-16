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
// fallback matchmaker. Defining feature: BOTH sides have a real user_id
// (one of them is a seeded bot) AND at least one side has an engine
// flag set. No other code path produces that combination:
//   - PvP: both user_ids + no engine flags.
//   - PvE: engine flag on one side + nil user_id on that engine side.
//   - Engine-vs-engine owned by a signed-in user: one user_id (the owner,
//     on whichever side they "claim"), nil on the other, both engine
//     flags true.
//
// The earlier looser predicate ("any side has user_id AND engine flag")
// false-positived on the third case, which made snapshotFromRecord lie
// about engine_white/engine_black on a legitimate engine-vs-engine game.
// The SPA then showed both sides as Human, but the engine kept playing —
// and toggling the (visible-because-not-PvP) engine settings re-bound
// which side the engine drove without changing the displayed labels.
// Requiring both user_ids non-nil scopes the disguise to actual
// matchmaker output.
//
// Used by snapshotFromRecord to render the game as PvP. The server-side
// engine trigger still uses the truthful rec.EngineWhite/EngineBlack so
// moves happen.
//
// TODO(matchmaker-engine-fallback): delete with the bot pool.
func isBotMatch(rec *db.GameRecord) bool {
	if rec == nil || rec.WhiteUserID == nil || rec.BlackUserID == nil {
		return false
	}
	return rec.EngineWhite || rec.EngineBlack
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

// isBotUserID reports whether uid is in the seeded bot pool. Used by
// the draw/takeback handlers to decide whether the offer recipient is
// a bot that should auto-respond after a humanizing delay.
// TODO(matchmaker-engine-fallback): delete with the bot pool.
func isBotUserID(uid int64) bool {
	v := botPool.Load()
	if v == nil {
		return false
	}
	pool, _ := v.([]db.BotUser)
	for _, b := range pool {
		if b.ID == uid {
			return true
		}
	}
	return false
}

// opponentUIDOnRec returns the other participant's user_id, given a
// known participant. ok=false if rec doesn't have both sides populated
// (engine games or temp games will hit this and be skipped silently).
// TODO(matchmaker-engine-fallback): delete with the bot pool.
func opponentUIDOnRec(rec *db.GameRecord, uid int64) (int64, bool) {
	if rec == nil {
		return 0, false
	}
	if rec.WhiteUserID != nil && rec.BlackUserID != nil {
		if *rec.WhiteUserID == uid {
			return *rec.BlackUserID, true
		}
		if *rec.BlackUserID == uid {
			return *rec.WhiteUserID, true
		}
	}
	return 0, false
}

// botResponseDelay picks a wall-clock delay for the bot's reply to a
// draw or takeback offer. Bounded to (0.5s, 10s] — instant replies are
// the same "this is a bot" tell as instant moves, and >10s would feel
// like a hung opponent and tempt the user to refresh.
// TODO(matchmaker-engine-fallback): delete with the bot pool.
func botResponseDelay() time.Duration {
	// 500ms minimum, 10s maximum; uniformly distributed in between.
	return 500*time.Millisecond + time.Duration(mathrand.Intn(9500))*time.Millisecond
}

// botShouldAcceptOffer returns whether the bot accepts an offer this
// time. ~40% accept rate so the bot doesn't always agree (which would
// itself be a tell, and would let the user farm draws/takebacks).
// TODO(matchmaker-engine-fallback): delete with the bot pool.
func botShouldAcceptOffer() bool {
	return mathrand.Intn(10) < 4
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
