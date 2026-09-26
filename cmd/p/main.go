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
	"time"

	"github.com/lgvo/p.ai/internal/attachment"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/daemon"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errAPIExit) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "p:", err)
		os.Exit(1)
	}
}

var errAPIExit = errors.New("api error")

var buildVersion = "0.1.0-dev"

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	if args[0] == "version" {
		if len(args) != 1 {
			return usage()
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
			ControlAPI   int          `json:"control_api_version"`
			PluginAPI    string       `json:"plugin_api_version"`
			Dependencies []dependency `json:"dependencies"`
		}{buildVersion, runtime.Version(), 1, plugin.APIVersion, deps})
	}
	if len(args) == 1 && args[0] == "git-hook" {
		return gitservice.RunPreReceiveHook(context.Background(), os.Stdin)
	}
	if args[0] == "attach-helper" && len(args) == 1 {
		return attachment.Helper(os.NewFile(3, "attachment-carrier"))
	}
	if args[0] == "attach" {
		if len(args) != 3 {
			return errors.New("usage: p attach CONTROL_SOCKET SESSION_UUID")
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		return attachment.Client(ctx, args[1], args[2])
	}
	if args[0] == "daemon" {
		if len(args) != 2 {
			return usage()
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
		if len(args) != 3 && len(args) != 4 {
			return usage()
		}
		params := json.RawMessage(`{"v":1}`)
		if len(args) == 4 {
			params = json.RawMessage(args[3])
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		response, err := control.Call(ctx, args[1], args[2], params)
		if err != nil {
			_ = printJSON(map[string]any{"error": map[string]any{"kind": "transport", "message": err.Error()}})
			return errAPIExit
		}
		if _, err := os.Stdout.Write(append(response, '\n')); err != nil {
			return err
		}
		var envelope struct {
			Error *control.RPCError `json:"error"`
		}
		if err := json.Unmarshal(response, &envelope); err != nil {
			return err
		}
		if envelope.Error != nil {
			return errAPIExit
		}
		return nil
	}
	if len(args) < 3 || args[0] != "plugins" {
		return usage()
	}
	switch args[1] {
	case "defaults":
		if len(args) != 3 && len(args) != 4 {
			return usage()
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
			return usage()
		}
		p, err := plugin.Conformance(mustAbs(args[2]))
		if err != nil {
			return err
		}
		return printJSON(p)
	case "list":
		if len(args) != 3 {
			return usage()
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
			return usage()
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
			return usage()
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
			return usage()
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
		return usage()
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

func usage() error {
	return errors.New("usage: p version | daemon <trusted-host.json> | attach <socket-path> <session-uuid> | api <socket-path> <method> [json-params] | plugins defaults <absolute-event-log-path> [catalog-dir] | conformance <package-dir> | list <catalog-dir> | activate <trusted-activation.json> | emit|run-event <trusted-activation.json> <event.json> | plan-assets <trusted-activation.json> <plugin-id>")
}
