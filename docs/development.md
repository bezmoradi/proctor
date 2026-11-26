# Development

To build from source, with Go 1.27.4 or later: in Proctor's checkout, `scripts/build.sh <dir>` builds `proctor`, its drivers and the simulators into `<dir>` (`bin/` by default), and `--install` also copies them into `$(go env GOPATH)/bin`. `scripts/release.sh <version> <dir>` builds a release folder from a clean checkout: the four archives, the notices, `install.sh` and `SHA256SUMS`.

```sh
go build ./... && go vet ./... && test -z "$(gofmt -l .)"
sh scripts/check-arch.sh
scripts/build.sh /tmp/proctor-bin && /tmp/proctor-bin/proctor version --features
```

CI (`.github/workflows/ci.yml`) runs these and the rest of the checks in [CLAUDE.md](../CLAUDE.md#verify) on every pull request. A pull request needs a linked issue (`Closes #<n>`) to merge. This repository doesn't include Proctor's test suite.

## Self-check

The root `proctor/` folder is Proctor's check of itself with the cli driver: its scenarios run the `proctor` first on `PATH` (`version`, `version --features`, `validate` on the sample, `run` without `--binding`, and `init`'s refusals). CI's "Sample end to end" job runs it in local mode in the step "Proctor checks itself". To run it locally, from the repository root:

```sh
scripts/build.sh /tmp/proctor-self
export PATH="/tmp/proctor-self:$PATH" PROCTOR_NO_UPDATE_CHECK=1
(cd examples/sample/proctor/drivers/kafka && GOWORK=off GOTOOLCHAIN=go1.27.4 CGO_ENABLED=0 GOFLAGS=-mod=readonly \
  go build -trimpath -buildvcs=false -o bin/proctor-driver-kafka ./cmd/proctor-driver-kafka)
proctor validate
proctor run --binding proctor/environments/local.yaml
```

The third line builds the sample's `kafka` driver, as step 1 of [`examples/sample/README.md`](../examples/sample/README.md) does, since SELF-003 validates the sample's configuration; the sample's stack needn't run. The binding passes `PROCTOR_NO_UPDATE_CHECK` to the checked commands, so it must be set. `proctor.yaml` protects `examples/sample/proctor/` because SELF-003 names a file there, and an observe command's files must be protected (CLI-04).

On a pull request and in a merge queue, `.github/workflows/proctor.yml` also runs it in [authoritative mode](guides/ci-github.md), modeled on the reference workflow, [`examples/github/proctor.yml`](../examples/github/proctor.yml):

- **build** builds the pull request's `proctor`, its drivers and the sample's `kafka` driver into an image, with only `contents: read`.
- **verify** builds the verifier, `proctor` and its drivers, from the base commit's source with its `scripts/build.sh`, outside the checkout; no release is pinned. On `PATH` it puts a `proctor` script, outside the checkout, that runs the pull request's image with no network, no environment but `PROCTOR_NO_UPDATE_CHECK`, and the checkout mounted read-only, so this job's host never runs the pull request's code. The binding gives that image's ID as both digests.
- **verdict** is the check to require: it fails unless the report is a passing authoritative report for this commit, run and attempt. The owner adds it to the ruleset on GitHub; `.github/rulesets/main.json` lists it.

The scenarios are the tested commit's. A change to a protected file, in `proctor/`, `examples/sample/proctor/` or `.github/`, is listed as "approval required", with any weakening, in the report and the verify job's summary. There is no code-owner rule: `verdict` fails on such a change until the owner adds the `review-override` label after the latest push, as the Workflow guard does; adding or removing the label runs the workflow again. A merge group run skips this, since each pull request in it passed `verdict` first. The workflow runs as the pull request has it, so the Workflow guard check needs the owner's `review-override` label for a change to it.

## Releasing

Only the owner (bezmoradi) can release: `.github/workflows/release.yml` refuses a tag pushed by anyone else, one not on main, and one that isn't `vMAJOR.MINOR.PATCH` or `vMAJOR.MINOR.PATCH-PRERELEASE`. A prerelease tag makes a prerelease, which `latest` and the update check skip.

1. Set the new version: nothing to bump, the tag is the version.
2. Tag main and push the tag:

   ```sh
   git fetch origin && git tag -a vX.Y.Z -m "proctor X.Y.Z" origin/main && git push origin vX.Y.Z
   ```

3. Watch the release workflow: `gh run watch "$(gh run list --workflow release.yml --limit 1 --json databaseId --jq '.[0].databaseId')"`. It builds with `scripts/release.sh`, checks the archives and `install.sh`, signs their provenance and drafts the release.
4. Check the draft: `gh release view vX.Y.Z` lists the four archives, the notices file, `install.sh` and `SHA256SUMS`.
5. Publish it: `gh release edit vX.Y.Z --draft=false`, or on the releases page. From then on the one-command install installs it and older versions announce it.
