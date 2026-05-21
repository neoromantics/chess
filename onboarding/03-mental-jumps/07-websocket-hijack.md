# 3.7 WebSockets and the middleware-composition gotcha

WebSockets are HTTP upgrades: the request comes in as HTTP, the server returns `101 Switching Protocols`, and then the *underlying TCP socket* is handed to the WebSocket library, which speaks its own framing on top.

The "hand the socket over" step happens via the `http.Hijacker` interface. The library calls `Hijack()` on the `ResponseWriter`, gets the raw TCP connection back, and goes from there.

Here's the gotcha. Suppose you write a middleware that wraps `http.ResponseWriter` so you can record the status code for metrics:

```go
type statusRecorder struct {
    http.ResponseWriter
    status int
}
func (r *statusRecorder) WriteHeader(code int) {
    r.status = code
    r.ResponseWriter.WriteHeader(code)
}
```

That embedding only exposes the methods on `http.ResponseWriter`. It does **not** automatically expose `Hijack()` even if the underlying writer implements it. So when `gorilla/websocket` does the upgrade, it sees your wrapper, type-asserts to `http.Hijacker`, fails, and every WebSocket connection dies with `websocket: response does not implement http.Hijacker`.

The fix is explicit forwarding:

```go
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
    h, ok := r.ResponseWriter.(http.Hijacker)
    if !ok { return nil, nil, errors.New("...") }
    return h.Hijack()
}
```

Same for `Flush()` (needed for SSE). This is in `pkg/metrics/metrics.go`. Read the comment block above `statusRecorder` — every middleware in this codebase must do this forwarding, or every live update silently breaks.

The general lesson: when you wrap an interface in Go, embedded methods are exposed but **non-interface methods of the concrete type are hidden**. If anyone downstream does a type assertion, your wrapper has to forward.

Reference: [`../../docs/invariants.md`](../../docs/invariants.md) (HTTP & WebSocket plumbing section).

---

← [`06-passwords.md`](06-passwords.md) · Next: [`08-cswsh.md`](08-cswsh.md)
