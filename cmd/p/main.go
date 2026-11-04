package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/attachment"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/daemon"
	"github.com/lgvo/p.ai/internal/eventhandler"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if text := err.Error(); text != "" {
			fmt.Fprintln(os.Stderr, "p:", text)
		}
		os.Exit(exitStatus(err))
	}
}

// errAPIExit ends p api after it printed the daemon's error reply.
var errAPIExit = &exitError{code: exitFailed}

var buildVersion = "0.1.0-dev"

func run(args []string) error {
	if len(args) == 0 {
		printHelp(os.Stderr)
		return &exitError{code: exitUsage}
	}
	switch {
	case args[0] == "help" && len(args) == 1, args[0] == "--help", args[0] == "-h":
		return printHelp(os.Stdout)
	case args[0] == "help" && len(args) == 2 && isNoun(args[1]):
		return printNounHelp(os.Stdout, args[1])
	case isNoun(args[0]) && findCommand(args) == nil && len(args) == 1:
		printNounHelp(os.Stderr, args[0])
		return &exitError{code: exitUsage}
	case isNoun(args[0]) && findCommand(args) == nil && (args[1] == "--help" || args[1] == "-h"):
		return printNounHelp(os.Stdout, args[0])
	}
	if isNoun(args[0]) {
		// SIGHUP too: a closed terminal ends an attachment.
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		return (&cli{stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin,
			stdinTerminal: term.IsTerminal(os.Stdin.Fd()), stderrTerminal: term.IsTerminal(os.Stderr.Fd()),
			pause: pause}).runCommand(ctx, args)
	}
	if args[0] == "version" {
		if len(args) != 1 {
			return usageError("usage: p version")
		}
		type dependency struct {
			Path    string `json:"path"`
			Version string `json:"version"`
			Sum     string `json:"sum,omitempty"`
		}
		deps := []dependency{}
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, module := range info.Deps {
				deps = append(deps, dependency{module.Path, module.Version, module.Sum})
			}
		}
		return printJSON(struct {
			Version              string       `json:"version"`
			GoVersion            string       `json:"go_version"`
			ControlAPI           string       `json:"control_protocol"`
			PluginSchema         string       `json:"plugin_schema"`
			EventHandlerProtocol string       `json:"event_handler_protocol"`
			Dependencies         []dependency `json:"dependencies"`
		}{buildVersion, runtime.Version(), api.Protocol, plugin.ManifestSchema, eventhandler.Version, deps})
	}
	if args[0] == "attach-helper" && len(args) == 1 {
		return attachment.Helper(os.NewFile(3, "attachment-carrier"))
	}
	if args[0] == "tui" {
		return tui.Run(args[1:])
	}
	if args[0] == "daemon" {
		if len(args) != 2 {
			return usageError("usage: p daemon HOST_JSON")
		}
		cfg, err := control.LoadHostConfig(mustAbs(args[1]))
		if err != nil {
			return err
		}
		store, err := control.OpenStore(cfg.StateDir)
		if err != nil {
			return err
		}
		defer store.Close()
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return daemon.Serve(ctx, cfg, store)
	}
	if args[0] == "api" {
		socket, method, params, err := apiArguments(args[1:])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), apiDeadline(method, params))
		defer cancel()
		response, err := api.CallRaw(ctx, socket, method, params)
		if err != nil {
			// A daemon that refused p api's own hello answered an error object;
			// anything else failed before an answer.
			var refused *api.Error
			if errors.As(err, &refused) {
				_ = printJSON(map[string]any{"error": refused})
			} else {
				_ = printJSON(map[string]any{"error": map[string]any{"message": "transport", "detail": err.Error()}})
			}
			return errAPIExit
		}
		if _, err := os.Stdout.Write(append(response, '\n')); err != nil {
			return err
		}
		var envelope struct {
			Error *api.Error `json:"error"`
		}
		if err := json.Unmarshal(response, &envelope); err != nil {
			return err
		}
		if envelope.Error != nil {
			return errAPIExit
		}
		return nil
	}
	if args[0] != "plugins" {
		return usage()
	}
	if len(args) < 3 {
		return pluginsUsage()
	}
	if args[1] == "install" || args[1] == "update" || args[1] == "remove" {
		return managePlugins(args)
	}
	switch args[1] {
	case "defaults":
		if len(args) != 3 && len(args) != 4 {
			return pluginsUsage()
		}
		catalog := ""
		if len(args) == 4 {
			catalog = mustAbs(args[3])
		} else {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			executable, err = filepath.EvalSymlinks(executable)
			if err != nil {
				return err
			}
			catalog = filepath.Join(filepath.Dir(filepath.Dir(executable)), "share", "p", "plugins")
		}
		defaults, err := plugin.DefaultSelections(catalog, args[2])
		if err != nil {
			return err
		}
		return printJSON(defaults)
	case "conformance":
		if len(args) != 3 {
			return pluginsUsage()
		}
		p, err := plugin.Conformance(mustAbs(args[2]))
		if err != nil {
			return err
		}
		return printJSON(p)
	case "list":
		if len(args) != 3 {
			return pluginsUsage()
		}
		found, rejected, err := plugin.Discover(mustAbs(args[2]))
		if err != nil {
			return err
		}
		return printJSON(struct {
			Packages []plugin.Package  `json:"packages"`
			Rejected map[string]string `json:"rejected"`
		}{found, rejected})
	case "run-event":
		if len(args) != 4 {
			return pluginsUsage()
		}
		return runEvent(mustAbs(args[2]), args[3])
	default:
		return pluginsUsage()
	}
}

func mustAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func printJSON(value any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

// pluginsUsage is the usage error of p plugins.
func pluginsUsage() error {
	return usageError("usage: p plugins install HOST_JSON PACKAGE APPROVED_SHA256 | update HOST_JSON OLD_SHA256 PACKAGE APPROVED_SHA256 | remove HOST_JSON SHA256 | defaults <absolute-event-log-path> [catalog-dir] | conformance <package-dir> | list <catalog-dir> | run-event HOST_JSON <event.json>")
}
