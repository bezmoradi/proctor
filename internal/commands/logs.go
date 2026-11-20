package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"proctor/internal/applogs"
	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/runner"
	"proctor/internal/transcript"
)

const maxNormalizeBytes = 64 << 20

type normalizeOptions struct {
	repo, config string
	fields       []string
}

func newLogsCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Work with application logs outside a run",
	}
	cmd.AddCommand(newLogsNormalizeCmd(stdout, stderr))
	return cmd
}

func newLogsNormalizeCmd(stdout, stderr io.Writer) *cobra.Command {
	var o normalizeOptions
	cmd := &cobra.Command{
		Use:   "normalize <file>",
		Short: "Print a log file's lines as an approved log shows them: only app_logs.approved.fields, scrubbed, to stdout only",
		Example: `  proctor logs normalize incident.log --field request_id=8f2c > incident.normalized.log
  diff incident.normalized.log "$TMPDIR/proctor-received/demo/0a1b2c3d4e5f/users/RPT-001.log"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(normalizeLog(o, args[0], stdout, stderr))
		},
	}
	f := cmd.Flags()
	f.StringVarP(&o.repo, "repo", "r", ".", "repository directory")
	f.StringVarP(&o.config, "config", "c", defaultConfig, "path of proctor.yaml inside the repository")
	f.StringArrayVar(&o.fields, "field", nil, "keep only lines whose field has this value, as k=v (repeat for more; all must match)")
	return cmd
}

func normalizeLog(o normalizeOptions, file string, stdout, stderr io.Writer) int {
	refuse := func(code int, format string, args ...any) int {
		fmt.Fprintln(stderr, "proctor logs normalize: "+safe(fmt.Sprintf(format, args...)))
		return code
	}
	var filters []applogs.Filter
	seen := map[string]bool{}
	for _, kv := range o.fields {
		k, v, ok := strings.Cut(kv, "=")
		switch {
		case !ok || k == "":
			return refuse(exitInvalid, "--field %q isn't k=v", kv)
		case seen[k]:
			return refuse(exitInvalid, "--field names %s twice; a line has one value per field", k)
		}
		seen[k] = true
		filters = append(filters, applogs.Filter{Field: k, Value: v})
	}
	repo, err := gitrepo.Open(o.repo)
	if err != nil {
		return refuse(exitInvalid, "%v", err)
	}
	rep := integrity.LoadConfig(integrity.WorkTreeSource{Root: repo.Dir()}, o.config)
	if rep.Config == nil || rep.ExitCode() != exitOK {
		for _, f := range rep.Findings {
			fmt.Fprintf(stderr, "proctor logs normalize: %s %s\n", safe(f.Rule), safe(strings.TrimPrefix(f.Path+": ", ": ")+f.Message))
		}
		return refuse(exitInvalid, "%s doesn't validate; fix it, then normalize", o.config)
	}
	cfg := rep.Config
	settings := cfg.AppLogs.ApprovedSettings()
	if settings == nil {
		return refuse(exitInvalid, "%s has no app_logs.approved, so there are no fields to keep; add app_logs.approved.fields first", o.config)
	}
	if fi, err := os.Lstat(file); err == nil && !fi.Mode().IsRegular() {
		return refuse(exitInvalid, "%s is a %s; give the log file itself, a regular file", file, describeMode(fi.Mode()))
	}
	data, err := readPrivateFile(file, maxNormalizeBytes)
	if err != nil {
		return refuse(exitError, "%v", err)
	}
	rules := applogs.Rules{Format: cfg.AppLogs.FormatName(), Levels: cfg.AppLogs.EffectiveLevels(), LevelField: cfg.AppLogs.LevelKey(),
		MessageField: cfg.AppLogs.MessageKey(), Numbers: cfg.AppLogs.NumberLevels()}
	scrub, _ := transcript.CompileLogRules(settings.Scrub, settings.Fields, rules.MessageField)
	text := strings.TrimSuffix(string(data), "\n")
	var lines []string
	if text != "" {
		for _, l := range strings.Split(text, "\n") {
			lines = append(lines, strings.TrimSuffix(l, "\r"))
		}
	}
	col := rules.Collect(lines, settings.Fields, runner.SensitiveField(cfg.Redaction), filters)
	out := applogs.Render(col, applogs.Scrub{Clean: func(s string) string { return s }, Rules: scrub, Message: rules.MessageField, Sorted: settings.Sorted()})
	if _, err := stdout.Write(out); err != nil {
		return refuse(exitError, "writing to stdout: %v", err)
	}
	return exitOK
}
