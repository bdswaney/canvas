package main

import "embed"

// Build the frontend with mise run build before compiling Go. The embed lives
// in its own file so it is obvious that main, and only main, carries the
// built assets; every other package is importable without them.
//
//go:embed dist
var frontend embed.FS
