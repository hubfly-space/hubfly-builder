package autodetect

import (
	"fmt"
	"path"
	"strings"
)

func jsExecCommand(runtime, subcommand string) string {
	subcommand = strings.TrimSpace(subcommand)
	if runtime == "bun" {
		return "bunx " + subcommand
	}
	return "npx " + subcommand
}

func prefixCommand(dir, cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if dir == "" || dir == "." {
		return cmd
	}
	return fmt.Sprintf("cd '%s' && %s", escapeSingleQuotes(dir), cmd)
}

func joinContainerPath(base, child string) string {
	base = strings.TrimSpace(base)
	child = strings.TrimSpace(child)
	switch {
	case base == "", base == ".":
		return path.Clean(child)
	case child == "", child == ".":
		return path.Clean(base)
	default:
		return path.Clean(path.Join(base, child))
	}
}
