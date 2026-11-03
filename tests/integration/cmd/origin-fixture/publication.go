package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/tests/integration/internal/bundledgit"
)

// publish runs the bundled source driver's publication operations only. It
// does not simulate a lifecycle assignment, confirmation, core's answers or
// a public host RPC: a relation core answers itself is pushed by no one.
// Usage: publish MODE STATE PROJECT URL BRANCH COMMIT DESTINATION RELATION
//
//	[STATUS [RACE-REPO RACE-OID]]
//
// MODE run observes the origin, compares, and publishes an absent or
// fast-forward relation, expecting RELATION and STATUS ("none" when nothing
// is pushed). The refuse modes expect the driver to refuse the publication
// before its gate: refuse-relation publishes RELATION as given,
// refuse-moved a COMMIT that BRANCH does not name, and refuse-gate fails the
// gate after comparing.
func publish(a []string) error {
	if len(a) < 8 {
		return errors.New("publish MODE STATE PROJECT URL BRANCH COMMIT DESTINATION RELATION [STATUS [RACE-REPO RACE-OID]]")
	}
	mode, state, project, url := a[0], a[1], a[2], a[3]
	branch, commit, destination, relation := a[4], sourcedriver.OID(a[5]), a[6], sourcedriver.Relation(a[7])
	switch {
	case mode == "run" && (len(a) == 9 || len(a) == 11):
	case (mode == "refuse-relation" || mode == "refuse-moved" || mode == "refuse-gate") && len(a) == 8:
	default:
		return fmt.Errorf("unknown publication mode %q or wrong arguments", mode)
	}
	ctx := context.Background()
	driver, err := bundledgit.Open(ctx, state)
	if err != nil {
		return err
	}
	// Core checks the destination with CheckNames and publishes to the
	// branch name it returns.
	checked, err := driver.CheckNames(ctx, []sourcedriver.Name{{Kind: sourcedriver.NameDestination, Value: destination}})
	if err != nil {
		return err
	}
	p := sourcedriver.Publication{URL: url, Branch: branch, Commit: commit, Destination: checked[0].Value, Relation: relation}
	gateCalls := 0
	gate := func() error { gateCalls++; return nil }
	switch mode {
	case "refuse-relation":
		if _, err := driver.Publish(ctx, project, p, gate); !errors.Is(err, sourcedriver.ErrInvalid) || gateCalls != 0 {
			return fmt.Errorf("publishing relation %q answered %v after %d gate calls, want ErrInvalid before the gate", relation, err, gateCalls)
		}
		return printPublication(mode, "", "", "none")
	case "refuse-moved":
		if _, err := driver.Publish(ctx, project, p, gate); !errors.Is(err, sourcedriver.ErrConflict) || gateCalls != 0 {
			return fmt.Errorf("publishing a commit the branch does not name answered %v after %d gate calls, want ErrConflict before the gate", err, gateCalls)
		}
		return printPublication(mode, "", "", "none")
	}
	refs, err := driver.ObserveOrigin(ctx, url)
	if err != nil {
		return err
	}
	var observed *sourcedriver.OriginRef
	for i := range refs {
		if refs[i].Ref == destination {
			observed = &refs[i]
		}
	}
	compared, err := driver.ComparePublication(ctx, project, url, commit, observed)
	if err != nil {
		return err
	}
	if compared != relation {
		return fmt.Errorf("compared %q, want %q", compared, relation)
	}
	destinationOID := ""
	if observed != nil {
		destinationOID = string(observed.OID)
	}
	if mode == "refuse-gate" {
		refused := errors.New("the fixture's gate refuses")
		if _, err := driver.Publish(ctx, project, p, func() error { return refused }); !errors.Is(err, refused) {
			return fmt.Errorf("publishing past a failing gate answered %v, want the gate's error", err)
		}
		return printPublication(mode, compared, destinationOID, "none")
	}
	status := "none"
	if relation == sourcedriver.RelationAbsent || relation == sourcedriver.RelationFastForward {
		if len(a) == 11 {
			cmd := exec.Command("git", "-C", a[9], "update-ref", destination, a[10])
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("race origin destination: %w: %s", err, output)
			}
		}
		published, err := driver.Publish(ctx, project, p, gate)
		switch {
		case errors.Is(err, sourcedriver.ErrUnknownOutcome):
			status = "outcome_unknown"
		case err != nil:
			return err
		default:
			status = string(published)
		}
		if gateCalls != 1 {
			return fmt.Errorf("the publication called its gate %d times, want once", gateCalls)
		}
	}
	if status != a[8] {
		return fmt.Errorf("publication status %q, want %q", status, a[8])
	}
	return printPublication(mode, compared, destinationOID, status)
}

func printPublication(mode string, relation sourcedriver.Relation, destinationOID, status string) error {
	return json.NewEncoder(os.Stdout).Encode(struct {
		Mode           string `json:"mode"`
		Relation       string `json:"relation,omitempty"`
		DestinationOID string `json:"destination_oid,omitempty"`
		Status         string `json:"status"`
	}{mode, string(relation), destinationOID, status})
}

// preparePublish creates only the local bare P repository, with the bundled
// source driver. The caller seeds its ordinary source head with normal Git
// and owns all scenario assertions.
func preparePublish(a []string) error {
	if len(a) != 2 {
		return errors.New("prepare-publish STATE PROJECT")
	}
	ctx := context.Background()
	driver, err := bundledgit.Open(ctx, a[0])
	if err != nil {
		return err
	}
	if _, err := driver.CreateRepository(ctx, a[1]); err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, bundledgit.RepositoryPath(a[0], a[1]))
	return err
}
