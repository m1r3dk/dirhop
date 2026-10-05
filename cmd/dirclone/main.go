package main

import (
	"fmt"
	"os"

	"github.com/m1r3dk/dirclone/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		if !cli.AlreadyReported(err) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(cli.ExitCode(err))
	}
}
