# Go PVClient

A plain TCP PV text client, using only the Go standard library. No Java/PVServer
object hierarchy is required. `PVClient` is a struct in package `main`, callable
from `main.go`.

Run against your server with commands appropriate for that system:

```sh
go run . -addr localhost:5002 'your-command' 'another-command' 'third-command'
```

The first command blocks for its reply; remaining commands are submitted as
concurrent futures and printed in submission order. Responses can arrive in any
order. Use `go run .`, not `go run main.go`, to include all source files.
The module declares Go 1.27.

```go
client, err := NewPVClient("localhost:5002", Options{})
if err != nil {
    return err
}
defer client.Close()
if err := client.Connect(ctx); err != nil {
    return err
}

reply, err := client.AskTxt("your-command")
// Or AskTxtContext(ctx, command) for caller cancellation.

future, err := client.AskTxtAsync(ctx, "another-command")
if err != nil {
    return err
}
id := future.FutureID()
reply, err = future.Wait() // May be called repeatedly or concurrently.
// future.Cancel() cancels the local wait, not execution on the server.
```

Handle `err` at each call site. A server error is a `*RemoteError` retaining its
original text and numeric code when present (`-1` if unparseable); `errors.As` can inspect it.
Network, protocol, cancellation, and lifecycle failures are returned as errors.

## Connection and request behavior

Call `Connect(ctx)` first. Each connection sends `!context off` before becoming
ready. Once connected, socket loss triggers automatic reconnect attempts;
`Reconnect(ctx)` also replaces the connection explicitly. Initial connection
failure is returned without starting an endless background retry loop.
`Close()` stops reconnection, interrupts socket operations, and resolves pending
futures. The client cannot be reopened after Close.

| Option | Default |
|---|---|
| `ConnectTimeout` | 2.5 seconds, including initialization |
| `ReconnectInterval` | 2.5 seconds |
| `RequestTimeout` | 30 seconds, including submission/reconnection |
| `MaxMessageSize` | 16 MiB, including NUL termination |
| `BigData` | false |
| `ResendAfterReconnect` | false |

Zero-valued durations/size select defaults; invalid negative values are rejected.
Caller deadlines can shorten these limits. Options are copied at construction.
Connection failures during automatic retry are logged with `log/slog`.

There are 32 concurrent futureID slots (1–32); zero is reserved for initialization.
Submission waits for a slot and socket writer, subject to cancellation/deadline.
During reconnect, new requests wait for readiness within their original timeout.
A timed-out/canceled sent request keeps its ID reserved until its late response is
drained or the connection is replaced, preventing a late reply from completing
an unrelated request. If an unresponsive server exhausts all slots, explicitly
reconnect to reset them. IDs can be reused after completion; they are not globally
unique request identifiers.

With `ResendAfterReconnect: false`, transport-interrupted requests fail with
`ErrDisconnected`. With `true` (CLI: `-resend`), they are resent after reconnect,
using the original futureID and deadline. **A command may have already executed:
resending can execute it more than once.** Server errors, malformed protocol
messages, expired/canceled requests, and Close are never retried. Manual
reconnection uses the same policy.

## Wire compatibility and boundaries

The client uses the active Java Netty encoder's layout: packet type, flags,
telegram type, response type, fragment status, futureID, then a little-endian
payload length (2 bytes normally, 4 bytes with the BigData flag). In particular,
byte 5 is the futureID; the legacy Java `PVCPackage.getBytes()` clears that byte.

Text is UTF-8 with a trailing NUL. Standard payloads are fragmented at 1008 bytes;
responses are reassembled as bytes before decoding. Responses end at the first
NUL, matching Java, without stripping meaningful whitespace. Response type zero is accepted
as success for Java's legacy system-command compatibility.

`BigData: true` (CLI: `-big`) negotiates `!big on` on every connection and sends
single BigData requests. Rejected negotiation is an explicit connection error.
Both standard and BigData replies are understood, subject to `MaxMessageSize`.
The 16 MiB default deliberately bounds the protocol's much larger theoretical
limit.

No TLS, WebSockets, binary/JSON calls, authentication, or server-event API is
included. Unsolicited ASY messages are logged as ignored and cannot complete a
future. The server must support futureID multiplexing.

Local mock TCP tests verify framing and lifecycle behavior; a real PVServer is
needed to establish interoperability with your specific target.

```sh
go test -race ./...
go vet ./...
```
