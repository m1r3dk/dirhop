package app

import "errors"

var (
	ErrNoSession          = errors.New("session not available")
	ErrPathNotFound       = errors.New("path not found")
	ErrNetwork            = errors.New("network failure")
	ErrDownload           = errors.New("download failure")
	ErrUnsupportedListing = errors.New("unsupported directory listing")
)
