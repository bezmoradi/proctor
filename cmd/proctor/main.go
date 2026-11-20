package main

import (
	"os"

	"proctor/internal/commands"
)

func main() {
	os.Exit(commands.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
