package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnersRefuseMisbehavingModules(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go compiler unavailable")
	}
	modules := map[string][]byte{}
	for _, mode := range []string{"loop", "flood", "exit"} {
		out := filepath.Join(t.TempDir(), mode+".wasm")
		cmd := exec.Command(goPath, "build", "-ldflags", "-X main.mode="+mode, "-o", out, "./testdata/wasi-misbehave")
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s module: %v: %s", mode, err, output)
		}
		if modules[mode], err = os.ReadFile(out); err != nil {
			t.Fatal(err)
		}
	}
	selected := func(t *testing.T, manifest, entry, mode string, grants []string, config any) Active {
		t.Helper()
		dir := t.TempDir()
		raw, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, entry), modules[mode], 0600); err != nil {
			t.Fatal(err)
		}
		pkg, err := Conformance(dir)
		if err != nil {
			t.Fatal(err)
		}
		active := Active{Package: pkg, Grants: grants}
		if config != nil {
			active.Config, _ = json.Marshal(config)
		}
		return active
	}
	private := t.TempDir()
	runners := []struct {
		name string
		run  func(ctx context.Context, mode string) error
		want map[string]string
	}{
		{"event handler", func(ctx context.Context, mode string) error {
			active := selected(t, "../../plugins/examples/filter-log/plugin.json", "filter.wasm", mode, []string{"event.file.append"},
				FileLogConfig{Path: filepath.Join(private, "events.ndjson"), MaxBytes: 4096})
			event := Event{Schema: "p.event/v1", ID: "e-1", Kind: "session.condition_changed", OccurredAt: time.Now().UTC().Format(time.RFC3339), Instance: "p-one", Fields: map[string]string{"condition": "ready"}}
			_, err := RunEvent(ctx, active, event)
			return err
		}, map[string]string{"loop": "plugin execution timed out or was cancelled", "flood": "plugin output exceeds limit", "exit": "plugin execution failed"}},
		{"source Git", func(ctx context.Context, mode string) error {
			active := selected(t, "testdata/git-alternate/plugin.json", "git.wasm", mode, []string{"git.project"}, nil)
			_, err := RunSourceGit(ctx, active, GitCommand{Kind: "git.transport.plan", Project: "team/app", Service: "upload", Ceilings: &GitTransportPlan{MaxInputBytes: GitCoreInputCeiling, MaxDurationMS: GitCoreDurationMS}})
			return err
		}, map[string]string{"loop": "source-git execution timed out", "flood": "source-git output exceeded limit", "exit": "source-git execution failed"}},
	}
	for _, runner := range runners {
		for _, mode := range []string{"loop", "flood", "exit"} {
			t.Run(runner.name+" "+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				defer cancel()
				err := runner.run(ctx, mode)
				if err == nil || err.Error() != runner.want[mode] {
					t.Fatalf("answer %v, want %q", err, runner.want[mode])
				}
			})
		}
	}
}
