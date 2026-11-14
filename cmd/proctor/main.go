// Command proctor verifies an application against a protected behavioral
// contract. See prd.md.
package main

import (
	"os"

	"proctor/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
