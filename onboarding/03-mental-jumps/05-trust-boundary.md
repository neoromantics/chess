# 3.5 The trust boundary: JWT and `X-User-ID`

In school you probably wrote auth as "check the password, set a session cookie." Production auth has more moving parts because (a) you don't want every service to know the password-hashing secret, and (b) you want stateless verification.

The pattern here:

1. User logs in → gateway checks bcrypt hash against the DB → gateway signs a JWT with `JWT_SECRET` → JWT goes into an HttpOnly cookie called `token`.
2. On every subsequent request, the browser sends the cookie. The gateway validates the JWT signature (only the gateway has `JWT_SECRET`), extracts the user ID.
3. When the gateway proxies the request to `game-service`, it strips any incoming `X-User-ID` header and *injects* the validated user ID: `r.Header.Set("X-User-ID", strconv.FormatInt(user.UserID, 10))`.
4. `game-service` trusts the `X-User-ID` header and does not validate the JWT.

This is the **trust boundary pattern**. Only one service holds the cryptographic secret. Downstream services trust headers set by the trust boundary, because the only way a request reaches them is through the gateway. If you skipped this pattern and let the frontend supply `user_id`, *any caller could read anyone's games*.

Two corollaries you'll meet:

- Anonymous play uses the same pattern with a different header: `X-Anon-ID`, sourced from a `chess-anon` HttpOnly cookie minted on first hit. Same trust boundary, different identity.
- **Authoritative data never comes from the request body when the server already knows it.** Matchmaker rating? Server reads it from the DB. Game owner? Server checks the game row. The frontend can lie about everything; the gateway can lie about nothing because it just verified the JWT.

Reference: [`../../docs/invariants.md`](../../docs/invariants.md) (Auth & trust boundary section).

---

← [`04-streams-vs-pubsub.md`](04-streams-vs-pubsub.md) · Next: [`06-passwords.md`](06-passwords.md)
