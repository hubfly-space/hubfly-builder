//go:build windows

package main

import "fmt"

func runServer() error {
	return fmt.Errorf("the Hubcell build server requires Linux; on Windows use hubfly-builder offline inspect [--path <project>] [--config <hubfly.build.json>]")
}
