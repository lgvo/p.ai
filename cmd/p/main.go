package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/lgvo/p.ai/internal/gitservice"
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
			Version      string       `json:"version"`
			GoVersion    string       `json:"go_version"`
			ControlAPI   string       `json:"control_protocol"`
			PluginAPI    string       `json:"plugin_api_version"`
			Dependencies []dependency `json:"dependencies"`
		}{buildVersion, runtime.Version(), api.Protocol, plugin.APIVersion, deps})
	}
	if len(args) == 1 && args[0] == "git-hook" {
		return gitservice.RunPreReceiveHook(context.Background(), os.Stdin)
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
		activation, err := plugin.DefaultActivation(catalog, args[2])
		if err != nil {
			return err
		}
		return printJSON(activation)
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
	case "activate":
		if len(args) != 3 {
			return pluginsUsage()
		}
		active, err := plugin.LoadActivation(mustAbs(args[2]))
		if err != nil {
			return err
		}
		// Do not print trusted per-plugin config, such as host paths.
		var summary []struct {
			ID         string   `json:"id"`
			Capability string   `json:"capability"`
			SHA256     string   `json:"sha256"`
			Grants     []string `json:"grants"`
		}
		for _, item := range active {
			summary = append(summary, struct {
				ID         string   `json:"id"`
				Capability string   `json:"capability"`
				SHA256     string   `json:"sha256"`
				Grants     []string `json:"grants"`
			}{item.Package.Manifest.ID, item.Package.Manifest.Capability, item.Package.SHA256, item.Grants})
		}
		return printJSON(summary)
	case "plan-assets":
		if len(args) != 4 {
			return pluginsUsage()
		}
		active, err := plugin.LoadActivation(mustAbs(args[2]))
		if err != nil {
			return err
		}
		for _, selected := range active {
			if selected.Package.Manifest.ID == args[3] {
				plan, err := plugin.PlanAssets(selected)
				if err != nil {
					return err
				}
				return printJSON(plan)
			}
		}
		return errors.New("selected asset package not active")
	case "emit", "run-event":
		if len(args) != 4 {
			return pluginsUsage()
		}
		active, err := plugin.LoadActivation(mustAbs(args[2]))
		if err != nil {
			return err
		}
		fd, err := syscall.Open(args[3], syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), args[3])
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if !info.Mode().IsRegular() {
			f.Close()
			return errors.New("event file must be regular")
		}
		data, err := io.ReadAll(io.LimitReader(f, 4097))
		f.Close()
		if err != nil {
			return err
		}
		event, err := plugin.ParseEvent(data)
		if err != nil {
			return err
		}
		result, err := plugin.DispatchEvent(context.Background(), active, event)
		if err != nil {
			return err
		}
		if args[1] == "run-event" {
			return printJSON(result)
		}
		return printJSON(map[string]string{"status": result.Status, "event_id": event.ID})
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
	return usageError("usage: p plugins install HOST_JSON PACKAGE APPROVED_SHA256 | update HOST_JSON OLD_SHA256 PACKAGE APPROVED_SHA256 | remove HOST_JSON SHA256 | defaults <absolute-event-log-path> [catalog-dir] | conformance <package-dir> | list <catalog-dir> | activate <trusted-activation.json> | emit|run-event <trusted-activation.json> <event.json> | plan-assets <trusted-activation.json> <plugin-id>")
}
