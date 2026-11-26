# Driver reference

## http

- `http.request`: `method` (`GET`, `HEAD`, `OPTIONS`, `POST`, `PUT`, `PATCH`, `DELETE`), `path`, `query`, `headers`, a body and `trace` (true sends a fresh sampled `traceparent`); fact `status`, `headers`, `header_lists` (each value list split at commas), `body` or `body_text`, `body_truncated`, and `body_invalid`, why a body that looks or claims to be JSON wasn't read as JSON, and with `trace: true` `trace_id`. Binding `base_url`, `user_agent` (required) and `ready_path`.
- http limits: a response body over 10 MiB is ERROR `response_too_large`; redirects aren't followed, proxies are ignored and compression is off; a `HEAD` step asserts only status and headers; specifications can't set `Host`, `User-Agent`, `Authorization`, `Proxy-Authorization`, `Cookie`, `Content-Length`, `Transfer-Encoding` or `Connection`; a path with `?`, `#`, `\`, a space, a control character, `%2F`, `%5C` or a `.` or `..` segment is refused; a header name with capital letters in an expectation is refused, since facts hold them in lower case (HTTP-08); `set-cookie` is redacted.

A body that claims to be JSON but isn't valid JSON, or has a duplicate key, is kept as `$.body_text` with the reason in `$.body_invalid`, and every assertion on a `$.body` path then fails, `absent` and `never` included (HTTP-10).

A 2xx step must assert something besides its status (EXE-03). For a response without a body, such as a `DELETE` answering 204, assert that the body is absent with both paths, `absent: ["$.body", "$.body_text"]`: a JSON body is `$.body` and any other is `$.body_text`, so the pair fails on any body. Either path alone as the only other assertion is refused, since `absent: [$.body]` passes a text body. A 304 needs only its status, like every 3xx.

When an `equal` on `$.status` fails, the report shows the response's top-level `error` and `message`, or the start of the body, scrubbed and within `report.excerpt_bytes` (`response` in the JSON report). Progress lines name a table's row as `<table>/<case>`, with `tables[<n>]` for a table without an `id`.

An http request sends at most one body: `body` (JSON), `body_text` (text as written), or one of three forms for bytes that aren't JSON or text (HTTP-14):

```yaml
- { method: POST, path: /api/images, headers: { Content-Type: image/png }, body_base64: iVBORw0KGgoA//4A }
- { method: POST, path: /api/catalog/import, headers: { Content-Type: application/gzip }, body_fill: { bytes: 314572800, pattern: x } }
- { method: POST, path: /api/catalog/import, headers: { Content-Type: application/gzip }, body_file: fixtures/malicious-catalog.tar.gz }
```

- `body_base64` is standard base64 with `=` padding; spaces and line breaks are ignored, so a long value can be a YAML block. Validation refuses invalid base64, an empty body and more than 8 MiB.
- `body_fill` repeats a pattern of 1 to 64 bytes, `pattern` as text or `pattern_base64` for any bytes, to exactly `bytes`. The driver streams it, so a 1 GiB fill never sits in memory.
- `body_file` is a file inside `proctor/`, named relative to it, of 1 byte to 8 MiB. Its path can't be absolute, hold `..` or `${...}`, or end in `.yaml` or `.yml`, and neither the file nor a folder above it can be a symbolic link, even one that stays inside `proctor/` (INT-13). Proctor reads it from the commit or working tree it validates, records its sha256 in the report's `provenance.sha256`, reads it again before the step and refuses the step, sending nothing, if its bytes changed since validation.

These three need a `Content-Type` in `headers`. Every body is bounded by `max_body_bytes` in the http driver's policy (16 MiB when absent, at most 1 GiB); validation refuses one of these three over it, and a `body` or `body_text` over it is ERROR when sent, since its size is known only once values are inserted:

```yaml
drivers:
  http:
    official: http
    policy: { max_body_bytes: 314572800 }
```

The client sets `Content-Length` to the body's true length, so a route that refuses a large `Content-Length` before reading can be checked; specifications still can't set it. The report shows these bodies as their size and sha256 (with the file's path or the fill's pattern), never their bytes. `proctor diff` lists a raised `max_body_bytes` as `max_body_bytes_higher`, a step whose `body_file` now holds other bytes as a `body_file` change with both hashes, and a long `body_base64` as its size and sha256.

The http driver counts GET, HEAD and OPTIONS as reads and every other method as a write. An API whose reads are POSTs can declare those POST requests in the http driver's policy in `proctor.yaml` (HTTP-11):

```yaml
drivers:
  http:
    official: http
    policy:
      reads:
        - { method: POST, path: /api/search }
        - { method: POST, path: /api/quote }
```

A step whose method and path, as written in the specification, equal an entry is a read: it can be the state check after a write, `eventually`, `consistently` and `never` can poll it, HTTP-09 applies to it, and it can't carry `no_state_check`. The request body isn't matched. Entries take only method POST (PUT, PATCH and DELETE change state, so they can't be declared reads) and a path without a query string; a bad entry fails `validate` and `run` with exit 3. `proctor diff` lists each added entry as a `read_declared` weakening. Without `reads`, every request is judged by its method.

A write's state check must read what the write changed (EXE-07): a later read on the same driver of the same path, or of a path above or below it by whole segments (`GET /things/${id}` after `POST /things`), expecting a status that describes the resource (2xx, 3xx, 404, 409, 410, 412 or 423, not 400, 401, 403, 405, 422 or 5xx). A read that shows a write on another path says so with `shows`, on a `reads` entry or, for a GET, in the policy's own `shows` list, writing `{}` where a step's path has `${...}`:

```yaml
policy:
  reads:
    - { method: POST, path: /api/cart, shows: [/api/add-item, /api/remove-item] }
  shows:
    - { path: "/v1/users/{}/messages", acts: [/v1/messages/mark-read] }
```

`proctor diff` lists each added path as a `state_check_declared` weakening.

A POST that deletes or overwrites what it names can be declared in the policy's `destructive` list, so EXE-09 guards it like a DELETE:

```yaml
policy:
  destructive:
    - { method: POST, path: /api/remove-item }
```

Such a request still acts and needs its state check. Its target is its path and its body, so it is fixed unless one of them refers to `${run.id}` or a saved value. Entries take only method POST and a path without a query string, and can't also be in `reads`. `proctor diff` lists an added entry as `destructive_declared` and a removed one as a `destructive_removed` weakening.

Every entry must be used by a step of the listed specifications, or `validate` and `run` fail with exit 3 (HTTP-12), so an entry left behind after a route is renamed can't quietly apply to a route added later on its path. A `reads` or `destructive` entry is used when some step, a table's row included, sends that request. A `shows` path is used when, in one scenario, a read that shows it checks an act on that path: after the act as its state check (not necessarily the first one), or before a destructive act on a fixed target as its guard, with an expectation and an outcome that reads state. Drivers sharing a policy through `policy_from` share its uses.

## Streams

`http.open` and `http.frames` read a response that stays open, newline-delimited JSON (NDJSON) or server-sent events (SSE), the way `ws.connect` and `ws.messages` read a WebSocket (HTTP-15):

```yaml
steps:
  - id: open
    do: http.open
    identity: alice
    as: feed
    with: { path: /api/orders/feed }
    expect:
      equal: { $.status: 200, $.open: true, $.format: ndjson }
  - id: hello
    do: http.frames
    on: feed
    eventually: { within: 2s, every: 100ms }
    expect:
      any: { "$.frames": { json.type: hello } }
  - id: no-errors
    do: http.frames
    on: feed
    consistently: { for: 1s, every: 200ms }
    expect:
      none: { "$.frames": { json.type: error } }
      equal: { $.overflow: false }
```

- `http.open` acts and always sends GET, with `path`, `query` and `headers` as a request takes them, the binding's User-Agent and the identity's credential. It returns at the status and headers. `format: ndjson | sse` is optional: without it `text/event-stream` reads as SSE, `application/x-ndjson` and its siblings as NDJSON, and any other 2xx is ERROR naming the Content-Type. A non-2xx answer opens no stream; its fact has `open: false` and the body as a request's.
- `http.frames` answers at once with every frame since the stream opened: `count`, `frames`, `closed` and `close_reason`, `overflow` and `overflow_reason`, `body_text` (the first 1 MiB as received) with `body_truncated`, and for SSE `retry`. An NDJSON frame is a line, with `text` and `json` (or `json_invalid`); blank keep-alive lines aren't frames. An SSE frame is an event, with `event`, `data` (its `data:` lines joined with a newline), `id` and `json`; comments aren't frames. Wait for frames with `eventually`, `consistently` or `never`.
- The first 10,000 frames, 4 MiB of them and 1 MiB per frame are kept. Past a limit, or when the stream ends inside a frame, `overflow` is true and every claim on `$.frames` or `$.count` but `any` and `present` fails, naming the reason, since a frame left out could hold what `none` rules out. The same holds for `ws.messages`, which keeps the first 10,000 frames and 16 MiB of them.
- `http.frames`, `ws.messages`, `ses.sent`, `stub.received`, `otel.spans`, `otel.logs` and `browser.page` report what has arrived so far. `eventually` on one of them with only `none`, `absent`, `excludes`, a zero `count` or `length`, `equal` to false, 0 or an empty value, or `differs` from anything else is refused (exit 3, TIME-14): it would pass at the first sample, before anything arrived. Use `consistently` or `never`, or add a claim that waits, such as `equal: { $.closed: true }` or `length: { $.traces: 1 }`.
- The stream is closed at the end of the scenario, passed, failed or errored. A value at a sensitive path in a frame (`json.api_token`) is scrubbed from the frame's `text`, `body_text` and the excerpt too.

## ws

- `ws.connect` (with `as`): `path`, `query`, `headers`, `subprotocols`, `auth` (where the credential goes: `subprotocol`, the default, `query:<name>` or `header:<name>`) and `no_origin`; fact `connected`, `status`, `subprotocol`, `headers`, `body_text`, `body_truncated`. `ws.send` (with `on`) takes exactly one of `json` and `text`; fact `sent`. `ws.messages` (with `on`): `count`, `messages` (`type` `text` or `binary`, `text`, `json`), `closed`, `close_code`, `close_reason` and `overflow`, set after 10,000 frames or 16 MiB. Binding `base_url` (`ws`, `wss`, `http` or `https`), `origin` (sent as `Origin` unless `no_origin`) and `ready_path`; no policy.

## Browser checks

- `browser.open` (with `as`): `path`, `query`, `auth` (`header`, the default, as `Authorization`, `header:<name>` or `cookie:<name>`); fact `url`, `status`, `title`. `browser.fill` (with `on`): `selector`, `value`; fact `filled`. `browser.click` (with `on`): `selector`, `wait_for` (a CSS selector, or a path starting with `/`); fact `url`, `title`, `navigated`, `status`. `browser.page` (with `on`): `selectors`, at most 100 names (`^[a-z][a-z0-9_]{0,62}$`) of CSS selectors; fact `url`, `title`, `selectors.<name>` (`count`, `text`, `text_truncated`, `visible`, `attributes`), `console_errors` (`source` `console`, `exception` or `log`, `text`, `url`), `failed_requests` (`url`, `method`, `type`, `status`, 0 when none, `error`, `blocked`), `overflow`, `overflow_reason`. Binding `base_url`, `chrome_path` or `remote_url`, `ready_path`; policy `origins`. See [Browser checks](#browser-checks).

The `browser` driver checks a page the way a user sees it, in headless Chrome or Chromium driven over the DevTools protocol. It uses a Chrome or Chromium on `PATH` or in the usual places (`/Applications/Google Chrome.app` on macOS, `/usr/bin/google-chrome` or `/usr/bin/chromium` on Linux), the binding's `chrome_path`, or the browser at the binding's `remote_url`, a DevTools address such as `http://chrome:9222` in the compose file. Declare it as `web: { official: browser }`, and give it `base_url` (and optionally `ready_path`) in the binding:

```yaml
steps:
  - id: open
    do: web.open
    identity: anonymous
    as: page
    with: { path: /signup }
    expect: { equal: { $.status: 200, $.title: Sign up } }
  - id: name
    do: web.fill
    on: page
    with: { selector: "#name", value: "Ada ${run.id}" }
    expect: { equal: { $.filled: true } }
  - id: submit
    do: web.click
    on: page
    with: { selector: "button[type=submit]", wait_for: "#thanks" }
    expect: { equal: { $.navigated: true, $.status: 200 } }
  - id: saved
    do: db.query
    with: { connection: app, query: "select count(*) as n from users where name = $1", args: ["Ada ${run.id}"] }
    expect: { equal: { "$.rows[0].n": 1 } }
  - id: thanks
    do: web.page
    on: page
    with: { selectors: { thanks: "#thanks" } }
    expect: { equal: { $.selectors.thanks.text: "Thanks, Ada ${run.id}", $.selectors.thanks.visible: true } }
```

- `browser.open`, `browser.fill` and `browser.click` act on `page/<path>`, the path `open` was given (`page/signup` above, with `{}` for a `${...}`). `open` and `fill` change nothing on the server, so they need no state check and refuse `no_state_check` (EXE-07). A `click` does need one: `browser.page` reads the browser, not the application, so it never counts as a state check, and a click's state check is a read on another driver through a relation, such as `{ act: { driver: web, target: page/signup }, shows: [{ driver: db, target: app }] }`, or `no_state_check` with the reason, for a click that changes nothing on the server. TIME-14 applies to `browser.page` (see [Streams](#streams)).
- Each `open` gets its own tab in its own browser context, so handles share no cookies or storage. An identity's credential goes to `base_url`'s origin only: in the `Authorization` header by default (`Bearer <token>`), or `auth: header:<name>` or `auth: cookie:<name>` (an HTTP-only cookie for `base_url`'s host, which a browser sends to every port of that host). A bad `path` or query name is refused (BROWSER-01), and so is a bad `auth` or one without an identity (BROWSER-02).
- `fill` waits until the element is visible, clears it and types the value as text input, then fires `change`; a select or a date, time, color or range input gets the value set directly. It refuses buttons, checkboxes, radios and file inputs (click those), and a value over 1 MiB or with a control character other than a tab or a line break (BROWSER-05). `filled` says whether the field then holds exactly the value. `${secret.<name>}` and `${fill(N)}` may be used in a `fill` value, and nowhere else in a browser step (HTTP-02).
- `click` waits until the element is visible and clicks its center. `wait_for` waits for a CSS selector to be visible, or, starting with `/`, for the page's path under `base_url` to be that path (BROWSER-04 refuses an empty or bad one), until the step's timeout; past it the step is ERROR `timeout`. Without `wait_for`, a click that starts loading a page waits for that load. `navigated` says whether the main frame loaded a new document, and `status` is that document's status.
- The page may load only `base_url`'s origin and the policy's `origins` (`policy: { origins: [https://cdn.example.com] }`; a bad or repeated one exits 3, BROWSER-06). Every other request, a WebSocket and a new window included, is blocked and listed in `failed_requests` with `blocked: true`. A launched browser reaches the network only through the driver, which refuses other hosts and ports; with `remote_url` the requests are stopped in the browser, and a WebSocket to another origin can't be stopped, so it is listed with `blocked: false`. Downloads are refused, and dialogs are dismissed (an alert or a beforeunload is accepted).
- `failed_requests` also lists requests that answered 4xx or 5xx, or failed (the browser's own `/favicon.ico` request is left out). `console_errors` lists `console.error` and failed `console.assert` calls, uncaught exceptions and the browser's error messages, such as a CORS refusal. Both grow for the life of the handle and keep the first 1,000 entries.
- `text` is the first match's visible text (`innerText`), cut at 64 KiB with `text_truncated: true`, and a claim on a cut `text` fails (HTTP-13); an attribute value is cut at 4 KiB. A step names at most 100 selectors, each name matching `^[a-z][a-z0-9_]{0,62}$` (BROWSER-03). A cut, or a list past 1,000 entries, sets `overflow` with `overflow_reason`.
- A launched browser runs headless with a new profile in `TMPDIR`; the driver stops it and removes the profile when it closes. With `remote_url`, the driver opens and closes its own browser contexts and leaves the browser running; point it at a browser kept for testing, never at one with your own profile.

## postgres

- `postgres.query`: `connection`, `query`, `args`; fact `row_count`, `rows`. Policy `connections: { <name>: { schemas, objects } }`, with `objects` required and names lower-case and unquoted; binding `<name>_dsn_env`. Sessions run with `application_name=proctor` and `default_transaction_read_only=on`; connect and statement timeouts are 5 s; `NaN` and `Infinity` read as text; the role check also refuses `MAINTAIN` on PostgreSQL 17 and later.

A database check runs one parameterized, read-only query. Before using the verification role, Proctor checks that the role can only read the objects `proctor.yaml` declares and owns no object at all, types, domains, large objects, extensions and the other catalog objects included (DB-04).

## dynamodb and sqs

- `dynamodb.get` (`table`, `key`, `consistent`, default true; fact `found`, `item`), `dynamodb.query` (`table`, `index`, `key` with the partition key and at most the sort key, matched by equality, `limit` 1 to 1000; fact `count`, `items`; over 1,000 matches without `limit` is ERROR) and `dynamodb.put` (`table`, `item`; fact `written`). `table` is an alias from the policy's `tables: { <alias>: <table name> }`.
- dynamodb and sqs binding: `endpoint`, `region`, `ca_bundle` (a PEM file of at most 1 MiB, for https only), `access_key_env` and `secret_key_env` (static credentials only).
- `sqs.send` (`queue`, `body`, `attributes`, `delay_seconds` 0 to 900, and for a FIFO queue `group_id`, required, and `dedup_id`; fact `message_id`), `sqs.peek` (`queue`, `max` 1 to 1000, default 1000; fact `count`, `truncated`, `messages` with `message_id`, `body_text`, `body`, `attributes`) and `sqs.depth` (`queue`; fact `visible`, `in_flight`, `delayed`). Policy `queues: { <alias>: <queue name> }` and `peek_hold`. A peek stops after 150 receives, or when 3 receives in a row add nothing and it holds as many messages as the queue reports (20 in a row when it holds fewer); the step's summary says why it's `truncated`.

### SQS peeks

`sqs.peek` reads a queue's messages without deleting any. It receives with visibility timeout 0, and the pilot's emulator then returns the same oldest 10 messages to every receive, so a longer queue reads as `truncated: true`. To read past them, list the queue's alias in `peek_hold`:

```yaml
drivers:
  sqs:
    official: sqs
    policy:
      queues:
        dlq: orders-dlq
      peek_hold: [dlq]
```

A held peek hides each message it receives from every other consumer, your application's included, until about 2 s before the step's deadline and for at most 30 s, then sets every one visible again. If that fails, the step is ERROR and says how many messages stay hidden and for how long. `peek_hold` is off by default, takes only aliases from `queues`, and refuses FIFO queues, where one held message stops the rest of its group (exit 3, rule SQS-03). `proctor diff` lists turning `peek_hold` on, or adding a queue to it, as a `driver_policy_changed` weakening.

`sqs.peek` refuses a queue that has a redrive policy (rule SQS-04). Every receive, held or not, raises each message's `ApproximateReceiveCount`, which counts towards the policy's `maxReceiveCount`, so repeated peeks, for example under `eventually`, would move the queue's messages to its dead-letter queue. Before its first receive, a peek reads the queue's `RedrivePolicy`; if there is one, the step is ERROR (`invalid_request`, not retried), naming the dead-letter queue and `maxReceiveCount`, and nothing is received. Check such a queue with `sqs.depth`, and peek its dead-letter queue instead; `sqs.send` works on it as usual. `validate` can't see a redrive policy, since it doesn't contact SQS, so a `peek_hold` entry for such a queue is accepted and its peeks are refused at run time. Feature `sqs.peek_redrive_refused`.

## ses

- `ses.sent` (`to`, matched against to, cc and bcc, and `from`, both ignoring case; fact `count`, `emails` with `message_id`, `from`, `to`, `cc`, `bcc`, `reply_to`, `subject`, `text`, `html`, `sent_at`) reads `GET <endpoint>/__proctor/sent`; a 404 isn't retried and an answer over 15 MiB is ERROR. Binding `endpoint`, `ca_bundle`; no policy. `proctor-sim-ses [--addr 127.0.0.1:18193] [--identity SENDER]` answers SESv2 `POST /v2/email/outbound-emails` and `/__proctor/sent`, and keeps the newest 10,000 emails, each with at most 50 recipients and a 40 MiB raw message.

## HTTP fakes

`proctor-sim-http` stands in for an HTTP service your application calls, such as a vendor API, and the `stub` driver reads the requests it got and makes it fail on demand. Run one sim per faked service, each with its own `--addr`, route file and driver, such as `payments: { official: stub }` in `proctor.yaml` and `payments: { endpoint: "https://payments.sim.test:18194", ca_bundle: /tmp/sim/payments-ca.pem }` in the binding.

Keep the route file under `proctor/`, such as `proctor/sims/payments.yaml`, so it's protected and `proctor diff` shows its changes. It is strict YAML, like Proctor's own files: unknown or duplicate keys, anchors, aliases, merge keys, explicit tags and a second document are refused, and the sim exits 2 naming the line.

```yaml
# proctor/sims/payments.yaml
version: 1
redact: { headers: [x-vendor-signature], json_paths: [login.credentials.password], query: [token] }
routes:
  - id: vendor_login
    match:
      method: POST
      path: /v2/login.json
      body: { login: { credentials: { user: proctor } } }
    respond:
      headers: { Set-Cookie: "vendor-session=sim-token; Path=/" }
      json: { data: [{ login: { token: sim-token } }] }
  - id: refunds
    match:
      method: GET
      path: /v2/refunds.json
      query: { filter: { matches: 'eq\(refund\.state,"pending"\)' } }
      headers: { cookie: { matches: '.*vendor-session=sim-token.*' } }
    respond: { file: payments/refunds.json, headers: { Content-Type: application/json } }
  - id: charge_get
    match: { method: GET, path: "/v2/merchants/{merchant}/charges/{charge}" }
    respond: { json: { id: "{{path.charge}}", state: captured }, delay: 200ms }
```

- A route's `id` matches `^[a-z][a-z0-9_]{0,62}$`. `method` and `path` are required; `query`, `headers` (names in any case) and `body` narrow the match. A value is exact text or `{ matches: <RE2> }`, which must match the whole value, and extra names are allowed. `body` is a JSON subset: every key it lists must match, and lists and other values must be equal; a body that isn't JSON never matches.
- Paths are compared segment by segment, with escapes decoded within a segment, so `shop%2Feu` stays one segment; `{name}` is one whole segment. A path holds no query, and a path under `/__proctor/` is refused.
- The first matching route in the file wins, and two routes with the same `match` are refused. A request no route matches gets 404 with `X-Proctor-Sim: unmatched`, and is logged and counted in `unmatched`.
- `respond` takes `status` (200 to 599, default 200), `headers`, at most one of `json`, `text` and `file`, and `delay` (1ms to 10m). A `file` is relative to the route file's folder, without `..` or a symbolic link, at most 8 MiB, read once at start, and needs a `Content-Type`. An answer that always fails, such as a 500, is an ordinary route.
- `{{path.<name>}}`, `{{query.<name>}}` and `{{seq}}` (the request's number in the log) work in `json` values, `text` and header values; a value that is exactly `"{{seq}}"` becomes a number. Any other template is refused.

The sim logs every request before it answers. It writes `[redacted]` in place of `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-Auth-Token`, `X-Subject-Token`, `Private-Token`, `X-Api-Key` and what `redact` lists before it keeps anything, so a hidden value is never in the log or the report. To check that the application sent the right credential, match it in a route (`headers: { authorization: "Bearer sim-vendor" }`) and count that route's requests. Other bodies, such as forms, are kept as they are.

Every scenario shares the sim's log, so a scenario marks where it starts and reads after the mark. `stub.fail` opens a handle whose fault lasts until the end of the scenario, passed, failed or errored:

```yaml
steps:
  - id: login-down
    do: payments.fail
    as: payments_down
    with: { route: vendor_login, status: 503, json: { data: [] }, times: 2, for: 5m }
    expect: { equal: { $.active: true } }
    save: { mark: $.last_seq }
  - id: sync
    do: http.request
    identity: anonymous
    with: { method: POST, path: /api/sync }
    expect: { equal: { $.status: 502, $.body.error: upstream_unavailable } }
  - id: saw-fault
    do: payments.received
    with: { route: vendor_login, after: "${mark}" }
    expect:
      equal: { $.count: 2, $.unmatched: 0 }
      any: { $.requests: { fault: true, status: 503 } }
```

Without a fault, take the mark with a read: `do: payments.received`, `save: { mark: $.last_seq }`.

- `stub.received` takes `route`, `method`, `path` (the escaped path, without the query) and `after` (a number, or a saved `"${mark}"`), all optional. Its fact has `count`, `requests` (oldest first, each with `seq`, `at`, `route`, `method`, `host`, `path`, `query`, `headers`, `body` or `body_text`, `body_truncated`, `body_invalid`, `body_bytes`, `status` and `fault`), `unmatched` (requests no route matched after `after`, whatever the other filters), `last_seq`, `overflow` and `overflow_reason`. `status` is 0 when no answer was sent: a `reset`, or a client that gave up during a delay.
- `overflow` is true when requests in scope were dropped from the log, when more than 100 are in scope, or when an in-scope body was cut at 64 KiB. Then every claim on `$.requests` or `$.count` but `any` and `present` fails, naming the reason; narrow the read with `after`, `route` or `path`.
- `stub.fail` takes `route` (an id, or `"*"` for every request, unmatched ones included), then exactly one of `status` (with optional `headers` and `json` or `text`) and `reset: true` (the connection is reset without an answer), with an optional `delay` before either; `delay` alone gives a slow but normal answer, which with the application's timeout is a timeout. `times` (1 to 10,000) limits how many requests it changes, and `for` (1s to 1h, default 10m) how long it lasts, so a fault a stopped run left behind ends by itself. Its fact is `id`, `route`, `active`, `expires_at` and `last_seq`. The driver deletes the fault when the scenario ends, and any fault still open when it stops.
- A route has at most one active fault, and `"*"` overlaps every route: another fault on it is ERROR STUB-03, naming the active fault and its expiry. A `route` that isn't in the sim's route file is ERROR STUB-02 at run time, never a count of 0.
- Validation refuses a wrong combination of `fail`'s arguments (STUB-01), `stub.received` in setup (FIX-03), and `${fill(...)}` and `${secret...}` in either. `stub.fail` acts on `routes/<id>` (`routes` for `"*"`) and needs a later `stub.received` on the same driver as its state check (EXE-07); TIME-14 applies to `stub.received` (see [Streams](#streams)).
- A read on the sim counts as the state check of a request to the application only through a relation, declared when the application's code shows that the request calls that route: `{ act: { driver: http, target: /api/refunds }, shows: [{ driver: payments, target: routes/create_refund }] }`.

For HTTPS, `--tls-name payments.sim.test,127.0.0.1 --ca-out /tmp/sim/payments-ca.pem` makes a CA in memory, valid for 30 days, signs a certificate with those names, and writes only the CA certificate (mode 0644; a symbolic link there is refused). No key touches the disk. Make the application trust that file (for example with `SSL_CERT_FILE` or `NODE_EXTRA_CA_CERTS`), give it to the driver as `ca_bundle`, and reach the host name through a compose network alias or a hosts entry. `--cert FILE --key FILE` serves your own pair instead. TLS 1.2 is the minimum, the sim speaks HTTP/1.1 only, and client certificates aren't supported. The route file's sha256 is in `GET /__proctor/routes` and in the sim's first log line.

`POST /__proctor/reset` clears the log and the faults, for scripts between runs; `seq` keeps counting. Not supported: response sequences and state (for "the first call fails and the retry succeeds", use a fault with `times: 1`), record or proxy mode, OpenAPI-generated answers, XML, form and regex body matching, HTTP/2, streaming answers, WebSockets and reloading the route file.

### Operations and limits

- `stub.received` and `stub.fail` (with `as`; see [HTTP fakes](#http-fakes)) read `GET <endpoint>/__proctor/received` and post to `/__proctor/faults`; a 404 isn't retried. Binding `endpoint`, `ca_bundle`; no policy.
- `proctor-sim-http --routes FILE [--addr 127.0.0.1:18194] [--cert FILE --key FILE | --tls-name NAMES --ca-out FILE]` serves the route file and `/__proctor/routes`, `received`, `faults` and `reset`, logs one line per request without headers or bodies, exits 2 on any problem with the route file or flags, and stops within 5 s on SIGINT or SIGTERM.

- `proctor-sim-http` keeps the newest 10,000 requests and 128 MiB of them (bodies, headers and queries), the first 64 KiB of each body (it reads up to 16 MiB and counts the rest in `body_bytes`), and answers at most 100 requests (12 MiB) per read. A route's and a fault's `delay` are at most 10m, a fault's `for` at most 1h and its `times` at most 10,000; a `file` answer is at most 8 MiB.

## Telemetry

`proctor-sim-otel` is a local OpenTelemetry receiver, and the `otel` driver reads what it received: `otel.spans` for traces and `otel.logs` for log records. Declare `otel: { official: otel }` under `drivers`, and point both the application's OTLP exporter and the driver's binding `endpoint` at the receiver, such as `http://127.0.0.1:4318`.

`trace: true` on an `http.request` sends a fresh sampled `traceparent` (`00-<random trace id>-<random span id>-01`) and puts its `trace_id` in the fact. Save it and read that trace:

```yaml
- id: call
  do: http.request
  identity: alice
  with: { method: GET, path: /api/v1/orders, query: { customer: "c-${run.id}" }, trace: true }
  expect: { equal: { $.status: 200, $.body.count: 1 } }
  save: { trace: $.trace_id }
- id: one-trace
  do: otel.spans
  with: { trace_id: "${trace}" }
  expect:
    length: { $.traces: 1 }
    unordered: { "$.traces[0].services": [orders-api, orders-worker] }
    equal: { "$.traces[0].root_count": 1 }
  eventually: { within: 10s, every: 250ms }
- id: error-status
  do: otel.spans
  with: { trace_id: "${trace}", kind: server }
  expect:
    any: { $.spans: { status.code: error, "attributes['http.response.status_code']": 500 } }
  eventually: { within: 10s, every: 250ms }
```

- The sampled flag makes a parent-based sampler (the SDK default, `parentbased_always_on`, or `ParentBased(TraceIDRatioBased(…))`) keep the trace whatever its ratio. Validation refuses `trace: true` together with a `traceparent` in `headers`, and `$.trace_id` in `save` or `expect` without `trace: true` (exit 3). Without `trace: true` the request is sent as before.
- Every `otel.*` step must set `trace_id`, or filter on a value that holds `${run.id}` or a saved value (exit 3, OTEL-01): the receiver keeps every scenario's and every run's telemetry, so an unscoped read would match other people's records. `${run.id}` can't be a trace id (it isn't 32 hex characters), so without `trace: true` save a `trace_id` the application returns, or filter on an attribute that holds `${run.id}`.
- Both reads are verification steps, refused in setup (FIX-03), and count as an act's state check only through a `relations` entry (targets `spans` and `logs`). TIME-14 applies to both (see [Streams](#streams)).
- Exporters send in batches, so read with `eventually`. In the test environment set `OTEL_BSP_SCHEDULE_DELAY=200` and `OTEL_BLRP_SCHEDULE_DELAY=200` (the SDK defaults wait up to 5 s for spans and 1 s for logs), `OTEL_TRACES_SAMPLER=parentbased_always_on`, and `OTEL_EXPORTER_OTLP_ENDPOINT=http://<receiver>:4318` with `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf` or `http/json`. A `none` read straight after the act passes before anything arrived, so assert what must be there first.
- The receiver speaks OTLP/HTTP only. An exporter that uses gRPC sends through a test-only collector whose `otlphttp` exporter has `endpoint: http://proctor-sim-otel:4318` in its `traces` and `logs` pipelines; its batch processor adds its own delay (200 ms by default), so allow for it in `within`.
- The receiver never resets: start it in the environment's `up` script, and restart it to empty it. Once it refuses anything (a request it can't read, one too large or of another media type, or records past its limits), every later `otel.*` fact has `overflow: true` and an `overflow_reason`, and so does a read matching more than 1,000 records. Then every claim on `$.spans`, `$.logs` or `$.count` but `any` and `present` fails, since a refused record could be the one `none` rules out.
- `excludes` on a list forbids attribute names: `excludes: { $.attribute_keys: [authorization, cookie, password] }`.

After every sample of `otel.spans` or `otel.logs`, Proctor looks in the text of `$.spans` or `$.logs`, keys included, for every credential and secret it knows: each bound identity's token or variant, each declared secret the binding sets, and each binding `drivers.<name>.<key>_env` value, used by a step or not. A hit FAILs the step at once with reason `secret_in_telemetry`, without sampling again under `eventually`, `consistently` or `never`; the report names the path, such as `$.spans[0].attributes['url.query']`, and whose value it holds, and shows the value only as `[redacted]`.

### Operations and the receiver

- `otel.spans` (`trace_id`, `service`, `name`, `kind` `unspecified|internal|server|client|producer|consumer`, `status` `unset|ok|error`, `attributes` matched equal with their JSON type, `attribute_contains` text in a text attribute; fact `count`, `spans`, `traces` with `trace_id`, `span_count`, `services`, `root_count` and `error_count` over every received span of the trace, `services`, `attribute_keys`, `overflow`, `overflow_reason`) and `otel.logs` (`trace_id`, `service`, `severity_at_least` `TRACE` to `FATAL`, `body_contains`, `attributes`, `attribute_contains`; fact `count`, `logs`, `services`, `attribute_keys`, `overflow`, `overflow_reason`) read `POST <endpoint>/__proctor/spans` and `/__proctor/logs`. A span has `seq`, `received_at`, `service` (`unknown_service` without `service.name`), `scope`, `resource`, `attributes`, `trace_id`, `span_id`, `parent_span_id` (lower-case hex, `""` for none), `name`, `kind`, `status.code` and `status.message`, `start_time`, `end_time`, `duration_ms`, `events`, `links` and `dropped`; a log record `time`, `observed_time`, `severity_number`, `severity` (from the number, else the text in upper case), `severity_text`, `body` (text, or the JSON value), `trace_id`, `span_id` and `event_name`. Attributes are flattened (`parent.child` for nested maps, integers past ±2^53 as decimal text, bytes as base64); write a dotted key in brackets, `"$.spans[0].attributes['http.route']"`. `attribute_keys` lists the keys over the matched records, resource, event and link attributes included. Ready when `GET /__proctor/stats` answers as `proctor-sim-otel`; a 404, or an answer from something else such as a collector, isn't retried, and an answer over 15 MiB is ERROR. Binding `endpoint`; no policy.
- `proctor-sim-otel [--http 127.0.0.1:4318]` answers OTLP/HTTP `POST /v1/traces`, `/v1/logs` and `/v1/metrics` (metrics are counted and discarded) in `application/x-protobuf` or `application/json`, gzip or not. Any other media type, OTLP/gRPC included, or another encoding is 415, data it can't read 400 with a `google.rpc.Status` (in OTLP/JSON, ids must be hex, not base64), and a request over 64 MiB after decompression 413. It keeps the oldest 100,000 spans, 100,000 log records and 256 MiB of records, and refuses newer ones with a partial success (`rejectedSpans` or `rejectedLogRecords`), which clients don't retry. `GET /__proctor/stats` shows the counts, the refusals since it started and each service's last export.

## Docker checks

The `docker` driver reads containers, networks and volumes from Docker's Engine API, with six operations: `container`, `network` and `volume` take a `name`, and `containers`, `networks` and `volumes` take optional `labels` and `has_labels`. Its policy in `proctor.yaml` names the labels that make an object visible:

```yaml
drivers:
  docker:
    official: docker
    policy:
      scope:
        - { label: com.docker.compose.project, value: shop-proctor }
        - { label: com.example-shop.environment, value: shop-proctor }
```

An object is visible when it carries at least one listed label with exactly that value; everything else reads as not found, and a visible object's reference to one outside the scope (a network member, a network, a mounted volume) is shown without its name. The binding names Docker's Unix socket, with no default and no fallback to `DOCKER_HOST`:

```yaml
drivers:
  docker:
    socket: /var/run/docker.sock
```

A fact holds a fixed projection of each object's settings (user, read-only root, capabilities, security options, limits, restart policy, networks with their addresses, mounts, labels as `{ key, value }` lists), and never its environment, command, entrypoint, healthcheck command, log options or host paths. A bind shows only as `source: docker_socket` or `host_path`; it reads `docker_socket` (and the container `docker_socket: true`) when its destination is `/var/run/docker.sock` or `/run/docker.sock`, its source is a Docker socket (Docker Desktop's `/run/host-services/docker.proxy.sock` included) or a folder that holds one, and the rule errs towards `docker_socket`. Each step's `request_excerpt` lists every request it sent, inspects included. The report states `proof read_only: unproven`: Docker has no read-only socket, so the driver limits itself to GET requests on a fixed list of paths, and a run needs the same trust as `docker ps`. `proctor diff` lists a scope entry added or removed as a `docker_scope_widened` or `docker_scope_narrowed` weakening, and an added driver as `driver_added`.

A volume mount reads `source: docker_socket` (and the container `docker_socket: true`) when it's mounted at one of those socket destinations, or when its volume is a `local` bind (`-o type=none -o o=bind -o device=…`) whose device, or the mount's `subpath`, is a socket or a folder that holds one. The driver reads the device only to decide this and never shows it. A container that mounts a volume outside the scope, or a volume whose driver isn't `local`, makes the step ERROR (`unsupported_result`): the driver can't read a volume outside the scope and doesn't know other volume drivers, so it can't tell whether the mount reaches the socket, and guessing `false` could pass a check that should fail. Compose doesn't give the anonymous volumes it creates (an image's `VOLUME`, or a bare `- /path` under `volumes:`) the project label, so give the containers your specifications read named volumes instead.

The socket rule reads only paths, so Docker's socket under another name and destination (`-v /srv/x.sock:/tmp/y.sock`), or bound through a folder DKR-07 doesn't list, such as the home folder, reads `host_path`. The driver can't look at what a source path names: Docker reports only the path, and on Docker Desktop that path is resolved inside Docker's VM, not on the machine running Proctor. A check like "only one container holds Docker's socket" can then pass while another container holds it too, so also assert that a container has no host bind you don't expect:

```yaml
        expect:
          equal: { "$.container.docker_socket": false }
          none: { "$.container.mounts": { source: host_path } }
```

- `docker`: ready when `/_ping` answers `OK` and Docker accepts API 1.44; `labels` takes at most 20 keys, `has_labels` 1 to 20, and both together 39; the policy's `scope` takes at most 16 entries; more than 200 objects in scope is ERROR.

## Redis checks

The `redis` driver reads what the application stored in Redis, such as a cache entry, a session or a rate-limit counter, and when it expires: `redis.key` reads one key and `redis.keys` lists the keys that match a pattern. Both observe, and the driver sends only read commands. Its policy lists the key patterns, in Redis's glob syntax, that steps may read:

```yaml
drivers:
  redis:
    official: redis
    policy:
      keys: ["session:*", "ratelimit:*"]
```

```yaml
- id: session-stored
  do: redis.key
  with: { key: "session:${sid}" }
  expect:
    equal: { $.exists: true, $.type: hash, $.value.user_id: "${alice.id}" }
    differs: { $.ttl_ms: -1 }
- id: counters
  do: redis.keys
  with: { match: "ratelimit:${run.id}:*" }
  expect:
    equal: { $.count: 1, $.overflow: false }
```

- A `key` or `match` must lie inside one policy pattern whatever text each `${...}` stands for (exit 3, REDIS-03): `session:${sid}` is inside `session:*`, and `*${sid}` isn't. At run time the key, or the match with its values inserted, is checked again, and one outside is ERROR `invalid_request` with nothing read. Every pattern must be used by a step (REDIS-03), and a malformed `key` or `match` is refused (REDIS-01).
- A `redis.keys` match must hold `${run.id}` or a saved value (exit 3, REDIS-02), so it can't list other runs' keys. It uses `SCAN`, never `KEYS`, and stops at 1,000 keys or at the step's deadline with `overflow: true`; then every claim on `$.keys` or `$.count` but `any` and `present` fails.
- In `redis.key`'s fact, a string's `value` is text, with `value_json` when it parses as JSON (`"5"` gives `5`); a hash is an object; a list is an array in order; a set is a sorted array; a zset is an array of `{ member, score }` by score (`inf` and `-inf` as text). `size` is a string's bytes or the number of elements, and `ttl_ms` is −1 without an expiry. A missing key has `exists: false`, `type: none`, `value: null` and `ttl_ms: -2`. Another type, such as a stream, or a value that isn't UTF-8 is ERROR `unsupported_result`.
- A value over 64 KiB sets `value_truncated` and `overflow`. A list or zset keeps the elements before the cut, so every claim on the whole `$.value` but `any` and `present` fails; a string, hash or set reads `value: null`, so every claim on its content fails, and the step's response excerpt shows a string's first 4 KiB.
- Both reads are verification steps, refused in setup (FIX-03), and need an expectation (EXE-03). They count as an act's state check through a `relations` entry, such as `{ driver: redis, target: "key/session:{}" }`.
- `ttl_ms` changes between runs, so a transcript needs a `scrub` rule with `path: $.ttl_ms`.

The binding names the server and the user. The password comes only from the environment, never from YAML:

```yaml
drivers:
  redis:
    address: 127.0.0.1:6379
    username: proctor
    password_env: REDIS_PASSWORD
```

Instead of `address`, `sentinel: { addresses: ["127.0.0.1:26379"], master: mymaster }` finds the master through Sentinel. `db` selects a database, and `tls: true` connects over TLS (1.2 or later), trusting the system's roots or, with `ca_bundle`, the certificates of an absolute path to a PEM file. A bad binding exits 3 (REDIS-04).

At the start of a run the driver checks that its user can't write (`proof read_only (REDIS-05)`). With Redis 6 or later it reads the user's rules with `ACL WHOAMI` and `ACL GETUSER`, so the user needs `+acl|whoami +acl|getuser` besides `+@read`, and `+select` for a `db` other than 0. It reports `proven` when the rules allow only `@read` and a few connection commands and every key pattern lies inside the policy's patterns; selectors are checked the same way. A user that can do more, a user that may not run those two commands and a server without ACL are `unproven`, and the run goes ahead; an error from the check is `failed`, and the run stops (exit 4). Rules such as `+@all -@write`, which still allow `CONFIG SET` and `ACL SETUSER`, are `unproven`. A user that proves:

```sh
ACL SETUSER proctor on >… resetkeys ~session:* ~ratelimit:* -@all +@read +acl|whoami +acl|getuser
```

The ready check sends `PING` and counts a `NOPERM` answer to it as the server being up, so the user needs no `+ping`.

- `redis.key` (`key`; fact `exists`, `type`, `value`, `value_json`, `size`, `ttl_ms`, `value_truncated`, `overflow`, `overflow_reason`) and `redis.keys` (`match`; fact `count`, `keys`, `overflow`, `overflow_reason`). Policy `keys`, 1 to 64 patterns of at most 1,024 bytes without `${...}`; binding `address` or `sentinel` (`addresses`, at most 16, and `master`), `db`, `username` (needs `password_env`), `password_env`, `tls` and `ca_bundle` (at most 1 MiB, with `tls` only). Connections time out after 3 s, commands after 5 s or the step's deadline; a key that changes type while it is read is read again once. See [Redis checks](#redis-checks).

## CLI checks

The `cli` driver runs a command the application ships, such as an admin tool or a migration status check, and checks its exit code and output. Its policy declares each command by name:

```yaml
drivers:
  cli:
    official: cli
    policy:
      commands:
        user-count:
          program: proctor/checks/user-count.sh
          kind: observe
          targets: [users]
        reset-password:
          program: bin/admin
          args: [users, reset]
          kind: act
          targets: [users]
          extra_args: 1
          env: [DATABASE_URL]
          timeout: 30s
```

```yaml
- id: count
  do: cli.run
  with: { command: user-count }
  expect:
    equal: { $.exit_code: 0, $.stdout_json.count: 1 }
```

- `program` is a name looked up on `PATH`, an absolute path, or a path from the repository root; `args` are fixed, and a step adds at most `extra_args` more (0 by default), after them. `cli.run` takes `command`, `args` and `stdin` (text, at most 1 MiB), and its kind is the command's `kind`, so one driver both acts and observes. An undeclared command, too many arguments, and `${secret...}` or `${fill(N)}` in `args` or `stdin` are refused (exit 3, CLI-03).
- An `act` command needs `targets` and a later state check like any act (EXE-07), through a cli `observe` command with one of those `targets` or a relation; an observe command without `targets` reports `command/<name>`. `destructive: true` puts an act under EXE-09, its target being each target with the step's arguments. Every step expects `$.exit_code` in `equal` (CLI-05), and an observe step something more (EXE-03). Every declared command must be used by a step (CLI-01).
- The program runs directly, never through a shell, in the repository root or `dir` below it, with only `PATH`, `HOME`, `TMPDIR` and the variables it lists in `env`. Each listed variable's value comes from the binding, as `<NAME>_env: <VARIABLE>` (`DATABASE_URL_env: APP_DATABASE_URL`), and is scrubbed from the report; a listed variable without one, or one no command lists, is refused (exit 3, CLI-02).
- Proctor gives the driver the repository: the checkout for `run` and authoritative `run`, and the mutant's copy for `mutate`, so an act runs the mutated build. A binding that sets `drivers.<name>.repository` exits 3.
- An `observe` program must be outside the repository, or under the protected root or a `protected_paths` folder, reached without a symbolic link inside the repository, and so must every file a fixed or step argument names, so a pull request can't change what the check reports. `validate` doesn't check this; `run` refuses it before sending anything (exit 3, CLI-04), and the driver checks again before every call. An `act` program may be the application's own build output.
- Each command runs in its own process group, killed at the step's deadline or the command's `timeout` (at most 10m), whichever comes first, and again when it exits, so nothing it started outlives the step. The fact is `command`, `exit_code` (-1 when a signal stopped it), `duration_ms`, `timed_out`, `stdout_text`, `stdout_json` when stdout is one JSON value, `stderr_text`, `stdout_truncated` and `stderr_truncated`, and `overflow` with `overflow_reason` when a stream passes 1 MiB, of which the first 1 MiB is kept; a claim on a cut text fails (HTTP-13). For approved transcripts, scrub `$.duration_ms`.
- The report has `proof observe_programs: proven` and `sandbox: unproven`: a command runs as Proctor's user, with that user's files and network.

- `cli.run` (`command`, `args` up to the command's `extra_args`, `stdin` up to 1 MiB; kind from the command; fact `command`, `exit_code`, `duration_ms`, `timed_out`, `stdout_text`, `stdout_json`, `stderr_text`, `stdout_truncated`, `stderr_truncated`, `overflow`, `overflow_reason`). Policy `commands: { <name>: { program, args, kind, targets, destructive, extra_args (0 to 64), dir, env, timeout (at most 10m) } }`, at most 100 commands; binding `<NAME>_env` for each variable a command lists, and `repository`, which Proctor sets. Output is cut at 1 MiB per stream; process groups are Unix only, elsewhere only the program is killed. See [CLI checks](#cli-checks).
