package main

import (
	"context"
	"os"
)

func main() {
	os.Exit(runCLI(context.Background(), os.Stdout, os.Stderr, os.Args[1:]))
}
