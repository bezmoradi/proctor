# proctor diff reference

## proctor diff kinds

| Kind | Weakening |
|---|---|
| `scenario_removed`, `table_row_removed`, `step_removed`, `setup_step_removed` | removed, with the number of `assertions_lost` (a setup step that became cleanup counts as removed) |
| `step_moved_to_setup` | a verification step became setup |
| `invariant_removed`, `invariant_changed` | an environment invariant was removed, or its request, timing or expectations changed |
| `cleanup_step_removed`, `step_moved_to_cleanup`, `cleanup_made_conditional`, `cleanup_condition_changed` | a cleanup step was removed; a verification step became cleanup; a cleanup step gained `unless_passed`, or names another step there (see [Cleanup](../guides/specifications.md#cleanup)) |
| `driver_changed`, `driver_origin_changed` | a step's `do` names another driver; a driver's `official` or `program` changed, or it switched between them |
| `driver_program_changed` | a repository driver's `sha256` pin was added or changed, so its steps run another program |
| `operation_now_observes` | a repository driver's operation went from `act` to `observe` |
| `variant_accepted`, `variant_removed` | a variant added to `accepted_variants`; a step no longer uses its variant |
| `state_check_waived`, `state_check_waiver_changed` | `no_state_check` added or changed |
| `time_check_changed`, `wait_longer`, `window_shorter`, `sampling_sparser` | a time check was added, removed or changed kind, waits longer, watches for less time or samples less often |
| `max_window_longer`, `max_samples_higher`, `min_interval_shorter` | a `time_limits` value loosened |
| `redaction_removed` | a `redaction.json_paths` or `redaction.headers` entry removed |
| `identity_check_removed`, `identity_check_changed` | the identity check was removed, or its driver, path or `user_id` changed; `identity_check_added` is listed with the other changes |
| `app_logs_removed`, `app_log_level_removed`, `app_log_field_changed`, `app_log_unparsed_allowed`, `app_log_flush_shorter` | the app-log check loosened; `app_log_flush_shorter` when `flush_wait` became shorter for a file or an `otel` source, an unset value counting as that source's default |
| `log_error_allowed`, `log_count_removed`, `log_count_raised` | `expect_logs` allows more |
| `not_analysed` | one side doesn't parse, so read the file diff |

Every added scenario, table row and step is listed under `Added steps`, one line each with its act and every expectation (`added_steps` with `--json`, and in the authoritative job summary), since a new expectation that guesses wrong is as easy to miss as a removed one. An assertion that moved to another step sending the same request is listed under `Moved assertions (not lost)` as `assertion_moved` (`assertion_moves` with `--json`), not as a weakening. A scenario whose place among the others changed is listed as `reordered`, not as a weakening. A removed pin (`driver_pin_removed`) and an added, removed or `observe`-to-`act` operation (`operations_changed`) are listed with the driver changes, not as weakenings.
