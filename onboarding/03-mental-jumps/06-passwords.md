# 3.6 Why we hash passwords with bcrypt, not SHA-256

You probably learned SHA-family hashes as "cryptographically secure." For password storage they are catastrophically wrong because they're *fast*. An attacker who steals the password DB can compute billions of SHA-256 candidates per second on a GPU and brute-force most user passwords in hours.

bcrypt (and scrypt, argon2) are designed to be *slow* and *memory-hard*. They include a per-password salt (so identical passwords hash differently, and rainbow tables don't work) and a tunable work factor (so you can make hashing slower as hardware gets faster). We use bcrypt because it's well-supported in Go's standard library ecosystem and good enough for our scale. If we were a bigger target, argon2id would be the modern default.

See `pkg/auth/auth.go`.

---

← [`05-trust-boundary.md`](05-trust-boundary.md) · Next: [`07-websocket-hijack.md`](07-websocket-hijack.md)
