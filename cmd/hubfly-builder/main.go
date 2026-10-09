package main

import (
	"fmt"
	"io"
	"os"

	"hubfly-builder/internal/offline"
)

var version = "dev"

func run(args []string, output io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "version":
			_, err := fmt.Fprintln(output, version)
			return err
		case "offline":
			return offline.Run(args[1:])
		}
	}
	return runServer()
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
