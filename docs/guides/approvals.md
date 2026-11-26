# Approved transcripts and logs

## Approved transcripts

A transcript is a scenario's whole story: each step's interpolated request and the fact that came back, setup included. You read and approve it once; later runs compare their transcript with the approved one byte for byte. It adds a check and replaces none: the scenario's explicit assertions stay the reviewed contract.

```yaml
scenarios:
  - id: RPT-001
    name: a created report reads back
    transcript: true
    steps: [...]
tables:
  - id: KIND-T01
    transcript: true          # one transcript per row
```

```yaml
# proctor/proctor.yaml
transcripts:
  max_bytes: 1048576          # the default; at most 8 MiB
  scrub:
    - { name: time, pattern: '[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z' }
    - { name: request_id, pattern: 'req-[0-9a-f]{12}' }
    - { name: date, path: '$.headers.date', numbered: false }
    - { name: date_parts, path: '$.header_lists.date', numbered: false }
```

Before comparing, Proctor scrubs each step's `with` and `fact`: credentials, secrets and redacted fields become `[redacted]`; the scenario's run id, saved values and identity values become `${run.id}`, `${name}` and `${alice.id}`; then each `scrub` rule replaces what it matches with `<name-N>`, numbered by first appearance so equal values stay visibly equal (`numbered: false` makes every match `<name>`, for values like an HTTP `Date` that repeat only sometimes). A `path` is read in each step's fact; the http driver holds each header twice, in `headers` and `header_lists`, so name both or use a `pattern`. Run twice and compare the received files before approving: anything that still differs needs a rule.

The flow:

1. `proctor run --binding …` fails a scenario with no approved transcript as `transcript_not_approved`, and writes what it saw to a new private folder outside the repository, printed as `Received:` (default `$TMPDIR/proctor-received/<project>/<run id>/`, or `--received DIR`, which must be new or empty).
2. Read each received file. `proctor approve RPT-001` (or `--all`) shows the diff and asks `[y/N]` per file; `y` writes `proctor/approved/<spec id>/<scenario id>.transcript.yaml`.
3. Commit the approved files. They're protected inputs, so the pull request needs code-owner review: that review is the approval. `proctor approve` can't tell whether a person answered.
4. Later runs FAIL `transcript_changed` with a short diff when the story changes, and ERROR `transcript_too_large` over `max_bytes`. A scenario's own FAIL or ERROR stands.

`approve` refuses when `CI` or `GITHUB_ACTIONS` is set, when stdin isn't a terminal and `--yes` isn't given, when any specification, the configuration or the `transcripts` settings changed since the run, when a received or approved file changed since the run, and for a scenario that failed on its own. Validation refuses a file under `proctor/approved/` that no opted-in scenario owns (INT-19), a link anywhere there, an approved file over `max_bytes` or without its format line, and two ids that differ only in letter case. `proctor diff` lists `transcript_dropped`, `scrub_widened` and each changed approved file with the lines changed. Authoritative mode reads the approved files from the tested commit and writes nothing under the checkout.

## Approved logs

An approved log is the part of the application's log a scenario produced, cut to the fields you choose and scrubbed, that a person read and approved. Later runs compare their log with it byte for byte. It needs the app-log check (`app_logs` in `proctor.yaml`, and `app_logs.file` or `app_logs.otel` in the binding), and it allows nothing: an ERROR line still fails the scenario, whatever the approved log holds. With `app_logs.otel`, records from several processes interleave, so use `order: sorted`.

```yaml
scenarios:
  - id: RPT-001
    approved_log: true
tables:
  - id: KIND-T01              # a table needs an id; one log for the whole table
    approved_log: true
```

```yaml
# proctor/proctor.yaml
app_logs:
  format: logfmt
  approved:
    fields: [method, path, status, user, request_id, took]   # required; every other field is left out
    order: written                         # or sorted, for lines written concurrently
    max_bytes: 1048576                     # the default; at most 8 MiB
    scrub:
      - { name: req, field: request_id }   # the whole value
      - { name: dur, field: took, pattern: '[0-9.]+ms' }
      - { name: time, pattern: '[0-9]{2}:[0-9]{2}:[0-9]{2}' }
```

Each line is `<LEVEL> <msg> k=v…` with the listed fields in their order; `panic:` and `fatal error:` lines are `RUNTIME <line>` without their stack trace; lines that don't parse aren't shown and are counted in the last line, `unparsed lines: N`. Scrubbing is a transcript's (secrets and credentials, then `${run.id}`, saved values and identity values), plus `[redacted]` for a field or nested key with a sensitive name (`password`, `token`, `secret`, `api_key`, `authorization`, `cookie`), then the `scrub` rules. A received log looks like:

```text
format: "proctor-log/1"
INFO "report created" method=POST path=/reports/${report_id} status=201 user=${alice.id} request_id=<req-1> took=<dur-1>
RUNTIME panic: runtime error: index out of range [3] with length 2
unparsed lines: 1
```

The flow is the transcripts' flow: the first run FAILs `log_not_approved` and writes `<spec id>/<id>.log` to the `Received:` folder; read it, `proctor approve RPT-001` (an id names both a scenario's transcript and its log), commit `proctor/approved/<spec id>/<id>.log` for code-owner review; later runs FAIL `log_changed` with a diff. `approve` refuses a log whose scenario didn't pass on its own, the app-log check included. `proctor diff` lists `approved_log_dropped`, `scrub_widened` (a field no longer listed, a rule added or changed) and `log_order_relaxed` (`order` became `sorted`).

To compare a production incident with a scenario: take the incident's lines from production with your own tools; write a scenario that sends the same request as the same kind of identity, with `approved_log: true`; run it; then

```sh
proctor logs normalize excerpt.log --field request_id=8f2c > excerpt.normalized.log
diff excerpt.normalized.log "$RECEIVED/users/RPT-001.log"
```

and change the scenario until the only difference is the defect (or nothing, once it's fixed), then approve. `normalize` prints to stdout only and never writes a file. Proctor doesn't know production's secrets, so only the field list, the sensitive names and your scrub rules keep them out of its output: read it before you share it. Proctor never fetches production logs, and an approved log always comes from a test run.
