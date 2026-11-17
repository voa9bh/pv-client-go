# Go PVClient

A plain TCP PV text client, using only the Go standard library, plus an optional
tview terminal object browser. No Java/PVServer object hierarchy is required.
`PVClient` is a struct in the importable `PVClient/pvclient` package, used by both
executables.

```text
main.go          command-line request example
pvclient/        shared socket client, protocol, and tests
cmd/pvbrowser/   read-only tview object browser
```

Run against your server with commands appropriate for that system:

```sh
go run . -addr localhost:5002 'your-command' 'another-command' 'third-command'
```

The first command blocks for its reply; remaining commands are submitted as
concurrent futures and printed in submission order. Responses can arrive in any
order. Use `go run .`, not `go run main.go`, to include all source files.
The module declares Go 1.27.

```go
// Import "PVClient/pvclient" in your Go file.
client, err := pvclient.NewPVClient("localhost:5002", pvclient.Options{})
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

## Terminal object browser

```sh
go run ./cmd/pvbrowser -addr localhost:5002
# Optional BigData negotiation:
go run ./cmd/pvbrowser -addr localhost:5002 -big
```

In GoLand, create a **Go Build** run configuration with **Run kind: Package**,
target `PVClient/cmd/pvbrowser`, and program arguments
`-addr localhost:5002`. Do not put `.` or `go run` in program arguments.
The original CLI remains runnable with `go run .`.

The browser connects in the background and initially loads `/pvServer`'s direct
children. It enumerates each parent with `getNextChildObject(0)`, then
`getNextChildObject(previousID)` until zero, fetching each name via `#ID.name`.
Nested lookups use `#parentID`, including negative IDs. The root has no invented
numeric ID. Children appear in server order and are loaded only when expanded.

| Key | Action |
|---|---|
| Up / Down | Select an object |
| Enter | Expand/collapse; load children on first expansion |
| Right | Expand/load selected object |
| Left | Collapse, or select its parent if already collapsed |
| `r` | Reload selected children, discarding cached descendants on success |
| `R` | Reconnect and rebuild from the root, discarding cached IDs |
| `q` / Ctrl+C | Cancel outstanding work and quit |

The details pane shows the selected name, ID address, and constructed path.
Paths are informational, not filesystem paths; names containing `/` or other
special syntax may require server-specific escaping for path-based requests.
Control characters are replaced for display and server names cannot inject
tview color tags.

Networking never runs on the UI event loop. Each child-list operation has a
30-second overall budget; errors are displayed with retry guidance. A failed
refresh retains the previous snapshot, explicitly marked error/stale, rather
than presenting a partial or empty success. Repeated/cyclic IDs are errors.
Client logs appear in the diagnostics pane instead of overwriting the terminal.

Status reflects the **last browser operation**, not live connection monitoring.
The client still automatically reconnects after socket loss, but a server restart
may invalidate cached IDs: use `R` to rebuild. Interrupted queries are not resent
automatically. There is no command console, property editor, polling, or eager
whole-server scan; the browser reads names/children only, in addition to the
client's normal `!context off` and optional `!big on` initialization.

Build each executable separately (both are named `main` internally):

```sh
go build -o pvcli .
go build -o pvbrowser ./cmd/pvbrowser
```

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
needed to establish interoperability with your specific target. Browser tests
use a simulated terminal to exercise keyboard navigation, lazy loading, errors,
refresh/reconnect, and cancellation without requiring an interactive terminal.

```sh
go test -race ./...
go vet ./...
```
