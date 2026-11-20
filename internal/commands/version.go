package commands

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newVersionCmd(stdout io.Writer) *cobra.Command {
	var listFeatures, asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print Proctor's version, and with --features the features this build has",
		Example: `  proctor version
  proctor version --features
  proctor version --json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			switch {
			case asJSON:
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(struct {
					Version  string   `json:"version"`
					Features []string `json:"features"`
				}{Version, sortedFeatures()})
			case listFeatures:
				for _, f := range sortedFeatures() {
					fmt.Fprintln(stdout, f)
				}
			default:
				fmt.Fprintln(stdout, Version)
			}
		},
	}
	cmd.Flags().BoolVar(&listFeatures, "features", false, "print the names of the features this build has, sorted, one per line")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print {\"version\": ..., \"features\": [...]}")
	return cmd
}
