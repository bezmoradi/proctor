package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/runner"
)

type validateOptions struct {
	common
	commit string
}

func newValidateCmd(stdout, stderr io.Writer) *cobra.Command {
	var o validateOptions
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check the configuration, manifest and specifications, with each driver's own checks, without contacting the application",
		Example: `  proctor validate
  proctor validate --commit HEAD~1 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(validate(o, stdout, stderr))
		},
	}
	addCommon(cmd, &o.common)
	cmd.Flags().StringVar(&o.commit, "commit", "", "read files from this commit instead of the working tree")
	return cmd
}

func validate(o validateOptions, stdout, stderr io.Writer) int {
	c, commit := o.common, o.commit
	repo, err := gitrepo.Open(c.repo)
	if err != nil {
		fmt.Fprintf(stderr, "proctor: %s\n", safe(err.Error()))
		return exitInvalid
	}
	var src integrity.Source = integrity.WorkTreeSource{Root: repo.Dir()}
	if commit != "" {
		cs, err := integrity.NewCommitSource(repo, commit)
		if err != nil {
			fmt.Fprintf(stderr, "proctor: %s\n", safe(err.Error()))
			return exitInvalid
		}
		src = cs
	}
	r := integrity.Load(src, c.config)
	dc := &driverCheck{}
	if r.Config != nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		dc.drivers, dc.setup = describeSteps(ctx, r, nil, runner.Options{})
	}
	code := r.ExitCode()
	if dc.setup != nil && dc.setup.ExitCode > code {
		code = dc.setup.ExitCode
	}
	return write(stdout, c.json, "VALIDATE", r, dc, code, resultName(code))
}
