# Proctor — Runner Evaluation

**Question:** Should Proctor's execution backend be Venom, Hurl, Step CI, or a minimal runner built for Proctor?
**Checked:** 2026-11-14, by reading each project's source and documentation. Nothing was executed; "Observed" below means seen in the source code, not seen running.
**Status:** Decided on 2026-11-14 by the product owner: Proctor executes specifications with its own runner. This is the Phase 1 decision record in `prd-revised.md`. The evaluation below is kept as the record of what was examined and what Proctor learned from it.

## Decision

**Proctor has its own runner.** No other test runner is used as a library, a subprocess or a backend, and no code is copied from one. Other tools are a source of ideas only; "Ideas Proctor takes" below lists them, and "No copied code" sets the rule.

Why this holds up against the evaluation:

- **No existing runner can serve through its CLI.** Each one lets a test file skip checks, redirect its target, or exit in ways the integrity layer can't trust (Findings).
- **Wrapping wouldn't save much.** Revision 2 already puts scheduling, tables, setup, interpolation and trace context in Proctor's core (`prd-revised.md` §19), and Proctor needs its own PostgreSQL, Temporal and log executors whatever the choice. A wrapped Venom or Hurl would have contributed one HTTP request and some assertion helpers.
- **It removes a dependency the integrity layer would have to police.** Venom's `lib/` executors, Hurl's in-file options and every backend's retry options would each have needed a rejection rule.

What it costs: Proctor writes its HTTP execution, JSONPath evaluation (through a library, OD-10), assertions, captures and interpolation, and that code starts less exercised than Venom's or Hurl's. The test suite and the example applications in three languages (`prd-revised.md` §17) are where it gets exercised.

Phase 0 no longer runs the Venom proof of concept or the native-runner comparison planned in the earlier version of this document; the decision makes their outcome irrelevant. The decision rule set for them was not applied.

## Context

The integrity layer in `prd-revised.md` needs these properties from whatever executes the specifications. A candidate that can't provide one, even through wrapping, is out.

| ID | Mandatory property | Source in `prd-revised.md` |
|---|---|---|
| M1 | Every required scenario has a stable, unique ID and a per-ID result | INT-02, INT-03 |
| M2 | Nothing in a specification can skip a required scenario, or the wrapper can detect it | INT-03, INT-04 |
| M3 | Failures, errors and invalid input are distinguishable | REP-07, REP-08 |
| M4 | Specifications can't redirect the target or connections set by the trusted binding | §5.2, §17.1 |
| M5 | No shell or code execution from specifications | SEC-06 |
| M6 | Parameterized, read-only PostgreSQL queries | DB-01, DB-02 |
| M7 | Bounded polling; `consistently` and `never` can be added | §11.9 |
| M8 | Temporal and JSON-log checks can be added without forking | §11.6, §11.8 |
| M9 | The pinned version can be verified | SEC-11 |
| M10 | Per-step evidence for reports | §13 |

**Baseline (doing nothing):** a team can already combine Hurl or Venom with CODEOWNERS on its test directory and a required status check. That covers part of outcomes O-1 and O-2 in `prd-revised.md`. It lacks required-ID enforcement, provenance, evidence levels, `consistently`, Temporal and log checks, and any protection against a test file that skips or redirects itself. Those gaps are Proctor's reason to exist, and also its scope.

## Findings

Labels: **D** documented, **O** observed in source, **I** inferred, **U** unresolved.

### Venom

Source: `github.com/ovh/venom` at commit `0eb5198`. Latest stable release v1.3.0 (2026-01-06), pre-release v1.4.0-beta.4 (2026-10-01). Commits most months through 2026-10-30. Apache-2.0, Go.

| Property | Finding | Label |
|---|---|---|
| M1 | Test cases have an `id` field (`types.go:156`) that reaches JSON and XML output. It is undocumented and not checked for presence or uniqueness. Names are slugified in reports (`process_testsuite.go:221`). | O |
| M2 | `skip:` assertion blocks skip cases or steps (README "If one condition from the skip block is not true, it's skipped"). The run passes when one case passes and the rest are skipped (`process.go:210-216`). A suite with no test cases passes. An HTTP step with no assertions defaults to `statuscode ShouldEqual 200` (`executors/http/http.go:91-92`); a step with no type and no assertions passes vacuously. | D, O |
| M3 | Every non-pass outcome exits 2: invalid YAML, missing variables, assertion failures, all-skipped runs, flag errors (`cmd/venom/run/cmd.go:390-410`). XML writes failures and errors as the same `<error>` element. | O |
| M4 | Suite `vars:` can't override CLI variables (`process_files.go:181`), but a step's `vars:` with `default:` can shadow one for later steps (`process_testcase.go:213-214`), and a specification can hard-code any URL. `.venomrc` and `VENOM_VAR_*` are read from the working directory, `$HOME` and the environment. | O |
| M5 | The `exec` executor runs `/bin/sh` commands and is the default for steps with `script` or `command` (`venom.go:133-135`). YAML user executors load from `<suite dir>/lib`, and Go plugins from `lib/<name>.so` (`venom.go:340-350`). The CLI has no allowlist. | D, O |
| M5 (wrapped) | As a Go library, a wrapper can register only chosen executors (`RegisterExecutorBuiltin`, as `cmd/venom/run/cmd.go:328-330` does). User executors from `lib/` and the plugin fallback still load (`venom.go:170-186`), so the wrapper would also have to control the suite directory. | I |
| M6 | The SQL executor runs query strings with `db.Queryx(s)` and no bind arguments (`executors/sql/sql.go:79,97`). Values reach SQL only through template interpolation. No read-only option. `dbfixtures` writes to the database with testfixtures' safety check disabled (`dbfixtures.go:131,151`). **Fails M6; Proctor would need its own executor.** | O |
| M7 | `retry`, `delay` (whole seconds) and `timeout`, bounded by the retry count. The timeout passes the parent context to the executor, so a timed-out executor isn't cancelled (`process_teststep.go:147-149`). No `consistently`. | O |
| M8 | Possible through custom built-in executors in a Go wrapper. | I |
| M9 | Release assets are bare binaries with no checksums or signatures. `venom update` replaces the binary in place. Pinning would mean building from a pinned source commit. | O, D |
| M10 | JSON output has per-step status, assertion results, interpolated inputs, executor results (HTTP request, status, headers, body), retries and timings (`types.go:187-208`). One file per suite, named by basename, so suites with the same basename in different directories overwrite each other (`venom_output.go:113-114`). XML and TAP misreport skips; TAP misreports failures. | O |
| Other | No list, dry-run or lint command. `Parse()` is separate from `Process()` in the Go API, so a wrapper could enumerate cases without running them, but `skip:` can't be evaluated without running. Suites and cases run sequentially in command-line order. `range:` over a map iterates in random order. The Go API has no stated stability guarantee. | O, I, U |

### Hurl

Source: `github.com/Orange-OpenSource/hurl` at commit `cb4fa2d`. Latest release 8.1.0 (2026-10-28); master is 9.0.0. Daily commits. Apache-2.0, Rust. Releases ship `.sha256` checksums; no signatures or attestations found.

| Property | Finding | Label |
|---|---|---|
| M1 | Entries are identified only by file, index and line in the JSON report (`src/json/result.rs:67-83`). Proctor would have to map IDs to positions. | O |
| M2 | `[Options] skip: true`, which can be driven by a variable (`docs/grammar.md:120`), and `repeat: 0`. A skipped entry produces no result at all (`runner/hurl_file.rs:233-246`), so it is absent from every report. `--from-entry`, `--to-entry` and `--no-assert` also bypass checks. An all-skipped file exits 0. | D, O |
| M3 | 0 success, 1 command-line error, 2 parse error, 3 runtime error, 4 assertion failure (`main.rs:39-44`, `docs/manual/hurl.md`). JUnit separates `failure` from `error`. **Meets M3.** | D |
| M3 (precedence) | When both runtime errors and assertion failures occur, 3 wins (`exit_code()` in `packages/hurl/src/main.rs`). The manual lists the codes but not the precedence. | O |
| M4 | In-file `[Options]` override CLI variables ("overrides all others", `docs/manual/hurl.md:159-164`). Files can also set `resolve`, `connect-to` or `proxy`, or use absolute URLs. 8.1.0 reads `~/.config/hurl/config` implicitly. A wrapper must reject these options and isolate `HOME` and `XDG_CONFIG_HOME`. | D, O |
| M5 | No code execution; template functions are `newUuid` and `newDate` only. File reads are confined to `--file-root`. **Meets M5.** | D, O |
| M6, M8 | HTTP only. No SQL, Temporal, logs or plugins. | D |
| M7 | `retry: N` retries on any failure, including assertions, so it works as polling. `-1` means unlimited and must be forbidden. No `consistently`. | D |
| M9 | Checksummed releases. **Meets M9.** | O |
| M10 | JSON report per entry: request, response, timings, captures, each assertion with line and message; secrets redacted (`json/result.rs:76-148`). **Meets M10.** | O |
| Other | `hurlfmt --out json` parses a file into a JSON syntax tree without running it, which a wrapper can use to inspect options before execution. | D |

### Step CI

Source: `github.com/stepci/stepci` at commit `e9af898` and `stepci/runner` at `01b236f`. CLI 2.8.2 (2024-06-10); last CLI commit 2024-08-03; last runner commit 2025-04-05, a dependency bump. MPL-2.0, TypeScript, npm.

| Property | Finding | Label |
|---|---|---|
| M2 | A step `if:` that evaluates false is marked skipped and passed (`runner index.ts:375-377`); an all-skipped workflow exits 0. | O |
| M3 | Exit 5 for any non-pass, assertion or error alike (`src/index.ts:31`). | O |
| M4 | Test-level `env` overrides CLI values (`runner index.ts:375,382`). | O |
| M5 | The `plugin` step runs `require(params.id)` (`runner steps/plugin.ts:20`), so any test file can load any resolvable Node module. **Fails M5.** | O |
| M10 | The CLI has no JSON, JUnit or other report output; details are only available through the library API. | O |
| Other | Analytics are sent to PostHog on each command unless `STEPCI_DISABLE_ANALYTICS` is set (`src/lib/analytics.ts`). No dry-run or list mode. | O |

**Rejected:** fails M5, the project is dormant, and analytics by default are unsuitable for a verification tool.

### Minimal Proctor-native runner

Not built; assessed from the requirements.

- Meets every mandatory property by design, because the schema, executors, results and exit codes are Proctor's own.
- Cost: an HTTP executor on Go's `net/http`, JSONPath evaluation (a third-party library), the assertion engine, captures and interpolation, polling, and the YAML schema with validation. Most of these are things the integrity layer has to control or constrain anyway, whichever runner is chosen.
- Risk: Proctor rebuilds what already exists, and its HTTP and assertion code is less exercised than Venom's or Hurl's.

## Comparison

| | M1 IDs | M2 no silent skip | M3 exit codes | M4 no redirect | M5 no code exec | M6 SQL | M7 timing | M8 extensible | M9 pinning | M10 evidence |
|---|---|---|---|---|---|---|---|---|---|---|
| Venom, CLI | Partial | No | No | No | No | No | Partial | No | No | Yes |
| Venom, Go library wrapper | Wrapper enforces | Wrapper enforces | Wrapper classifies | Wrapper validates | Partial: `lib/` still loads | Own executor | Own constructs | Yes | Build from source | Yes |
| Hurl, wrapped | Wrapper maps | Wrapper rejects | Yes | Wrapper rejects | Yes | Not covered | Partial | Not covered | Yes | Yes |
| Step CI | Partial | No | No | No | No | Not covered | Partial | Plugin, unsafe | npm only | Library only |
| Native runner | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes |

"Wrapper enforces" means Proctor writes the logic; the runner doesn't provide it.

## Effect of revision 2 of the PRD

Revision 2 moves sample scheduling, table expansion, setup handling, interpolation, trace context and per-sample transactions into Proctor's core (`prd-revised.md` §19, ARC-02), and forbids using a backend's retry, delay or repeat options (TIME-06), because Venom's whole-second delays and uncancelled timeouts, and Hurl's retries, break the time bounds. A runner backend can therefore contribute at most single-operation execution and assertion helpers. This strengthened the inferred native-runner outcome before the product owner decided it.

## Format consequence

If Proctor uses Venom's or Hurl's format directly, the specifications people approve are Venom YAML or Hurl files plus restrictions. That conflicts with the schema in `prd-revised.md` §11 (scenario IDs, tables, identities, evidence levels, `consistently`). The alternative, translating Proctor's schema into Venom suites or Hurl files, adds a compiler and leaves Venom or Hurl as little more than an HTTP executor. This was OD-09 in `open-decisions.md`, resolved by the decision: people approve Proctor's own schema.

## Ideas Proctor takes

These are ideas and documented behaviors, described here in Proctor's own words. Most are already requirements in `prd-revised.md`; the requirement is what Proctor implements, not the other tool's code.

| Idea | Seen in | Where Proctor has it |
|---|---|---|
| Separate exit codes for a failed check, an error and invalid input | Hurl's exit codes; JUnit's `failure` versus `error` | §13.3 exit codes, REP-07 |
| Parse and validate everything before running anything | Hurl's `hurlfmt`, Venom's separate parse step | `proctor validate`, EXE-01 |
| Per-step evidence: the request as sent, the response, each assertion and its message | Venom's and Hurl's JSON reports | §13 |
| Redact secrets in reports | Hurl's JSON report | REP-04 to REP-06 |
| Confine file reads to one root | Hurl's `--file-root` | INT-01 |
| Publish checksums with each release | Hurl's releases | SEC-11 |

And what Proctor avoids, because a tool above showed how it goes wrong:

| Avoided | Seen in | Proctor's rule |
|---|---|---|
| Test files that skip themselves, conditionally or not | Venom `skip:`, Hurl `skip`/`repeat: 0`, Step CI `if:` | INT-03, INT-04 |
| A step with no assertions passing, or a hidden default assertion | Venom's HTTP default and vacuous steps | EXE-03 |
| Test files overriding the target or connections set by the run | Venom step `vars`, Hurl `[Options]`, Step CI `env` | HTTP-05, SEC-07 |
| Shell commands or code loaded from the test directory | Venom `exec` and `lib/`, Step CI `plugin` | SEC-06 |
| Configuration read implicitly from the home directory or environment | `.venomrc`, `VENOM_VAR_*`, Hurl's user config | MODE-01 (configuration and binding come only from paths the run names) |
| SQL built by string interpolation | Venom's SQL executor | DB-01 |
| Timeouts that don't cancel the work, delays in whole seconds, unbounded retries | Venom, Hurl `retry: -1` | TIME-06 and §11.9 |
| Reports that overwrite each other when two files share a name | Venom's output files | One JSON report per run, results keyed by scenario ID (REP-08) |
| Analytics sent by default | Step CI | SEC-07 (Proctor contacts only the hosts it lists) |

## No copied code

Venom and Hurl are Apache-2.0 and Step CI is MPL-2.0. Proctor copies none of their code, under any license.

- No source from another test runner is copied, translated into Go or closely paraphrased into Proctor. That covers tests, fixtures and documentation text too.
- Runner code is written from Proctor's requirements and from primary references (Go's standard library documentation, RFC 9110 for HTTP, RFC 9535 for JSONPath), not with another runner's source open. The clones read for this evaluation have been deleted; the Findings cite public commits instead.
- A library used as a declared dependency in `go.mod`, such as `go.yaml.in/yaml/v3` (MIT and Apache-2.0), is not copied code. Its license is checked before it is added, and its notices travel with Proctor's releases.
- File-format ideas, such as a YAML suite with named steps, are ideas rather than code. Proctor's schema is its own design (`prd-revised.md` §11).

This is an engineering rule, not legal advice. If the project needs a firm answer on a licensing question, such as what a release must ship to meet Apache-2.0's NOTICE terms, ask someone qualified.

## Risks and uncertainty

- Proctor's HTTP execution and assertion code starts with no field use. Mitigation: the Phase 2 example applications in Go, Node.js and Python, and the adversarial tests in `acceptance-tests.md`.
- The Findings come from reading source at one commit, not from running it. They no longer drive a decision, so they weren't re-checked.
- What would reopen the decision: a requirement Proctor can't meet with reasonable effort on its own, such as a protocol whose client is large and specialized. Even then, the choice would be a library dependency, not another test runner.

## Sources

- Venom: https://github.com/ovh/venom (commit `0eb5198`), releases page, README and `executors/*/README.md`.
- Hurl: https://hurl.dev/docs/manual.html and https://github.com/Orange-OpenSource/hurl (commit `cb4fa2d`).
- Step CI: https://docs.stepci.com and https://github.com/stepci/stepci (commit `e9af898`), https://github.com/stepci/runner (commit `01b236f`).
