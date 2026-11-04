package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func testCommitImage(project, commit string) CommitImage {
	base, build := strings.Repeat("c", 64), "550e8400-e29b-41d4-a716-446655440000"
	return CommitImage{
		Key: CommitImageKey{Project: project, Commit: commit, BaseImage: base, System: "x86_64-linux", BuilderVersion: "incus-commit-image/1"},
		Ref: strings.Repeat("e", 64), Build: build,
		Labels: map[string]string{
			"project": project, "source_commit": commit, "base_image": base,
			"system": "x86_64-linux", "builder_version": "incus-commit-image/1", "build": build,
		},
		Selection: "devshell", MaterialSHA256: strings.Repeat("d", 64), Size: 1024,
	}
}

func TestCommitImageIndexIsScopedToProjectAndStartsReady(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for _, project := range []string{"team/a", "team/b"} {
		if err := s.CreateProject(ctx, project, json.RawMessage(`{"network":"none"}`)); err != nil {
			t.Fatal(err)
		}
	}
	image := testCommitImage("team/a", strings.Repeat("a", 40))
	if err := s.PutCommitImage(ctx, image); err != nil {
		t.Fatal(err)
	}
	other := image.Key
	other.Project = "team/b"
	if _, found, err := s.GetCommitImage(ctx, other); err != nil || found {
		t.Fatalf("another project's commit found this image: found=%v err=%v", found, err)
	}
	got, found, err := s.GetCommitImage(ctx, image.Key)
	if err != nil || !found {
		t.Fatalf("indexed image not found: %v %v", found, err)
	}
	if got.State != "ready" || got.CreatedAt == "" {
		t.Fatalf("new row is not ready: %+v", got)
	}
	got.State, got.CreatedAt = "", ""
	if !reflect.DeepEqual(got, image) {
		t.Fatalf("row changed the image:\n got %+v\nwant %+v", got, image)
	}
}

func TestPutCommitImageRefusesAnIndexedKeyOrReference(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	first := testCommitImage("app", strings.Repeat("a", 40))
	if err := s.PutCommitImage(ctx, first); err != nil {
		t.Fatal(err)
	}
	sameKey := first
	sameKey.Ref = strings.Repeat("f", 64)
	if err := s.PutCommitImage(ctx, sameKey); !errors.Is(err, ErrConflict) {
		t.Fatalf("second image for one key: %v", err)
	}
	sameRef := testCommitImage("app", strings.Repeat("b", 40))
	if err := s.PutCommitImage(ctx, sameRef); !errors.Is(err, ErrConflict) {
		t.Fatalf("one image indexed for two commits: %v", err)
	}
	got, _, _ := s.GetCommitImage(ctx, first.Key)
	if got.Ref != first.Ref {
		t.Fatalf("refused put changed the row: %+v", got)
	}
}

func TestPutCommitImageRefusesMalformedRows(t *testing.T) {
	cases := map[string]func(*CommitImage){
		"project":              func(c *CommitImage) { c.Key.Project = "/abs" },
		"long commit":          func(c *CommitImage) { c.Key.Commit = strings.Repeat("a", 65); c.Labels["source_commit"] = c.Key.Commit },
		"unprintable commit":   func(c *CommitImage) { c.Key.Commit = "a\n"; c.Labels["source_commit"] = c.Key.Commit },
		"base":                 func(c *CommitImage) { c.Key.BaseImage = "base" },
		"system":               func(c *CommitImage) { c.Key.System = "riscv64-linux" },
		"empty builder":        func(c *CommitImage) { c.Key.BuilderVersion = "" },
		"long builder":         func(c *CommitImage) { c.Key.BuilderVersion = strings.Repeat("v", 129) },
		"unprintable builder":  func(c *CommitImage) { c.Key.BuilderVersion = "v1\n" },
		"ref":                  func(c *CommitImage) { c.Ref = strings.Repeat("E", 64) },
		"build":                func(c *CommitImage) { c.Build = "build"; c.Labels["build"] = "build" },
		"build label":          func(c *CommitImage) { c.Labels["build"] = "11111111-1111-4111-8111-111111111111" },
		"project label":        func(c *CommitImage) { c.Labels["project"] = "other" },
		"commit label":         func(c *CommitImage) { c.Labels["source_commit"] = strings.Repeat("b", 40) },
		"base label":           func(c *CommitImage) { c.Labels["base_image"] = strings.Repeat("b", 64) },
		"system label":         func(c *CommitImage) { c.Labels["system"] = "aarch64-linux" },
		"builder label":        func(c *CommitImage) { c.Labels["builder_version"] = "incus-commit-image/2" },
		"missing label":        func(c *CommitImage) { delete(c.Labels, "system") },
		"extra label":          func(c *CommitImage) { c.Labels["owner"] = "p" },
		"selection":            func(c *CommitImage) { c.Selection = "default" },
		"devshell no material": func(c *CommitImage) { c.MaterialSHA256 = "" },
		"base with material":   func(c *CommitImage) { c.Selection = "no-flake" },
		"negative size":        func(c *CommitImage) { c.Size = -1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := openTestStore(t)
			ctx := context.Background()
			if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			image := testCommitImage("app", strings.Repeat("a", 40))
			change(&image)
			if err := s.PutCommitImage(ctx, image); !errors.Is(err, ErrInvalid) {
				t.Fatalf("malformed row accepted: %v", err)
			}
		})
	}
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, selection := range []string{"no-flake", "no-default"} {
		image := testCommitImage("app", strings.Repeat("a", 40))
		image.Key.System = "aarch64-linux"
		image.Labels["system"] = "aarch64-linux"
		image.Selection, image.MaterialSHA256 = selection, ""
		image.Key.BuilderVersion = "v " + selection
		image.Labels["builder_version"] = image.Key.BuilderVersion
		image.Ref = strings.Repeat("1", 63) + map[string]string{"no-flake": "1", "no-default": "2"}[selection]
		if err := s.PutCommitImage(ctx, image); err != nil {
			t.Fatalf("%s image refused: %v", selection, err)
		}
	}
}

func TestMarkCommitImageReleasingTakesOnlyTheExactRow(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	image := testCommitImage("app", strings.Repeat("a", 40))
	if err := s.MarkCommitImageReleasing(ctx, image.Key, image.Ref); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing row marked releasing: %v", err)
	}
	if err := s.PutCommitImage(ctx, image); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCommitImageReleasing(ctx, image.Key, strings.Repeat("f", 64)); !errors.Is(err, ErrConflict) {
		t.Fatalf("another image's release marked this row: %v", err)
	}
	if got, _, _ := s.GetCommitImage(ctx, image.Key); got.State != "ready" {
		t.Fatalf("refused mark changed the row: %+v", got)
	}
	for range 2 {
		if err := s.MarkCommitImageReleasing(ctx, image.Key, image.Ref); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := s.GetCommitImage(ctx, image.Key); got.State != "releasing" || got.Ref != image.Ref {
			t.Fatalf("row not releasing: %+v", got)
		}
	}
}

func TestForgetCommitImageRemovesOnlyTheExactRow(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	image := testCommitImage("app", strings.Repeat("a", 40))
	if err := s.ForgetCommitImage(ctx, image.Key, image.Ref); err != nil {
		t.Fatalf("forgetting a missing row: %v", err)
	}
	if err := s.PutCommitImage(ctx, image); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetCommitImage(ctx, image.Key, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetCommitImage(ctx, image.Key); !found {
		t.Fatal("another image's forget removed this row")
	}
	if err := s.MarkCommitImageReleasing(ctx, image.Key, image.Ref); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetCommitImage(ctx, image.Key, image.Ref); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetCommitImage(ctx, image.Key); err != nil || found {
		t.Fatalf("forgotten row remains: found=%v err=%v", found, err)
	}
}

func TestListCommitImagesPagesOneProjectInKeyOrder(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for _, project := range []string{"app", "other"} {
		if err := s.CreateProject(ctx, project, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	put := func(project string, i int) CommitImage {
		image := testCommitImage(project, fmt.Sprintf("%040x", i+1))
		image.Ref = fmt.Sprintf("%064x", i+1)
		if err := s.PutCommitImage(ctx, image); err != nil {
			t.Fatal(err)
		}
		got, _, _ := s.GetCommitImage(ctx, image.Key)
		return got
	}
	put("other", 100)
	var want []CommitImage
	for i := 24; i >= 0; i-- {
		want = append([]CommitImage{put("app", i)}, want...)
	}
	var got []CommitImage
	var after CommitImageKey
	for _, size := range []int{10, 10, 5} {
		page, more, err := s.ListCommitImages(ctx, "app", after, 10)
		if err != nil || len(page) != size || more != (size == 10) {
			t.Fatalf("page of %d: %d rows, more=%v, %v", size, len(page), more, err)
		}
		got = append(got, page...)
		after = page[len(page)-1].Key
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pages:\n got %+v\nwant %+v", got, want)
	}
	if _, _, err := s.ListCommitImages(ctx, "app", CommitImageKey{}, 101); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded page: %v", err)
	}
}

// commitSessions creates one session per branch of project, each started from
// the commit its branch maps to, and returns their UUIDs by branch.
func commitSessions(t *testing.T, s *Store, project string, commits map[string]string) map[string]string {
	t.Helper()
	ctx := context.Background()
	uuids := map[string]string{}
	for branch, commit := range commits {
		req := ReserveSessionRequest{Key: project + "/" + branch, Project: project, Branch: branch, Choice: "existing"}
		_, session, err := s.BeginSessionCreate(ctx, req, strings.Repeat("c", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
			return commit, true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		uuids[branch] = session.UUID
	}
	return uuids
}

func TestCommitUseCountsEverySessionStartedFromTheCommit(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for _, project := range []string{"app", "other"} {
		if err := s.CreateProject(ctx, project, json.RawMessage(`{"network":"none"}`)); err != nil {
			t.Fatal(err)
		}
	}
	c, d := strings.Repeat("c", 40), strings.Repeat("d", 40)
	app := commitSessions(t, s, "app", map[string]string{"creating": c, "established": c, "removing": c, "gone": c, "later": d})
	commitSessions(t, s, "other", map[string]string{"main": c})
	if sessions, creating, err := s.CommitUse(ctx, "app", c); err != nil || sessions != 4 || creating != 4 {
		t.Fatalf("creating sessions: %d %d %v", sessions, creating, err)
	}
	for _, uuid := range []string{app["established"], app["removing"]} {
		if err := s.AdvanceSessionRegistry(ctx, uuid, "creating", "established"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AdvanceSessionRegistry(ctx, app["removing"], "established", "removing"); err != nil {
		t.Fatal(err)
	}
	// A session is gone once its row is: removal deletes it last.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE uuid=?`, app["gone"]); err != nil {
		t.Fatal(err)
	}
	if sessions, creating, err := s.CommitUse(ctx, "app", c); err != nil || sessions != 3 || creating != 1 {
		t.Fatalf("sessions of the commit: %d %d %v", sessions, creating, err)
	}
	if sessions, creating, err := s.CommitUse(ctx, "app", strings.Repeat("e", 40)); err != nil || sessions != 0 || creating != 0 {
		t.Fatalf("unused commit: %d %d %v", sessions, creating, err)
	}
}

func TestCommitSessionsDigestBindsOnlyTheCommitsSessions(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	for _, project := range []string{"app", "other"} {
		if err := s.CreateProject(ctx, project, json.RawMessage(`{"network":"none"}`)); err != nil {
			t.Fatal(err)
		}
	}
	c, d := strings.Repeat("c", 40), strings.Repeat("d", 40)
	app := commitSessions(t, s, "app", map[string]string{"work": c, "later": d})
	other := commitSessions(t, s, "other", map[string]string{"main": c})
	reviewed, err := s.CommitSessionsDigest(ctx, "app", c)
	if err != nil || !validFingerprint(reviewed) {
		t.Fatalf("digest: %q %v", reviewed, err)
	}
	for _, uuid := range []string{app["later"], other["main"]} {
		if err = s.AdvanceSessionRegistry(ctx, uuid, "creating", "established"); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.CommitSessionsDigest(ctx, "app", c); err != nil || got != reviewed {
		t.Fatalf("unrelated sessions changed the digest: %v", err)
	}
	if err = s.AdvanceSessionRegistry(ctx, app["work"], "creating", "established"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.CommitSessionsDigest(ctx, "app", c); err != nil || got == reviewed {
		t.Fatalf("a session of the commit changed without changing the digest: %v", err)
	}
}

func TestMarkCommitImageReleasingRefusesACommitInUse(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "app", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	c := strings.Repeat("c", 40)
	commitSessions(t, s, "app", map[string]string{"work": c})
	used := testCommitImage("app", c)
	unused := testCommitImage("app", strings.Repeat("d", 40))
	unused.Ref = strings.Repeat("f", 64)
	for _, image := range []CommitImage{used, unused} {
		if err := s.PutCommitImage(ctx, image); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkCommitImageReleasing(ctx, used.Key, used.Ref); !errors.Is(err, ErrConflict) {
		t.Fatalf("image of a commit in use marked releasing: %v", err)
	}
	if got, _, _ := s.GetCommitImage(ctx, used.Key); got.State != "ready" {
		t.Fatalf("refused mark changed the row: %+v", got)
	}
	if err := s.MarkCommitImageReleasing(ctx, unused.Key, unused.Ref); err != nil {
		t.Fatal(err)
	}
}
