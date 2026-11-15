package commands

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newVersionCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print Proctor's version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(stdout, Version)
		},
	}
}
