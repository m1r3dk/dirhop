package main

import (
	"fmt"
	"os"

	"github.com/1jehuang/dirclone/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cli.ExitCode(err))
	}
}
