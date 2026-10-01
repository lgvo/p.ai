package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
)

// publish composes the production substrate only. It does not simulate a
// lifecycle assignment, confirmation, or a public host RPC.
// Usage: publish MODE STATE ACTIVATION PROJECT URL SOURCE-REF SOURCE-OID
//
//	DESTINATION EXPECT-RELATION EXPECT-STATUS [RACE-REPO RACE-OID]
func publish(a []string) error {
	if len(a) != 10 && len(a) != 12 {
		return errors.New("publish MODE STATE ACTIVATION PROJECT URL SOURCE-REF SOURCE-OID DESTINATION EXPECT-RELATION EXPECT-STATUS [RACE-REPO RACE-OID]")
	}
	mode, state, activation, project, url := a[0], a[1], a[2], a[3], a[4]
	sourceRef, sourceOID, destination := a[5], a[6], a[7]
	if mode != "run" && mode != "wrong" && mode != "expired" && mode != "no-observe" {
		return fmt.Errorf("unknown publication mode %q", mode)
	}
	if len(a) == 12 && mode != "run" {
		return errors.New("race is only valid in run mode")
	}
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
	ctx := context.Background()
	var preview gitservice.OriginPublicationPreview
	var result gitservice.OriginPublicationResult
	var expired *gitservice.OriginScope
	err = backend.WithOrigin(ctx, project, func(scope *gitservice.OriginScope) error {
		if mode == "no-observe" {
			if _, err := scope.PreviewPublication(ctx, sourceRef, sourceOID, destination); !errors.Is(err, control.ErrInvalid) {
				return fmt.Errorf("preview without observation: %v", err)
			}
			return nil
		}
		refs, err := scope.Observe(ctx, url)
		if err != nil {
			return err
		}
		_ = refs // Observe must complete even when the destination is absent.
		preview, err = scope.PreviewPublication(ctx, sourceRef, sourceOID, destination)
		if err != nil {
			return err
		}
		if preview.Project != project || preview.URL != url || preview.SourceRef != sourceRef || preview.SourceOID != sourceOID || preview.DestinationRef != destination || preview.Relation != a[8] {
			return fmt.Errorf("unexpected publication preview: %+v", preview)
		}
		if mode == "expired" {
			expired = scope
			return nil
		}
		if mode == "wrong" {
			wrong := preview
			wrong.DestinationRef = "refs/heads/fixture-wrong"
			if _, err := scope.PublishPublication(ctx, wrong); !errors.Is(err, control.ErrInvalid) {
				return fmt.Errorf("mismatched preview accepted: %v", err)
			}
			return nil
		}
		if len(a) == 12 {
			cmd := exec.Command("git", "-C", a[10], "update-ref", destination, a[11])
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("race origin destination: %w: %s", err, output)
			}
		}
		result, err = scope.PublishPublication(ctx, preview)
		if err != nil {
			return err
		}
		if result.Preview != preview || result.Status != a[9] {
			return fmt.Errorf("unexpected publication result: %+v", result)
		}
		if _, err := scope.PublishPublication(ctx, preview); !errors.Is(err, control.ErrInvalid) {
			return fmt.Errorf("used preview accepted again: %v", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if mode == "expired" {
		if _, err := expired.PublishPublication(ctx, preview); !errors.Is(err, control.ErrInvalid) {
			return fmt.Errorf("expired preview accepted: %v", err)
		}
		if err := backend.WithOrigin(ctx, project, func(scope *gitservice.OriginScope) error {
			if _, err := scope.Observe(ctx, url); err != nil {
				return err
			}
			if _, err := scope.PublishPublication(ctx, preview); !errors.Is(err, control.ErrInvalid) {
				return fmt.Errorf("foreign scope accepted preview: %v", err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Mode    string                              `json:"mode"`
		Preview gitservice.OriginPublicationPreview `json:"preview"`
		Result  gitservice.OriginPublicationResult  `json:"result"`
	}{mode, preview, result})
}

// preparePublish creates only the local bare P repository. The caller seeds
// its ordinary source head with normal Git and owns all scenario assertions.
func preparePublish(a []string) error {
	if len(a) != 3 {
		return errors.New("prepare-publish STATE ACTIVATION PROJECT")
	}
	store, err := control.OpenStore(a[0])
	if err != nil {
		return err
	}
	defer store.Close()
	active, err := plugin.LoadActivation(a[1])
	if err != nil {
		return err
	}
	backend, err := gitservice.New(store, a[0], active)
	if err != nil {
		return err
	}
	if err := backend.InitBare(context.Background(), a[2]); err != nil {
		return err
	}
	repo, err := backend.RepositoryPath(a[2])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, repo)
	return err
}
