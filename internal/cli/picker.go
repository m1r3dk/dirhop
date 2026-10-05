package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/m1r3dk/dirhop/internal/app"
	"github.com/m1r3dk/dirhop/internal/model"
)

// pickSession lists sessions and, on a TTY, asks the user to choose one. It
// never prompts when stdin is not a terminal, so scripts fail fast instead.
func pickSession(ctx context.Context, a *app.App, in *os.File, out io.Writer) (*model.Site, error) {
	sites, err := a.Sessions.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(sites) == 0 {
		return nil, fmt.Errorf("%w: run `dirhop <URL>` to index a site", app.ErrNoSession)
	}
	if len(sites) == 1 {
		return a.Sessions.Use(ctx, sites[0].Name)
	}
	fmt.Fprintln(out, "Available sessions:")
	for i, s := range sites {
		fmt.Fprintf(out, "  %d. %s  (%s, %d files)\n", i+1, s.Name, s.Hostname, s.FileCount)
	}
	if !term.IsTerminal(int(in.Fd())) {
		return nil, fmt.Errorf("%w: choose one with `dirhop session use <name>` or -s", app.ErrNoSession)
	}
	fmt.Fprint(out, "Select session: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		return nil, app.ErrNoSession
	}
	choice := strings.TrimSpace(line)
	if n, convErr := strconv.Atoi(choice); convErr == nil && n >= 1 && n <= len(sites) {
		choice = sites[n-1].Name
	}
	site, err := a.Sessions.Use(ctx, choice)
	if err != nil {
		return nil, fmt.Errorf("%w: unknown session %q", app.ErrNoSession, choice)
	}
	return site, nil
}
