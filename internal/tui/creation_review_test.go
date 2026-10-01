package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestCreationReviewEnterUsesPinnedRequestOnceAndBackIgnoresReply(t *testing.T) {
	for _, method := range []string{"project.create", "session.create"} {
		for _, failed := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/success", true: "/error"}[failed], func(t *testing.T) {
				f := &fakeClient{fn: func(got string, p params) (any, error) {
					if got != method || p["project"] != "reviewed" || p["key"] != "pinned" || method == "project.create" && p["url"] != "ssh://reviewed" || method == "session.create" && (p["branch"] != "topic" || p["expected_commit_oid"] != "tip" || p["expected_origin_url"] != "ssh://reviewed") {
						t.Fatalf("creation request changed: %s %+v", got, p)
					}
					if failed {
						return nil, errors.New("late failure")
					}
					return map[string]any{"operation": map[string]string{"id": "created", "status": "running"}}, nil
				}}
				m := fixture()
				m.client = f
				m.creation = params{"v": 1, "project": "reviewed", "key": "pinned"}
				if method == "project.create" {
					m.creation["url"] = "ssh://reviewed"
				} else {
					m.creation["choice"] = "new"
					m.creation["branch"] = "topic"
					m.creation["origin_ref"] = "refs/heads/main"
					m.creation["expected_commit_oid"] = "tip"
					m.creation["expected_origin_url"] = "ssh://reviewed"
				}
				m.confirmCreation(method)
				m.creation["project"] = "changed after review"
				for _, key := range []string{"y", "n"} {
					next, cmd := press(m, key)
					m = next
					if cmd != nil || m.page != "creation-review" {
						t.Fatal("creation review retained a confirmation control")
					}
				}
				m, submit := press(m, "enter")
				if submit == nil || !m.working {
					t.Fatal("Enter did not submit creation")
				}
				m, repeat := press(m, "enter")
				if repeat != nil {
					t.Fatal("repeated Enter submitted a second request")
				}
				late := submit()
				m, _ = press(m, "esc")
				updated, cmd := m.Update(late)
				m = updated.(Model)
				if cmd != nil || m.page != "form" || m.working || m.notice != "" || m.attachOnComplete || m.operationID != "" || len(f.calls) != 1 {
					t.Fatalf("canceled creation reply reopened work: %+v", m)
				}
			})
		}
	}
}

func TestCreationReviewBackPreservesEditableInputs(t *testing.T) {
	for _, kind := range []string{"project", "new", "existing"} {
		m := fixture()
		m.creation = params{"project": "app", "key": "key", "choice": kind, "branch": "topic", "url": "ssh://example/repo"}
		method := "session.create"
		if kind == "project" {
			method = "project.create"
		}
		m.confirmCreation(method)
		m, cmd := press(m, "q")
		if kind == "existing" {
			if m.page != "create" || m.form != "branch" || cmd == nil {
				t.Fatal("retained branch Back did not refresh branch choices")
			}
		} else if cmd != nil || m.page != "form" || kind == "new" && (m.form != "branch-name" || m.input != "topic") || kind == "project" && (m.form != "project-url" || m.input != "ssh://example/repo") {
			t.Fatalf("Back lost input: %s %s %q", m.page, m.form, m.input)
		}
	}
}

func TestCreationReviewScrollableWithoutConfirmationFraming(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}, {48, 16}} {
		m := fixture()
		m.width, m.height = size[0], size[1]
		m.creation = params{"project": "app", "branch": "topic", "key": "key"}
		m.sourceReview = strings.Repeat("Long source description. ", 80) + "SOURCE_END"
		m.confirmCreation("session.create")
		m, _ = press(m, "G")
		frame := ansi.Strip(m.View().Content)
		if !strings.Contains(frame, "SOURCE_END") || !strings.Contains(frame, "Enter create, boot and enter") || strings.Contains(frame, "[y/N]") || strings.Contains(frame, "decline") || strings.Count(frame, "\n") >= m.height {
			t.Fatalf("creation controls or source unreadable at %v:\n%s", size, frame)
		}
		m, _ = press(m, "g")
		m, _ = press(m, "g")
		if m.reviewOffset != 0 {
			t.Fatal("gg did not return to review start")
		}
	}
}
