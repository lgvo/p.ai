package main

import (
	"context"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/plugin"
)

const (
	originURL  = "ssh://git.example.com/team/app.git"
	observedAt = "2026-11-03T09:00:00Z"
	mainCommit = "3333333333333333333333333333333333333333"
)

// servedOrigin serves an origin whose refresh answers status from *status,
// fresh, unknown after a failed contact, or local-only, with its cached
// refs over two pages.
func servedOrigin(d *fakeDaemon, status *string) {
	refs := []plugin.GitOriginRef{{Ref: "refs/heads/main", OID: mainCommit, CommitOID: mainCommit}, {Ref: "refs/tags/v1", OID: tip, CommitOID: otherTip}}
	state := func() api.OriginState {
		if *status == "local-only" {
			return api.OriginState{Project: "app", Status: *status}
		}
		return api.OriginState{Project: "app", URL: originURL, Status: *status, ObservedAt: observedAt, RefCount: len(refs)}
	}
	handle(d, api.OriginRefresh, func(api.OriginRefreshRequest) (api.OriginResult, error) {
		return api.OriginResult{Origin: state()}, nil
	})
	handle(d, api.OriginInspect, func(api.OriginInspectRequest) (api.OriginResult, error) {
		return api.OriginResult{Origin: state()}, nil
	})
	handle(d, api.OriginSources, func(r api.OriginSourcesRequest) (api.OriginSourcesResult, error) {
		page := api.OriginSourcesResult{Project: "app", OriginURL: originURL, Status: *status, ObservationStatus: "fresh", ObservedAt: observedAt, Refs: refs[:1], Next: refs[0].Ref}
		if r.After != "" {
			page.Refs, page.Next = refs[1:], ""
		}
		return page, nil
	})
}

// p origin refs lists refs only from a fresh observation: after a refresh
// that failed, with cached refs present, it prints no refs, says when the
// origin was last reached and exits 1. A local-only project exits 3.
func TestOriginRefsComeOnlyFromAFreshObservation(t *testing.T) {
	d := newFakeDaemon(t)
	status := "fresh"
	servedOrigin(d, &status)
	run := runCLI(context.Background(), d, "origin", "refs", "app")
	if run.code != 0 {
		t.Fatalf("fresh: exit %d: %s", run.code, run.stderr)
	}
	contains(t, run.stdout, "refs/heads/main")
	contains(t, run.stdout, "refs/tags/v1")
	status = "unknown"
	run = runCLI(context.Background(), d, "origin", "refs", "app")
	if run.code != 1 || run.stdout != "" {
		t.Fatalf("stale: exit %d, printed %q", run.code, run.stdout)
	}
	contains(t, run.stderr, "last reached at "+observedAt)
	status = "local-only"
	if run := runCLI(context.Background(), d, "origin", "refs", "app"); run.code != 3 || run.stdout != "" {
		t.Fatalf("local-only: exit %d, printed %q", run.code, run.stdout)
	}
}

// The refresh and every page of origin.sources must say fresh: a refresh
// that failed withholds refs even when another refresh made the pages fresh
// again, and a page no longer fresh withholds every ref.
func TestOriginRefsNeedTheRefreshAndEveryPageFresh(t *testing.T) {
	for _, test := range []struct{ refresh, lastPage string }{{"unknown", "fresh"}, {"fresh", "unknown"}} {
		d := newFakeDaemon(t)
		handle(d, api.OriginRefresh, func(api.OriginRefreshRequest) (api.OriginResult, error) {
			return api.OriginResult{Origin: api.OriginState{Project: "app", URL: originURL, Status: test.refresh, ObservedAt: observedAt}}, nil
		})
		handle(d, api.OriginSources, func(r api.OriginSourcesRequest) (api.OriginSourcesResult, error) {
			page := api.OriginSourcesResult{Project: "app", OriginURL: originURL, Status: "fresh", ObservedAt: observedAt,
				Refs: []plugin.GitOriginRef{{Ref: "refs/heads/main", OID: mainCommit, CommitOID: mainCommit}}, Next: "refs/heads/main"}
			if r.After != "" {
				page.Status, page.Next = test.lastPage, ""
			}
			return page, nil
		})
		if run := runCLI(context.Background(), d, "origin", "refs", "app", "--json"); run.code != 1 || run.stdout != "" {
			t.Errorf("%+v: exit %d, printed %q", test, run.code, run.stdout)
		}
	}
}

// --from-origin creates from the commit and URL of a fresh observation and
// says which commit it chose; a stale observation creates nothing.
func TestSessionCreateFromOriginTakesAFreshCommit(t *testing.T) {
	d, j := newFakeDaemon(t), &journal{}
	serveJournal(d, j)
	status := "fresh"
	servedOrigin(d, &status)
	handle(d, api.SessionCreate, func(r api.SessionCreateRequest) (api.OperationResult, error) {
		return api.OperationResult{Operation: j.record("session.create", r.Key, api.OperationIntent{Project: r.Project, Session: sessionID,
			Branch: r.Branch, Choice: r.Choice, OriginRef: r.OriginRef})}, nil
	})
	run := runCLI(context.Background(), d, "session", "create", "app:work", "--from-origin", "main", "--key", "k1")
	if want := `{"key":"k1","project":"app","branch":"work","choice":"new","origin_ref":"refs/heads/main","expected_commit_oid":"` + mainCommit +
		`","expected_origin_url":"` + originURL + `"}`; run.code != 0 || d.params(api.SessionCreate.Name)[0] != want {
		t.Fatalf("exit %d, sent %v: %s", run.code, d.params(api.SessionCreate.Name), run.stderr)
	}
	contains(t, run.stderr, mainCommit)
	contains(t, run.stdout, "Created session "+sessionID+" (app:work)")
	if run := runCLI(context.Background(), d, "session", "create", "app:tagged", "--from-origin", "refs/tags/v1"); run.code != 0 {
		t.Fatalf("tag: exit %d: %s", run.code, run.stderr)
	}
	contains(t, d.params(api.SessionCreate.Name)[1], `"origin_ref":"refs/tags/v1","expected_commit_oid":"`+otherTip+`"`)
	for _, test := range []struct{ status, ref string }{{"unknown", "main"}, {"fresh", "missing"}} {
		status = test.status
		if run := runCLI(context.Background(), d, "session", "create", "app:other", "--from-origin", test.ref); run.code != 1 {
			t.Errorf("%v: exit %d: %s", test, run.code, run.stderr)
		}
	}
	if n := d.count(api.SessionCreate.Name); n != 2 {
		t.Fatalf("%d creations sent", n)
	}
}
