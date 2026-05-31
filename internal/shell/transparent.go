package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// ToolExecutor is a callback to invoke a structured tool in-process.
type ToolExecutor interface {
	Execute(ctx context.Context, toolName string, inputJSON string) (string, bool, error)
}

type toolExecutorContextKey string

const ToolExecutorContextKey toolExecutorContextKey = "tool_executor"

// WithToolExecutor attaches a ToolExecutor to the context.
func WithToolExecutor(ctx context.Context, executor ToolExecutor) context.Context {
	return context.WithValue(ctx, ToolExecutorContextKey, executor)
}

// GetToolExecutor resolves the ToolExecutor from the context.
func GetToolExecutor(ctx context.Context) ToolExecutor {
	if val := ctx.Value(ToolExecutorContextKey); val != nil {
		if executor, ok := val.(ToolExecutor); ok {
			return executor
		}
	}
	return nil
}

// transparentRouteHandler attempts to intercept bare commands and route them to structured Go tools.
func transparentRouteHandler() func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}

			executor := GetToolExecutor(ctx)
			if executor == nil {
				return next(ctx, args)
			}

			toolName, inputJSON, ok := MatchAndRouteCommand(args)
			if !ok {
				return next(ctx, args)
			}

			slog.Info("Bash transparent route match", "cmd", args, "tool", toolName, "input", inputJSON)
			output, isErr, err := executor.Execute(ctx, toolName, inputJSON)

			hc := interp.HandlerCtx(ctx)
			if err != nil {
				fmt.Fprintln(hc.Stderr, err.Error())
				return interp.ExitStatus(1)
			}

			if isErr {
				fmt.Fprint(hc.Stderr, output)
				return interp.ExitStatus(1)
			}

			// 特判 grep 找不到匹配的情况，模拟进程退出码 1
			isGrepCmd := false
			if len(args) > 0 {
				cmdBase := args[0]
				if cmdBase == "grep" || cmdBase == "rg" || cmdBase == "ag" || cmdBase == "ack" {
					isGrepCmd = true
				}
			}
			if isGrepCmd && output == "No matches found." {
				fmt.Fprint(hc.Stdout, output)
				return interp.ExitStatus(1)
			}

			fmt.Fprint(hc.Stdout, output)
			return nil
		}
	}
}

func MatchAndRouteCommand(args []string) (string, string, bool) {
	if len(args) == 0 {
		return "", "", false
	}

	cmd := args[0]
	var toolName string
	var params map[string]any
	var ok bool

	switch cmd {
	case "rg", "grep", "ag", "ack":
		toolName, params, ok = parseGrep(args)
	case "find", "fd":
		toolName, params, ok = parseFind(args)
	case "ls", "tree":
		toolName, params, ok = parseLs(args)
	case "cat", "head":
		toolName, params, ok = parseCat(args)
	default:
		return "", "", false
	}

	if !ok {
		return "", "", false
	}

	inputBytes, err := json.Marshal(params)
	if err != nil {
		return "", "", false
	}
	return toolName, string(inputBytes), true
}

func parseGrep(args []string) (string, map[string]any, bool) {
	ignoreCase := false
	literal := false
	filesOnly := false
	pattern := ""
	var paths []string

	for i := 1; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if !strings.HasPrefix(arg, "--") {
				valid := true
				for j := 1; j < len(arg); j++ {
					c := arg[j]
					switch c {
					case 'r', 'n', 'H', 'E':
						// accepted common flags
					case 'i':
						ignoreCase = true
					case 'F':
						literal = true
					case 'l':
						filesOnly = true
					default:
						valid = false
					}
				}
				if !valid {
					return "", nil, false
				}
			} else {
				switch arg {
				case "--ignore-case":
					ignoreCase = true
				case "--fixed-strings":
					literal = true
				case "--files-with-matches":
					filesOnly = true
				default:
					return "", nil, false
				}
			}
		} else {
			if pattern == "" {
				pattern = arg
			} else {
				paths = append(paths, arg)
			}
		}
	}

	if pattern == "" {
		return "", nil, false
	}

	path := ""
	if len(paths) > 0 {
		path = paths[0]
	}
	if hasGlob(path) {
		return "", nil, false
	}

	params := map[string]any{
		"mode":        "content",
		"pattern":     pattern,
		"path":        path,
		"ignore_case": ignoreCase,
		"literal":     literal,
		"files_only":  filesOnly,
	}
	return "Search", params, true
}

func parseFind(args []string) (string, map[string]any, bool) {
	cmd := args[0]
	path := ""
	pattern := ""

	if cmd == "find" {
		for i := 1; i < len(args); i++ {
			arg := args[i]
			if arg == "-name" || arg == "--name" {
				if i+1 < len(args) {
					pattern = args[i+1]
					i++
				} else {
					return "", nil, false
				}
			} else if strings.HasPrefix(arg, "-") {
				return "", nil, false
			} else {
				if path == "" {
					path = arg
				} else {
					return "", nil, false
				}
			}
		}
		if pattern == "" {
			pattern = "*"
		}
	} else {
		for i := 1; i < len(args); i++ {
			arg := args[i]
			if strings.HasPrefix(arg, "-") {
				return "", nil, false
			}
			if pattern == "" {
				pattern = arg
			} else if path == "" {
				path = arg
			} else {
				return "", nil, false
			}
		}
	}

	if hasGlob(path) {
		return "", nil, false
	}

	params := map[string]any{
		"mode":    "files",
		"pattern": pattern,
		"path":    path,
	}
	return "Search", params, true
}

func parseLs(args []string) (string, map[string]any, bool) {
	cmd := args[0]
	path := ""
	depth := 0

	if cmd == "ls" {
		for i := 1; i < len(args); i++ {
			arg := args[i]
			if strings.HasPrefix(arg, "-") {
				if !strings.HasPrefix(arg, "--") {
					for j := 1; j < len(arg); j++ {
						c := arg[j]
						switch c {
						case 'l', 'a', 'h', 'F', '1':
							// accepted flags
						default:
							return "", nil, false
						}
					}
				} else {
					return "", nil, false
				}
			} else {
				if path == "" {
					path = arg
				} else {
					return "", nil, false
				}
			}
		}
	} else {
		for i := 1; i < len(args); i++ {
			arg := args[i]
			if arg == "-L" {
				if i+1 < len(args) {
					d := 0
					_, err := fmt.Sscanf(args[i+1], "%d", &d)
					if err != nil {
						return "", nil, false
					}
					depth = d
					i++
				} else {
					return "", nil, false
				}
			} else if strings.HasPrefix(arg, "-") {
				return "", nil, false
			} else {
				if path == "" {
					path = arg
				} else {
					return "", nil, false
				}
			}
		}
	}

	if hasGlob(path) {
		return "", nil, false
	}

	params := map[string]any{
		"path": path,
	}
	if depth > 0 {
		params["depth"] = depth
	}
	return "ReadDir", params, true
}

func parseCat(args []string) (string, map[string]any, bool) {
	cmd := args[0]
	filePath := ""
	limit := 0
	offset := 0

	if cmd == "cat" {
		for i := 1; i < len(args); i++ {
			arg := args[i]
			if strings.HasPrefix(arg, "-") {
				return "", nil, false
			}
			if filePath == "" {
				filePath = arg
			} else {
				return "", nil, false
			}
		}
	} else if cmd == "head" {
		for i := 1; i < len(args); i++ {
			arg := args[i]
			if arg == "-n" {
				if i+1 < len(args) {
					l := 0
					_, err := fmt.Sscanf(args[i+1], "%d", &l)
					if err != nil {
						return "", nil, false
					}
					limit = l
					i++
				} else {
					return "", nil, false
				}
			} else if strings.HasPrefix(arg, "-") {
				l := 0
				_, err := fmt.Sscanf(arg, "-%d", &l)
				if err == nil {
					limit = l
				} else {
					return "", nil, false
				}
			} else {
				if filePath == "" {
					filePath = arg
				} else {
					return "", nil, false
				}
			}
		}
		if limit == 0 {
			limit = 10
		}
	} else {
		return "", nil, false
	}

	if filePath == "" {
		return "", nil, false
	}

	if hasGlob(filePath) {
		return "", nil, false
	}

	params := map[string]any{
		"file_path": filePath,
		"fold":      false,
	}
	if limit > 0 {
		params["limit"] = limit
	}
	if offset > 0 {
		params["offset"] = offset
	}
	return "Read", params, true
}

// ParseCommandLine parses a simple command string into argument slice,
// resolving quotes and basic word structures. If it's a complex command
// (contains pipes, redirections, or multiple statements), it returns nil, false.
func ParseCommandLine(cmdStr string) ([]string, bool) {
	file, err := syntax.NewParser().Parse(strings.NewReader(cmdStr), "")
	if err != nil {
		return nil, false
	}
	// We only intercept a single statement.
	if len(file.Stmts) != 1 {
		return nil, false
	}
	stmt := file.Stmts[0]
	if stmt.Background || stmt.Coprocess || len(stmt.Redirs) > 0 {
		return nil, false
	}
	cmd, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok {
		return nil, false
	}
	// Avoid env vars prefixes (like "ENV_VAR=1 grep ...")
	if len(cmd.Assigns) > 0 {
		return nil, false
	}

	var args []string
	cfg := &expand.Config{
		Env: expand.ListEnviron(),
	}
	for _, word := range cmd.Args {
		val, err := expand.Document(cfg, word)
		if err != nil {
			return nil, false
		}
		args = append(args, val)
	}
	return args, true
}

func hasGlob(s string) bool {
	return strings.ContainsAny(s, "*?[]")
}

