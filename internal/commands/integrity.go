package commands

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
)

type integrityOptions struct {
	common
	commit string
}

func newIntegrityCmd(stdout, stderr io.Writer) *cobra.Command {
	var o integrityOptions
	cmd := &cobra.Command{
		Use:   "integrity",
		Short: "Run the static integrity checks on a commit, reading Git's object database",
		Example: `  proctor integrity
  proctor integrity --commit main --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(checkIntegrity(o, stdout, stderr))
		},
	}
	addCommon(cmd, &o.common)
	cmd.Flags().StringVar(&o.commit, "commit", "HEAD", "commit to check")
	return cmd
}

func checkIntegrity(o integrityOptions, stdout, stderr io.Writer) int {
	c, commit := o.common, o.commit
	repo, err := gitrepo.Open(c.repo)
	if err != nil {
		fmt.Fprintf(stderr, "proctor: %v\n", err)
		return exitInvalid
	}
	r := integrity.CheckCommit(repo, commit, c.config)
	code := r.ExitCode()
	return write(stdout, c.json, "INTEGRITY", r, code, resultName(code))
}
