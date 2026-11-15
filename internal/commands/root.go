package commands

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"proctor/internal/runner"
)

const devVersion = "0.0.0-phase0"

var Version = devVersion

const (
	exitOK        = 0
	exitFailed    = 1
	exitError     = 2
	exitInvalid   = 3
	exitIntegrity = 4
)

const defaultConfig = "proctor/proctor.yaml"

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func exitWith(code int) error {
	if code == exitOK {
		return nil
	}
	return exitCode(code)
}

type common struct {
	repo, config string
	json         bool
}

func addCommon(cmd *cobra.Command, c *common) {
	f := cmd.Flags()
	f.StringVarP(&c.repo, "repo", "r", ".", "repository directory")
	f.StringVarP(&c.config, "config", "c", defaultConfig, "path of proctor.yaml inside the repository")
	f.BoolVar(&c.json, "json", false, "write machine-readable JSON")
}

func Execute(args []string, stdout, stderr io.Writer) int {
	runner.CoreVersion = Version
	u := &updateCheck{}
	root := newRootCmd(stdout, stderr, u)
	root.SetArgs(args)
	err := root.Execute()
	u.report(stderr)
	var code exitCode
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &code):
		return int(code)
	default:
		fmt.Fprintf(stderr, "proctor: %s\nRun 'proctor --help' for usage.\n", safe(err.Error()))
		return exitInvalid
	}
}

func newRootCmd(stdout, stderr io.Writer, u *updateCheck) *cobra.Command {
	root := &cobra.Command{
		Use:   "proctor",
		Short: "Verify behavior, not implementation",
		Long: `Proctor checks that an application still does what people agreed it should do,
and refuses to report a pass when the agreement itself was quietly changed.

Exit codes: 0 passed, 1 failed, 2 error, 3 invalid, 4 integrity violation.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SetOut(stderr)
			_ = cmd.Help()
			return exitCode(exitInvalid)
		},
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if wantsUpdateCheck(cmd) {
				u.start()
			}
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("{{.Version}}\n")
	root.CompletionOptions.HiddenDefaultCmd = true
	root.AddCommand(
		newValidateCmd(stdout, stderr),
		newIntegrityCmd(stdout, stderr),
		newDiffCmd(stdout, stderr),
		newRunCmd(stdout, stderr),
		newVersionCmd(stdout),
	)
	return root
}
