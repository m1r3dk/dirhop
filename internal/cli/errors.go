package cli

import (
	"errors"

	"github.com/m1r3dk/dirclone/internal/app"
)

func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrInvalidArguments):
		return 2
	case errors.Is(err, app.ErrNoSession):
		return 3
	case errors.Is(err, app.ErrPathNotFound):
		return 4
	case errors.Is(err, app.ErrNetwork):
		return 5
	case errors.Is(err, app.ErrDownload):
		return 6
	case errors.Is(err, app.ErrUnsupportedListing):
		return 7
	default:
		return 1
	}
}

var ErrInvalidArguments = errors.New("invalid arguments")
