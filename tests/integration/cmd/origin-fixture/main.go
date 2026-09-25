// origin-fixture exercises the production source-Git origin scope inside the VM.
// It is test-only composition; project association is a later lifecycle step.
package main

import (
	"bytes"
	"charm.land/ssh"
	"charm.land/wish/v2"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	gossh "golang.org/x/crypto/ssh"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
)

func main() {
	var err error
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		err = serve(os.Args[2:])
	} else if len(os.Args) > 1 && os.Args[1] == "serve-publish" {
		err = servePublish(os.Args[2:])
	} else if len(os.Args) > 1 && os.Args[1] == "publish" {
		err = publish(os.Args[2:])
	} else if len(os.Args) > 1 && os.Args[1] == "prepare-publish" {
		err = preparePublish(os.Args[2:])
	} else {
		err = run(os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "origin-fixture:", err)
		os.Exit(1)
	}
}

// Usage: origin-fixture MODE STATE ACTIVATION PROJECT URL [REF COMMIT [REMOTE-REPO NEW-OID]]
func run(a []string) error {
	if len(a) < 5 {
		return errors.New("usage: origin-fixture mode state activation project URL [ref commit [remote-repo new-oid]]")
	}
	mode, state, activation, project, remote := a[0], a[1], a[2], a[3], a[4]
	store, err := control.OpenStore(state)
	if err != nil {
		return err
	}
	defer store.Close()
	active, err := plugin.LoadActivation(activation)
	if err != nil {
		return err
	}
	backend, err := gitservice.New(store, state, active)
	if err != nil {
		return err
	}
	repo, err := backend.RepositoryPath(project)
	if err != nil {
		return err
	}
	existed := false
	if _, err := os.Stat(repo); err == nil {
		existed = true
	} else if !os.IsNotExist(err) {
		return err
	}
	ctx := context.Background()
	if mode == "cancel" {
		c, cancel := context.WithCancel(ctx)
		cancel()
		ctx = c
	} else if mode == "timeout" {
		c, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		ctx = c
	}
	var refs []plugin.GitOriginRef
	var got string
	var configBefore []byte
	started := time.Now()
	err = backend.WithOrigin(ctx, project, func(scope *gitservice.OriginScope) error {
		observed, e := scope.Observe(ctx, remote)
		if e != nil {
			return e
		}
		refs = observed
		if mode == "observe" || mode == "observe-empty" || mode == "refuse" {
			return nil
		}
		if mode != "fetch" && mode != "race" && mode != "alternate" {
			return errors.New("unknown mode")
		}
		if len(a) < 7 {
			return errors.New("missing selected ref and commit")
		}
		ref, expected := a[5], a[6]
		found := false
		for _, v := range observed {
			if v.Ref == ref {
				if v.CommitOID != expected {
					return fmt.Errorf("selected commit changed before fetch: %s", v.CommitOID)
				}
				found = true
			}
		}
		if !found {
			return errors.New("selected ref absent")
		}
		if !existed {
			if e := backend.InitBare(ctx, project); e != nil {
				return e
			}
		}
		configBefore, e = os.ReadFile(filepath.Join(repo, "config"))
		if e != nil {
			return e
		}
		if head, e := os.ReadFile(filepath.Join(repo, "HEAD")); e != nil || string(head) != "ref: refs/heads/main\n" {
			return errors.New("P HEAD changed before fetch")
		}
		if mode == "race" {
			if len(a) != 9 {
				return errors.New("race requires remote repository and new OID")
			}
			cmd := exec.Command("git", "-C", a[7], "update-ref", ref, a[8])
			if output, e := cmd.CombinedOutput(); e != nil {
				return fmt.Errorf("move fixture origin: %w: %s", e, output)
			}
		}
		got, e = scope.Fetch(ctx, ref, expected)
		if mode == "race" {
			if e == nil {
				return errors.New("moved origin ref was accepted")
			}
			if _, e = scope.Fetch(ctx, ref, expected); !errors.Is(e, control.ErrInvalid) {
				return errors.New("failed fetch retained observation")
			}
			return nil
		}
		if e != nil {
			return e
		}
		if got != expected {
			return fmt.Errorf("fetched %s, expected %s", got, expected)
		}
		return nil
	})
	if mode == "cancel" || mode == "timeout" || mode == "refuse" {
		if err == nil {
			return errors.New("refused origin contact succeeded")
		}
		if mode == "refuse" && project == "invalid" && !errors.Is(err, control.ErrInvalid) {
			return fmt.Errorf("invalid transport was rejected for another reason: %w", err)
		}
		if (mode == "cancel" || mode == "timeout") && time.Since(started) >= 5*time.Second {
			return fmt.Errorf("%s did not stop promptly: %v", mode, err)
		}
		err = nil
	}
	if err != nil {
		return err
	}
	if mode == "observe-empty" && len(refs) != 0 {
		return fmt.Errorf("empty origin advertised %d refs", len(refs))
	}
	if mode == "observe" && len(refs) == 0 {
		return errors.New("nonempty origin advertised no refs")
	}
	if mode == "observe" || mode == "observe-empty" || mode == "cancel" || mode == "timeout" || mode == "refuse" {
		if _, e := os.Stat(repo); !os.IsNotExist(e) {
			return errors.New("observation created a P repository")
		}
	}
	associated, e := store.HasActiveProject(context.Background(), project)
	if e != nil {
		return e
	}
	if associated {
		return errors.New("substrate contact created project association")
	}
	if mode == "fetch" || mode == "race" || mode == "alternate" {
		// A selected fetch may add objects, but never creates refs, tracking refs,
		// or an origin remote in the bare P repository.
		cmd := exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname)")
		if out, e := cmd.Output(); e != nil || len(out) != 0 {
			return fmt.Errorf("fetch wrote P refs: %q %v", out, e)
		}
		config, e := os.ReadFile(filepath.Join(repo, "config"))
		if e != nil {
			return e
		}
		if len(config) == 0 || !bytes.Equal(config, configBefore) {
			return errors.New("fetch changed P repository config")
		}
		if _, e := os.Stat(filepath.Join(repo, "FETCH_HEAD")); !errors.Is(e, os.ErrNotExist) {
			return fmt.Errorf("fetch wrote P FETCH_HEAD: %v", e)
		}
		head, e := os.ReadFile(filepath.Join(repo, "HEAD"))
		if e != nil || string(head) != "ref: refs/heads/main\n" {
			return errors.New("fetch changed P HEAD")
		}
		cmd = exec.Command("git", "-C", repo, "remote")
		if out, e := cmd.Output(); e != nil || len(out) != 0 {
			return fmt.Errorf("fetch configured remote: %q %v", out, e)
		}
	}
	result := struct {
		Refs   []plugin.GitOriginRef `json:"refs,omitempty"`
		Commit string                `json:"commit,omitempty"`
		Status string                `json:"status"`
	}{refs, got, mode}
	return json.NewEncoder(os.Stdout).Encode(result)
}

// serve accepts only one fixture principal and fixed upload-pack paths. The
// publication variant also accepts receive-pack for full.git only. The origin
// client is the real host OpenSSH binary selected by production code.
func serve(a []string) error {
	return serveMode(a, false)
}

func servePublish(a []string) error {
	return serveMode(a, true)
}

func serveMode(a []string, publication bool) error {
	if (!publication && len(a) != 6) || (publication && len(a) != 7) {
		return errors.New("serve HOST-PRIVATE CLIENT-PUBLIC EMPTY-REPO FULL-REPO FLOOD-REPO TRACE [LOST-RESPONSE-MARKER for serve-publish]")
	}
	hostKey, err := os.ReadFile(a[0])
	if err != nil {
		return err
	}
	pub, err := os.ReadFile(a[1])
	if err != nil {
		return err
	}
	client, _, _, _, err := gossh.ParseAuthorizedKey(pub)
	if err != nil {
		return err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	server, err := wish.NewServer(
		wish.WithHostKeyPEM(hostKey),
		wish.WithPublicKeyAuth(func(auth ssh.Context, key ssh.PublicKey) bool {
			return auth.User() == "pdev" && key != nil && bytes.Equal(key.Marshal(), client.Marshal())
		}),
		wish.WithPasswordAuth(func(ssh.Context, string) bool { return false }),
		wish.WithMaxTimeout(10*time.Second),
		wish.WithMiddleware(func(next ssh.Handler) ssh.Handler {
			return func(s ssh.Session) {
				raw := s.RawCommand()
				service := "upload-pack"
				if publication && strings.HasPrefix(raw, "git-receive-pack '") {
					service = "receive-pack"
				}
				prefix := "git-" + service + " '"
				if !strings.HasPrefix(raw, prefix) || !strings.HasSuffix(raw, "'") {
					_ = s.Exit(1)
					return
				}
				name := strings.TrimSuffix(strings.TrimPrefix(raw, prefix), "'")
				if publication && service == "receive-pack" && name != "/full.git" && name != "full.git" {
					_ = s.Exit(1)
					return
				}
				trace, traceErr := os.OpenFile(a[5], os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
				if traceErr != nil {
					_ = s.Exit(1)
					return
				}
				if publication {
					_, traceErr = fmt.Fprintln(trace, service, name)
				} else {
					_, traceErr = fmt.Fprintln(trace, name)
				}
				_ = trace.Close()
				if traceErr != nil {
					_ = s.Exit(1)
					return
				}
				var repo string
				switch name {
				case "/empty.git", "empty.git":
					repo = a[2]
				case "/full.git", "full.git":
					repo = a[3]
				case "/flood.git", "flood.git":
					repo = a[4]
				case "/hang.git", "hang.git":
					select {
					case <-s.Context().Done():
					case <-time.After(9 * time.Second):
					}
					_ = s.Exit(1)
					return
				default:
					_ = s.Exit(1)
					return
				}
				cmd := exec.CommandContext(s.Context(), gitPath, service, repo)
				cmd.Stdin = s
				cmd.Stdout = s
				cmd.Stderr = s.Stderr()
				if err := cmd.Run(); err != nil {
					_ = s.Exit(1)
					return
				}
				if publication && service == "receive-pack" {
					if _, err := os.Stat(a[6]); err == nil {
						_ = s.Exit(1)
						return
					}
				}
				_ = s.Exit(0)
			}
		}),
	)
	if err != nil {
		return err
	}
	fmt.Printf("{\"address\":%q}\n", listener.Addr().String())
	return server.Serve(listener)
}
