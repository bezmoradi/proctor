# Concepts

Proctor's words, and the model behind its checks. Each term links to the page that has the details.

## The agreement and who may change it

- **Specification:** a YAML file under `proctor/` holding a suite of scenarios and tables. The manifest lists every specification, and an unlisted YAML file there is refused (INT-05), so a scenario can't be dropped by leaving it out.
- **Protected inputs:** the files whose change changes what Proctor checks: everything under the folder holding `proctor.yaml` (`proctor/` by default), `.github/`, `.gitattributes`, `.gitmodules`, `.lfsconfig`, CODEOWNERS, and any `protected_paths`. CODEOWNERS makes a person approve each change to them; Proctor finds and lists the changes, and approves nothing.
- **Weakening:** a protected change after which the checks catch less, such as a removed assertion, an added waiver, a longer wait or a step moved into setup. `proctor diff` lists weakenings first, and lists every added step with what it asserts. See [`proctor diff`](reference/diff.md).
- **Change kind:** what a pull request claims it is (`refactor`, `docs`, `fix` or `feature`). A claim can add a failure, never remove one. See [Change kind](guides/ci-github.md#change-kind).

## Scenarios and steps

- **Scenario:** one behavior, as a list of steps that run in order. A **table** is many scenarios from one step and a list of rows.
- **Step:** one call to a driver. It names the operation in `do`, the identity it acts as, the arguments in `with`, what the answer must hold in `expect`, and the values it saves for later steps with `save`.
- **Phases:** a step is setup, verification or cleanup. **Setup** prepares through the application and must also be exercised by a verification step (FIX-08). **Verification** steps are the checks. **Cleanup** acts after the steps whatever happened, to free what the scenario created (see [Cleanup](guides/specifications.md#cleanup)).
- **Fact:** what a driver returns for a step, such as an http response's `status`, `headers` and `body`. Expectations are claims on paths in the fact (`$.body.id`), checked by **matchers** such as `equal`, `absent` and `none` (see [Matchers](reference/specifications.md#matchers)).
- **`${run.id}`:** a value unique to each scenario in each run. A value a scenario writes and later matches must hold it (FIX-05), so one run can't pass on what another run left.
- **Sampling:** a step that observes can wait for its expectations (`eventually`), or must keep them for a while (`consistently`) or never meet them (`never`). Samples can't see a change between them, and a passing `consistently` or `never` step says so (TIME-05).

## Acts, observations and state checks

- **Act and observe:** each driver operation either acts (changes something, such as a POST) or observes (reads, such as a GET or a database query). An act can't be sampled (TIME-11).
- **State check:** a later read that shows what an act changed. Every act needs one (EXE-07), because a status code alone shows only that the application answered: a step that changes state must be followed by a read of the same target, or of a target a declared relation says it shows. `no_state_check: <reason>` waives it in writing.
- **Destructive act and guard:** a DELETE, PUT, PATCH or declared destructive POST on a **fixed target**, one whose path or body holds neither `${run.id}` nor a saved value, could destroy data that isn't the run's. It needs an earlier read of that target, its **guard**, or a written waiver (EXE-09).
- **Waiver:** a written reason that turns one rule off for one step, such as `waive: { FIX-05: <reason> }`. Only FIX-05, HTTP-09 and EXE-09 can be waived, and `proctor diff` lists each added waiver as a weakening.

## Drivers, bindings and identities

- **Driver:** a program that runs a kind of step, such as `http`, `postgres` or `browser`. **Official drivers** ship with Proctor, and `proctor` refuses one whose checksum it doesn't hold (INT-17). A **repository driver** lives in your repository, pinned by checksum in `proctor.yaml` (see [Repository drivers](guides/repository-drivers.md)).
- **Policy:** a driver's settings in `proctor.yaml`, such as the http driver's declared reads or the docker driver's scope. It is a protected input, so widening it shows in `proctor diff`.
- **Binding:** where the application runs and which credentials to use, kept apart from the specifications: base URLs, socket paths, and the names of environment variables that hold tokens. One agreement can run against several environments, each with its own binding.
- **Identity:** a user or client a step acts as. `proctor.yaml` declares identities; the binding says which environment variable holds each one's token. A **variant** is another credential for the same identity, such as an expired token, sent with `variant: <name>`; one listed under `variants` must be refused with 401 or 403 (AUTH-06), and one under `accepted_variants` needn't be.

## Modes

- **Local mode:** for development. `proctor run --binding FILE` runs the working tree's specifications against an application you started, and its result is never authoritative.
- **Authoritative mode:** for the trusted CI job. `proctor run --authoritative` reads every protected input from the tested commit, compares it with the base, verifies the job's identity, and refuses to run while any of its checks fails. See [Authoritative mode](guides/ci-github.md) and the [Security model](security-model.md).

## Evidence beyond assertions

- **Approved transcript and approved log:** a scenario's whole story, or its log, that a person read and approved; later runs must match it byte for byte. They add checks and replace none (see [Approved transcripts and logs](guides/approvals.md)).
- **Environment invariant:** a read-only check that must hold before the first scenario and after each one, so a scenario that leaves state behind is the one blamed (see [Environment invariants](guides/specifications.md#environment-invariants)).
- **Mutant:** a declared defect in the application. A mutant is killed when a scenario fails against it; one that survives shows a behavior the specifications don't check (see [Mutation testing](guides/mutation-testing.md)).
