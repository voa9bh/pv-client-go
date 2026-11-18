# Go PVClient

A plain TCP PV text client, using only the Go standard library, plus an optional
tview terminal object browser. No Java/PVServer object hierarchy is required.
`PVClient` is a struct in the importable `PVClient/pvclient` package, used by both
executables.

```text
main.go          command-line request example
pvclient/        shared socket client, protocol, and tests
cmd/pvbrowser/   tview object browser, metadata inspector, and command editor
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
go run ./cmd/pvbrowser localhost 5002
# Optional BigData negotiation:
go run ./cmd/pvbrowser -big localhost 5002
# Built executable:
pvbrowser localhost 5002
```

In GoLand, create a **Go Build** run configuration with **Run kind: Package**,
target `PVClient/cmd/pvbrowser`, and program arguments
`localhost 5002`. Do not put `.` or `go run` in program arguments.
The browser no longer accepts `-addr`; optional flags precede host/port.
IPv6 is supported, for example `pvbrowser ::1 5002`.
The original CLI keeps its `-addr` syntax and remains runnable with `go run .`.

The browser connects in the background and initially loads `/pvServer`'s direct
children. It enumerates each parent with `getNextChildObject(0)`, then
`getNextChildObject(previousID)` until zero, fetching each name via `#ID.name`.
Nested lookups use `#parentID`, including negative IDs. The root has no invented
numeric ID. Children appear in server order and are loaded only when expanded.

| Key | Action |
|---|---|
| Tab / Shift+Tab | Cycle tree, member list, Input/Output, shared information |
| F2 | Switch Objects / Macros (placeholder only) |
| Up / Down in tree | Select an object |
| Enter in tree | Expand/collapse; load children on first expansion |
| Right / Left in tree | Expand/load, or collapse/select parent |
| `r` in tree | Reload selected children and metadata |
| `R` in tree | Reconnect and rebuild, discarding cached IDs |
| Up / Down in member list | Select property/method and show its information |
| `g` in member list | Get selected property; never invokes a method |
| `s` in member list | Edit a blank property value |
| `c` in member list | Edit blank parameters for the selected method |
| Enter in input field | Submit once and immediately return to the selected member |
| Esc in input field | Cancel editing without sending |
| `q` in tree / Ctrl+C anywhere | Cancel outstanding work and quit |

The Objects workspace has a tree on the left, a combined member list on the
right, and an Input/Output region below that list. Properties come first;
a nonselectable dashed divider separates methods, which display with `()`.
One shared information panel below both columns follows tree or member focus.
The Macros workspace is a placeholder with no storage or execution.

The browser fetches the selected object's `.classHandle`, then
`.getPropertyInfos(classHandle)` and `.getMethodInfos(classHandle)`.
The list shows names only. The shared information panel shows property handle,
name, description, security, dimension, and datatype; for methods it shows handle,
name, description, security, request format, and response format. Tab to this
panel to scroll long descriptions with arrow/Page keys. Tree focus shows object
name/address, class handle, and path. No root ID is invented; object descriptions
are marked unavailable because no object-description query is assumed.
The six count-prefixed lists are parsed without dropping empty format strings.
Malformed lists are reported explicitly; the Java text format does not escape
semicolons embedded in descriptions, so ambiguous metadata cannot be reliably
recovered. A failed list does not hide a successful other list.

Selecting a member never executes it. **g** sends `#ID.propertyName` for a
property. **s** (property) or **c** (method) focuses a fresh blank input: enter only
the new property value or comma-separated method parameters, not a full command. Enter constructs
`#ID.propertyName=newValue` or `#ID.methodName(parameters)` and submits once.
The root uses `/pvServer` instead of an ID. The selected target is captured when
editing starts. String quotes are optional as supported by the server;
nonempty input is passed verbatim. Empty property input sends `=""`, an explicit
empty string, while empty method input sends `()`. Esc cancels an unsubmitted
edit; F2 also abandons an edit before changing workspace.

**s or c followed by Enter can modify the server or invoke destructive methods.**
Merely browsing, selecting a row, or editing does not execute anything.
No client-side security enforcement or automatic property-value polling is added.
Requests are not replayed after reconnect. A failed/canceled request
may already have taken effect on the server. Reconnect/quit cancels the local
wait, not execution on the server. After commands that change the object tree,
use `r` or `R` explicitly; commands are never rerun as part of refreshing.

Input/Output retains the submitted expression and success/error response,
including empty results. Focus returns to the member immediately on submission,
without blocking for a reply. Further get/set/call submissions are blocked while that
operation is pending. Changing members/objects discards obsolete edits/results
and cancels a pending local wait, warning that the operation may already have
executed. These shortcuts use unmodified lowercase letters only in the member
list; `g`, `s`, `c`, `q`, `r`, and `R` are ordinary text in the input.
F5/F6 are no longer bound. F2 still switches workspaces.

For object selection, the shared panel shows name, ID address, and constructed path.
Paths are informational, not filesystem paths; names containing `/` or other
special syntax may require server-specific escaping for path-based requests.
Control characters are replaced for display and server names cannot inject
tview color tags.

Networking never runs on the UI event loop. Each child-list operation has a
30-second overall budget; errors are displayed with retry guidance. A failed
refresh retains the previous snapshot, explicitly marked error/stale, rather
than presenting a partial or empty success. Repeated/cyclic IDs are errors.
There is no permanent diagnostics pane. New client warnings appear in the status
line; query failures appear in status or Input/Output instead of overwriting
terminal rendering.

Status reflects the **last browser operation**, not live connection monitoring.
The client still automatically reconnects after socket loss, but a server restart
may invalidate cached IDs: use `R` to rebuild. Interrupted queries are not resent
automatically. Browsing and metadata queries remain read-only, in addition to the
client's normal `!context off` and optional `!big on` initialization; explicitly
submitted commands are not restricted to read-only operations.

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
use a simulated terminal to exercise keyboard navigation, metadata, explicit
command submission, lazy loading, errors, refresh/reconnect, and cancellation
without requiring an interactive terminal.

```sh
go test -race ./...
go vet ./...
```
