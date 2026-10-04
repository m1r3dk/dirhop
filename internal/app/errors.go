package app

import "errors"

var (
	ErrNoSession          = errors.New("no active session")
	ErrPathNotFound       = errors.New("path not found")
	ErrNetwork            = errors.New("network failure")
	ErrDownload           = errors.New("download failure")
	ErrUnsupportedListing = errors.New("unsupported directory listing")
)
