# Specification reference

## Validation

There is no warning level. Every validation finding fails the command. A step can waive a rule only with a written reason (`no_state_check: <reason>`, or `waive: { FIX-05: <reason> }`), and `proctor diff` lists each added waiver as a weakening.

Validation requires:

- a later state check after every request that changes state (EXE-07). An official driver can say that an act changes no state, as `browser.open` and `browser.fill` do, since they change nothing on the server; such a step needs no state check and refuses `no_state_check` (EXE-07), and a repository driver that says so is refused (exit 3, INT-21);
- `${run.id}` in every value a scenario writes and later matches (FIX-05);
- every setup operation to also appear as a verification step (FIX-08);
- a destructive request (PUT, PATCH, DELETE, or a POST declared destructive) on a target that refers to neither `${run.id}` nor a value saved earlier in the scenario to follow an earlier read of the same target, or an earlier destructive request on it that EXE-09 accepted without a waiver, or to waive EXE-09 with a reason. A waived request guards nothing, so each later request on the same target needs its own waiver, which `proctor diff` lists as `rule_waived`. A path built from an identity value, such as `/things/${alice.id}`, is a fixed target, and a read counts only when it reads that target as written, a path above or below it, or a path its policy `shows`, expecting an outcome that reads state (EXE-09);
- every path in an expectation, a claim's keys and every `save` path to be one the operation's fact can hold, as its driver's `fact_schema` describes it, and each matcher to name the right kind of value: a list for `length`, `any`, `none` and `count`, an object for `keys`, text for `contains`, text or a number for `matches`, and text or a list of texts for `excludes` (EXE-11). Inside a free-form part, such as an http `body`, any path is accepted, so a misspelt `absent` there still can't be caught.

At run time, a `none` claim or a `count` with `is: 0` fails when the list has elements but none of them has one of the claim's keys, since such a claim could never fail (EXE-11). That holds when no element has the key's parent, too: `none: { "$.messages": { json.type: error } }` fails when every frame is plain text, because "no JSON frame of type error" would then also pass when the server starts sending its errors as plain text. The message names the missing part; add a claim that some element has it (`any`, or `count` above 0), or assert what the elements are instead.

A `waive` on a table's step written as its reason alone must apply in every row. A waiver for some rows names them, `waive: { EXE-09: { rows: [t01_remove_item], reason: <text> } }`: validation refuses a named row the rule doesn't apply to, a row it applies to that the list leaves out, and an unknown case id, and `proctor diff` reports each row the list newly covers as `rule_waived`.

## Matchers

| Matcher | Passes when the value at the path |
|---|---|
| `equal` | equals the expected value, with its JSON type |
| `present`, `absent` | exists, or doesn't |
| `keys` | is an object with exactly these keys |
| `length` | is a list with exactly this many elements |
| `unordered` | is a list of exactly these values, in any order |
| `contains` | is text that contains this text |
| `any`, `none` | is a list where some element, or no element, has every claimed value |
| `count` | is a list where exactly `is` elements have every `where` value: `count: { $.items: { where: { state: done }, is: 2 } }` |

Besides `equal`, `present`, `absent`, `keys`, `length`, `unordered`, `any`, `none`, `contains` and `count`, three matchers check text and change:

```yaml
expect:
  matches: { "$.headers.x-request-id[0]": "[0-9a-f]{16}" }
  excludes: { "$.body_text": [/workspace, "https://", "@"] }
  differs: { "$.headers.x-request-id[0]": "${first_id}" }
```

- `matches` takes an RE2 pattern that must match the whole text, as if written `^(?:…)$`. Validation refuses (exit 3, EXE-12) an invalid pattern, one longer than 1 KiB, one that matches empty text (`.*`, `[0-9]*`), and one holding `${...}`, which it doesn't expand. A JSON number is matched as the fact's JSON writes it, so `[0-9]{13}` checks a time sent as epoch milliseconds (`1795274282478`) and `1.50` or `1e3` is matched as that text. Any other value that isn't text fails.
- `excludes` takes one text or a list; the text at the path must contain none of them. On a list of texts, such as `$.attribute_keys`, no element may contain any of them, and an empty list fails, since nothing could hold them. A missing path or a value that isn't text or a list of texts fails; an empty text, an empty list of texts to exclude and a repeated text are refused (EXE-12).
- `differs` takes any value, usually a saved one; the value at the path must be present and not equal to it, compared as `equal` compares (`7` differs from `"7"`).

`proctor diff` lists removing any of them as `assertion_removed` and loosening one as `assertion_weakened`: any change to a `matches` pattern (Proctor can't tell whether the new one accepts more), an `excludes` text that no remaining text rules out (`/workspace` to `/workspace/x`, or a text dropped from the list), and any change to a `differs` value.

## Paths

A path is `$` followed by `.name`, `[index]` and quoted names. A quoted name, `['…']` or `["…"]`, names a key a `.name` can't, such as one with a dot, a slash or a leading digit; inside it `\\`, `\'` and `\"` stand for a backslash and the quotes. Claim keys are relative to one element, so `['dev.x']` there means `$['dev.x']`:

```yaml
expect:
  equal:
    "$.body.labels['com.example-shop.environment']": shop-proctor
    '$["body"]["ports"]["8080/tcp"]': open
    "$.headers['x.trace']": [t1]
  none: { "$.body.items": { "['dev.x']": gone } }
save: { environment: "$.body.labels['com.example-shop.environment']" }
```

Without the brackets, `$.body.labels.com.example-shop.environment` reads three nested names. Proctor reads every path in one spelling (`$['body']['id']` is `$.body.id`), in the drivers, the report and `proctor diff` too, and refuses a matcher that names one path twice in two spellings (EXE-13).

## Cut text

The http driver keeps the first 64 KiB of a body that isn't JSON as `$.body_text` and says whether it cut it in `$.body_truncated` (the ws driver does the same for a refused handshake's body, cut at 1 KiB). `equal`, `contains`, `matches`, `excludes` and `differs` on a cut `$.body_text` fail, naming the cut and its size (`cut_at` in the report), and so does `save` from it; under `never` they fail the step instead of counting as not holding. `present` and `absent` still work (HTTP-13). The same rule holds for every text a driver marks as cut: any `<x>_text` beside `<x>_truncated: true`, such as the cli driver's `stdout_text` and `stderr_text` or a repository driver's `value_text`, and `text` beside `text_truncated: true`, such as a browser selector's. A claim on such a text fails rather than judge only its start.

## Saving part of a value

`save` keeps the whole value at a path. To keep part of a text, give `from` and `capture`, a pattern with exactly one group; the group's first match is saved:

```yaml
save:
  box: $.body.id
  code: { from: $.body.url, capture: "[?&]code=([0-9a-f]+)" }
```

The pattern is RE2, searched anywhere in the text (anchor it with `^` and `$` if it must match all of it), at most 1 KiB, and can't hold `${...}`; write `(?:...)` for a group that isn't saved. Validation refuses (exit 3) a pattern that doesn't compile or has no group or more than one (EXE-02), and a mapping without `from`, with neither `capture` nor `secret`, with an empty `capture` or with any other key. At run time a value that isn't text, a pattern that doesn't match and a group that matches empty text fail the step, as a missing value does; a cut text fails as above. When the whole value is redacted, the saved part is scrubbed too. Older Proctor versions refuse the mapping form (feature `save.capture`).

A value that must not appear in the output, such as a one-time code in a field the driver doesn't redact, takes `secret: true`, with or without `capture`: `code: { from: $.body.url, capture: "code=([0-9a-f]+)", secret: true }`. Once saved, it is scrubbed like a credential, `[redacted]` in the terminal, the report and transcripts, wherever it appears, the request that later sends it included. A secret value with text of fewer than 4 characters, or with no text or number, fails the step, since it couldn't be hidden. Older Proctor versions refuse it (feature `save.secret`).

## References and tables

- References: `${run.id}`, `${<saved>}`, `${<identity>.<key>}`, `${secret.<name>}` and `${fill(N)}` (N `x` characters, at most 16 MiB, in an http body or headers, or a `browser.fill` value up to 1 MiB, only).
- Tables: the first column is the case id, every other column must be used by `identity`, `variant`, `with` or `expect`, `${row.<column>}` can't be in `do`, a key or the case id, a cell can't refer to another, and `${row.x}` inside longer text needs a single-value cell. `setup_from: <table id>` reuses an earlier table's setup, instead of `setup`.

Tables are told apart by their position in the file, so two tables without an `id` keep their own setup, rows and `${run.id}`; messages call such a table `tables[<n>]`.

## State checks across drivers

Every driver reports what a step reads or changes, and on one driver a read counts after an act only when their targets relate: http and ws compare paths as above (`ws.send` and `ws.messages` take their connection's path, `http.frames` its stream's), dynamodb compares table aliases, sqs queue aliases, postgres connection names, ses always reads `sent`, stub reads and fails `routes/<id>` (`routes` for every route), otel reads `spans` or `logs`, docker reads `container/<name>`, `network/<name>`, `volume/<name>` or `containers`, `networks`, `volumes`, redis reads `key/<key>` (its key or match with each `${...}` as `{}`), cli reports its command's `targets`, or `command/<name>` for an observe command without them, and browser acts on `page/<path>` of its `browser.open` (`browser.fill` and `browser.click` take their handle's), while `browser.page` never counts as a state check. A read on another driver counts only when `proctor.yaml` declares that the act shows there (INT-20):

```yaml
# proctor/proctor.yaml
relations:
  - act: { driver: http, target: "/v1/messages/preferences/{}" }
    shows:
      - { driver: dynamodb, target: preferences }
  - act: { driver: sqs, target: outbox }
    shows:
      - { driver: ses, target: sent }
```

Declare a relation only when the application's code shows that the act changes what the read reads. EXE-07's message names the relation that would make a refused read on another driver count. A relation must name targets that steps report, and every `shows` entry must be used by an act and a later read in some scenario, or `validate` and `run` fail with exit 3; quote a target holding `{}`. `proctor diff` lists each added `shows` entry as a `state_check_declared` weakening.

A second driver of the same official program, or the same `program`, that reaches the same application takes the first one's policy with `policy_from: http` instead of its own `policy` (INT-18), so the two lists can't drift apart, and a read on either can be the state check of a write on the other.
