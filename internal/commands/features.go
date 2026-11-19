package commands

import "sort"

var features = []string{
	"app_logs",
	"app_logs.logfmt",
	"approve",
	"approved_logs",
	"auth03.distinct_ids",
	"change_kind",
	"cleanup",
	"docker",
	"docker.volume_socket",
	"exe09.related_guard",
	"exe03.no_body",
	"exe09.waivers_guard_nothing",
	"exe11.fact_paths",
	"http.body_base64",
	"http.body_file",
	"http.body_fill",
	"http.body_truncated",
	"http.destructive",
	"http.reads",
	"http.shows",
	"http.stream",
	"http.unused_entries",
	"matchers.differs",
	"matchers.excludes",
	"matchers.matches",
	"matchers.overflow",
	"mutants.generate",
	"mutate",
	"mutate.html",
	"paths.bracket_names",
	"policy_from",
	"progress.table_rows",
	"relations",
	"report.always",
	"report.first_failure",
	"report.status_response",
	"sqs.peek_hold",
	"step.timeout",
	"table.time",
	"tables.covers",
	"time14.accumulates",
	"transcripts",
	"validate.no_git",
	"waive.rows",
}

func sortedFeatures() []string {
	out := append([]string(nil), features...)
	sort.Strings(out)
	return out
}
