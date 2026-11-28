# Mutation testing

`proctor mutate` checks that your specifications catch defects you declare. Each mutant is an exact text edit of the application; your own scripts build and start the changed copy, and Proctor runs the scenarios against it. A mutant is killed when at least one scenario FAILs.

```yaml
# proctor/proctor.yaml
mutation:
  mutants: mutants.yaml            # under proctor/, so it is protected like the specifications
  up: env/mutant-up.sh             # builds and starts the copy; receives the copy's folder as $1
  down: env/mutant-down.sh         # stops it; always runs once up has started
  binding: /tmp/my-app/binding.yaml  # absolute, outside the repository; up writes it
```

```yaml
# proctor/mutants.yaml
- id: M-01
  title: Create answers 200 instead of 201
  edits:
    - file: internal/handlers/users.go
      find: "w.WriteHeader(http.StatusCreated)"
      replace: "w.WriteHeader(http.StatusOK)"
- id: M-02
  title: Only the second greeting changes
  killed_by: [GRT-002]
  edits:
    - file: labels.go
      line: 8
      find: "\treturn \"hello\"\n"
      replace: "\treturn \"bonjour\"\n"
```

Writing mutants:

- `find` must be unique in the file, or appear exactly `count` times (default 1), and every occurrence is then replaced. Proctor never picks one of several matches: any other number of matches is an ERROR, "the edit didn't apply: found N times, expected M", never a kill. Make `find` long enough to be unique.
- To change one of several identical blocks, give `line`, the 1-based line where that block's `find` starts (it may span lines). If `find` doesn't start on that line, the mutant is an ERROR, "find not at line N", never a silent change somewhere else. `line` can't be combined with a `count` other than 1.
- Several edits of one file apply in the order written, and each is located in the original file, before any edit; edits that overlap are an ERROR.
- `proctor validate` applies each mutant's edits to the files as they are, without running anything, and refuses (exit 3, MUT-01) a `find` found another number of times than `count`, one that doesn't start at its `line`, and edits that overlap, so a code change that breaks a mutant shows before `proctor mutate` runs.
- An edit's `file` is repository-relative, a regular file tracked by Git or untracked and not ignored, and never a protected input: a mutant can't edit the specifications or Proctor's configuration.
- `killed_by` names the scenarios meant to catch the mutant. If only other scenarios fail, the verdict is `killed_elsewhere`, which counts as survived.
- Proctor copies the files `git ls-files -co --exclude-standard` lists into a fresh 0700 folder under `$TMPDIR` for the baseline and each mutant, without `.git` and without ignored files; a listed symbolic link refuses the run. `up` runs from your repository with the copy's folder as its argument, and `PROCTOR_MUTANT` (`baseline` or the mutant's id) and `PROCTOR_MUTANT_DIR` in its environment.
- An application that issues a credential each time it starts, and refuses the one before, needs the new one for each mutant. `up` and `down` also get `PROCTOR_ENV_FILE`, an empty file (mode 0600, in its own 0700 folder under `$TMPDIR`) where `up` can write `NAME=value` lines, one per line, such as `MY_APP_TOKEN=…` for an identity whose `token_env` is `MY_APP_TOKEN`. Once `up` succeeds, Proctor reads it and looks up every variable the binding names (`token_env`, a variant's `token_env` or `header_env`, a secret's `env`, a driver's `*_env`) there first, then in its own environment, and scrubs those values like any credential. A line that isn't `NAME=value`, a name set twice, a value with a carriage return or NUL, a file over 1 MiB or one replaced by a link makes the mutant an ERROR. The file is removed after `down`. Since the binding is read again for each mutant, `up` can also rewrite it for values that aren't credentials.
- The baseline (the unchanged copy) must pass, or no mutant runs (exit 2). The first `up` or `down` that fails or times out (`--up-timeout`, default 45m; `--down-timeout`, default 10m) stops the run, and the rest are `not_run`; a script past its timeout is killed with its whole process group. `--keep-going` goes on after an `up` that exits non-zero, never after a failed `down`, which may have left containers or images behind.
- Write some mutants without reading the specifications, or have someone else write them, so the score measures the specifications rather than their fit to known defects. Each full image build takes time, so use `--only` while writing them.

`proctor diff` lists a removed mutant (`mutant_removed`), a changed edit or `killed_by` (`mutant_changed`) and a changed or removed `mutation` key (`mutation_changed`, `mutation_removed`) as weakenings; an added mutant isn't one.

## The HTML report

`proctor mutate html` draws a report `proctor mutate --report` wrote as pages to open in a browser, so a long run can be looked at again without running it again:

```sh
proctor mutate --report /tmp/mutate.json
proctor mutate html --from /tmp/mutate.json --out /tmp/mutate-html --coverage /tmp/cover.txt
open /tmp/mutate-html/index.html
```

- `index.html` has the counts per verdict, the mutation score, the run time, Proctor's version, the survivors (`survived` and `killed_elsewhere`) with links to them, and a table of the mutated files, those with survivors first. The score is the mutants a scenario caught (`killed` and `killed_elsewhere`) out of those and the `survived` ones; `error` and `not_run` mutants aren't in it.
- Each mutated file gets a page with its whole source and line numbers. A mutated line carries each mutant's verdict as a word, not only a colour, and under it the mutant's id, title, operator (for a generated mutant), verdict and message, the scenarios that failed ("no scenario failed" otherwise) and its `killed_by`, and a diff of the lines its edit changes. A mutant whose edit didn't apply in the run is listed at the top of its file's page. With `--coverage` (a text profile, mapped to files as for `mutants generate`), lines in blocks that never ran are greyed and marked "never ran".
- The pages are read from the repository the report names, as it is now. A mutant whose edit applied in the run but whose `find` is no longer in its file, or no longer at its `line`, refuses the pages (exit 2), naming the mutant, rather than drawing the wrong lines; a file that changed elsewhere gets a warning and a note on its page.
- Each page is plain HTML that stands alone, with the stylesheet inline in one `<style>` element: no script, no other file, nothing loaded from the network, so it renders the same opened from disk. Its Content-Security-Policy is `default-src 'none'; style-src 'sha256-…'; img-src 'none'`, the hash computed from the same embedded stylesheet bytes the page holds, so only that `<style>` element applies and no `style=` attribute is used. All text, source code, titles, scenario ids and file names included, is escaped by `html/template`, and file pages are named `file-0001.html` and so on, never after their paths.
- `--out` must be outside the repository (exit 3: the pages hold its source code), not a symbolic link, and new or empty unless `--force`, which replaces the pages' files in it and leaves its other files. Each file is written with mode 0600 and the folder, when Proctor creates it, with 0700.
- The report must come from a Proctor with this command (feature `mutate.html`): older reports have no `provenance.repository` and no `edits` per mutant, and are refused (exit 3).

## Generating mutants

`proctor mutants generate` writes a first draft of mutants for a Go project, so nobody types hundreds of edits by hand. It reads Go only, with the standard library's parser, and applies eight operators:

| Operator | Change |
|---|---|
| `negate` | an `if` or `for` condition `c` becomes `!(c)` |
| `boundary` | `<` and `<=`, `>` and `>=` swap |
| `equality` | `==` and `!=` swap |
| `logic` | `&&` and `\|\|` swap, with parentheses added where the swap would change how the expression groups |
| `swallow` | in a function whose result is `error`, a returned error becomes `nil` |
| `remove-call` | a call statement or a `defer` is removed (`_ = 0` after a label) |
| `bool` | `true` and `false` swap |
| `constant` | an integer literal n becomes n+1, never in an array length or a shift, never to a type's largest value, and never to a value a neighbouring `case`, map key or constant already has |

```sh
proctor mutants generate --out /tmp/gen.yaml --index /tmp/gen.md \
  --coverage /tmp/cover.txt --build-check --limit 300 --seed 1
```

- Every mutant has one edit with `line` and `find`, the exact source text of the changed expression or statement, so `proctor mutate` changes exactly that spot and a stale line is an ERROR. When that text also starts elsewhere on the same line, `find` runs on to the end of the line, or starts at the line's first token, until one match starts there; a mutant whose `find` can't be made unique, is over 512 bytes, or leaves a file that doesn't parse is left out.
- `--coverage` takes a text profile (`go tool covdata textfmt -i DIR -o FILE`, or `-coverprofile`). With it, a mutant is written only where a block that ran (count above 0) holds the changed code; package-level values count as run when any block of their package ran. Profile paths are import paths, mapped to files through each `go.mod`'s module path. Without it every line is used, with a warning. To get a profile from your scenarios, build the application with `go build -cover`, run it with `GOCOVERDIR` set, run the scenarios, and stop it with a normal exit (counters are written at `os.Exit` or when `main` returns, not on a kill).
- `--build-check` keeps a mutant only if `go build -trimpath -o /dev/null ./...` and `go vet` of its package, run in the module that holds the file, still pass on a copy of the listed files with the mutant applied, with `GOOS`/`GOARCH` from `--goos`/`--goarch` (default `linux`/`arm64`) and `CGO_ENABLED=0`. `--jobs` (default 4) copies run at once, each reused and restored after each mutant; mutants that fail are replaced by the next ones until `--limit` is reached. The unchanged copy must build, or the command exits 2. Without the flag nothing is built, with a warning; even then, a removal that would leave a local variable or an import unused, and the removal of a `panic` in a function with results (which may then lack its final return), isn't generated.
- Choosing is spread first: the first pass takes one mutant per function and operator (closures count as their own function, `main.func1`), then later passes fill up to `--limit` (default 300), round-robin over the functions. Order within a pass comes from `--seed` (default 1): the same files and flags give a byte-identical file.
- Scope: the files `git ls-files -co --exclude-standard` lists, built for `--goos`/`--goarch`, minus `*_test.go`, generated files (`// Code generated … DO NOT EDIT.`), `vendor/`, `testdata/`, folders starting with `.` or `_`, and the protected inputs. `--include` and `--exclude` take path globs (`**` matches any folders; `dev/` means `dev/**`).
- Output: `--out` (required) gets a YAML list that passes `proctor validate` when added to the mutants file, with titles such as `boundary in parseLimit (limits.go:16)`. `--index` adds a `.csv`, `.md` or `.markdown` table of id, file:line, operator, function and coverage (`yes`, or `unknown` without a profile), and `--out-json` the same mutants as JSON. Each is written with mode 0600; the configured mutants file is always refused, and a path inside the protected inputs needs `--force`. A mutant whose file, line, find and replace already appear in the configured mutants file isn't generated again, and no two generated mutants share a file, find and replace.
