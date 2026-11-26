# Security model

## Privacy

Proctor runs on your own machine or CI runner. Its author runs no server for it and receives none of your data, and it sends no telemetry.

- **What it reads:** your repository's working tree or Git objects, the binding, the environment variables the binding names, the application's log file, or the records at its `app_logs.otel` receiver, when `app_logs` is set, and the answers of the targets your specifications check.
- **Where it writes:** reports to stdout and the `--report` file (mode 0600); received transcripts and logs to a new 0700 folder under `$TMPDIR/proctor-received/<project>/`, or `--received`; approved files under `proctor/approved/` only when you answer `y` to `proctor approve`; mutant copies under `$TMPDIR`; and the files you name for `mutate html` and `mutants generate`. A run scrubs the credentials, secrets and redacted values it knows from what it writes.
- **What leaves the machine:** `validate`, `integrity`, `diff` and a local `run` ask `https://api.github.com/repos/bezmoradi/proctor/releases/latest` for the latest release, sending only `proctor/<version>` as the User-Agent; set `PROCTOR_NO_UPDATE_CHECK=1` to turn it off (it is off when `CI` or `GITHUB_ACTIONS` is set, and in authoritative mode). In authoritative mode it verifies the job's OIDC token against GitHub's published keys and asks GitHub's API for CODEOWNERS errors with `GITHUB_TOKEN`. Drivers, and the app-log check with `app_logs.otel`, reach only the targets in the binding. The simulators keep what they receive in memory and send nothing on. `mutate` runs your `up` and `down` scripts, and `mutants generate --build-check` runs your `go` toolchain, which may download modules as your Go settings allow.
- **Stopping it:** delete the reports, the `proctor-received` folders and any `mutate` leftovers its report lists; Proctor keeps no other state.
