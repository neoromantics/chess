# 3.8 Same-origin and the CSWSH attack

**Same-origin** means: scheme + host + port all match. The browser enforces this for normal Fetch requests via CORS. WebSocket upgrades, by default, **do not** enforce same-origin — the `Origin` header is sent, but it's the *server's* job to check it.

If you skip the check, an attacker's site can open a WebSocket to your server using the victim's logged-in cookies (since cookies are sent by origin, not by the destination), then read/write the victim's data. This is "Cross-Site WebSocket Hijacking" (CSWSH).

We defend by setting `Upgrader.CheckOrigin` to a function that checks the `Origin` header against same-origin plus an explicit `ALLOWED_WS_ORIGINS` env-var allow-list. See `cmd/gateway/ws.go:checkWSOrigin`. The same logic applies to any cookie-authenticated WebSocket anywhere.

---

← [`07-websocket-hijack.md`](07-websocket-hijack.md) · Next: [`09-metric-cardinality.md`](09-metric-cardinality.md)
