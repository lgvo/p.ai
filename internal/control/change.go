package control

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// ChangeTTL is how long a change tag lasts.
const ChangeTTL = 2 * time.Minute

// changeHashFormat names the recipe of the change hash.
const changeHashFormat = "p.plan/v1"

// Change is one risky change of one call (api.ChangeRequest): what it acts on,
// how it is planned, and how its operation is recorded. Each kind of change
// supplies these; Do answers the dry run and the run the same way for all of
// them, and stores nothing between the two calls.
type Change[D any] struct {
	// Action is the method name, and Target what the change acts on, in
	// target form.
	Action, Target string
	// Instance is the daemon's instance UUID.
	Instance string
	// Inputs are the caller's choices besides the target; nil when there are
	// none.
	Inputs any
	// Hold runs fn while holding what the change acts on.
	Hold func(ctx context.Context, fn func(context.Context) error) error
	// Plan reads the state and plans the change into p. A conflict refusal
	// (Conflict) ends planning: it becomes p's refusal, and p keeps what was
	// planned before it. Any other error answers the call.
	Plan func(ctx context.Context, p *api.Plan[D]) error
	// Shown, when set, returns details without the facts they show for
	// information only, which the change hash leaves out: a run does not
	// compare them. It must not change what details shares, such as a slice.
	Shown func(details D) D
	// Replay returns the operation that the run's key recorded for this same
	// run, found false when the key is unused; it answers KeyReused for a key
	// recorded for another request.
	Replay func(context.Context) (op api.Operation, found bool, err error)
	// Begin records the operation of a run whose change tag matched.
	Begin func(context.Context, Run[D]) (api.Operation, error)
}

// Run is a run of a change that its change tag admitted.
type Run[D any] struct {
	// Plan is the plan that gave the tag again.
	Plan api.Plan[D]
	// Hash is the change hash, the exact change that runs: the operation
	// records it. Begin checks the run's tag's expiry again as it records
	// the operation.
	Hash string
	// Recheck plans the change again and refuses with precondition_failed,
	// reason changed, unless that gives the same change hash.
	Recheck func(context.Context) error
}

// Do answers a call of change c: a dry run returns c's plan, and its change
// tag when the plan is allowed; a run records c's operation if planning c
// again gives the tag in req.IfMatch.
func (c Change[D]) Do(ctx context.Context, req api.ChangeRequest) (api.ChangeResult[D], error) {
	if req.DryRun() {
		return c.dryRun(ctx)
	}
	if err := req.Validate(); err != nil {
		return api.ChangeResult[D]{}, err
	}
	op, err := c.run(ctx, req)
	if err != nil {
		return api.ChangeResult[D]{}, err
	}
	return api.ChangeResult[D]{Operation: &op}, nil
}

func (c Change[D]) dryRun(ctx context.Context) (api.ChangeResult[D], error) {
	var result api.ChangeResult[D]
	err := c.Hold(ctx, func(ctx context.Context) error {
		p, err := c.plan(ctx)
		if err != nil {
			return err
		}
		result.Plan = &p
		if !p.Allowed {
			return nil
		}
		hash, err := c.hash(p)
		if err != nil {
			return err
		}
		result.ChangeTag = changeTag(time.Now().Add(ChangeTTL), hash)
		return nil
	})
	if err != nil {
		return api.ChangeResult[D]{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return api.ChangeResult[D]{}, Internal(api.ReasonEncoding, err)
	}
	if len(encoded) > api.MaxFrameBytes-1024 {
		return api.ChangeResult[D]{}, Conflict(api.ReasonBoundExceeded, c.Target)
	}
	return result, nil
}

func (c Change[D]) run(ctx context.Context, req api.ChangeRequest) (api.Operation, error) {
	expiry, ok := api.ParseChangeTag(req.IfMatch)
	if !ok {
		return api.Operation{}, Invalid("if_match")
	}
	if op, found, err := c.Replay(ctx); err != nil || found {
		return op, err
	}
	if !time.Now().Before(expiry) {
		return api.Operation{}, Stale(api.ReasonExpired)
	}
	var op api.Operation
	err := c.Hold(ctx, func(ctx context.Context) error {
		// An identical run may have recorded the operation meanwhile.
		prior, found, err := c.Replay(ctx)
		if err != nil || found {
			op = prior
			return err
		}
		p, hash, err := c.replan(ctx)
		if err != nil {
			return err
		}
		if changeTag(expiry, hash) != req.IfMatch {
			return Stale(api.ReasonChanged)
		}
		if !time.Now().Before(expiry) {
			// It expired while the change was planned again.
			return Stale(api.ReasonExpired)
		}
		op, err = c.Begin(ctx, Run[D]{Plan: p, Hash: hash, Recheck: func(ctx context.Context) error {
			_, again, err := c.replan(ctx)
			if err == nil && again != hash {
				err = Stale(api.ReasonChanged)
			}
			return err
		}})
		return err
	})
	return op, err
}

// replan plans c for a run, and refuses one that is no longer allowed: its
// tag came from an allowed plan, so the state changed.
func (c Change[D]) replan(ctx context.Context) (api.Plan[D], string, error) {
	p, err := c.plan(ctx)
	if err != nil {
		return p, "", err
	}
	if !p.Allowed {
		return p, "", Stale(api.ReasonChanged)
	}
	hash, err := c.hash(p)
	return p, hash, err
}

// plan plans c into a plan with its envelope: the action and target, empty
// lists, the refusal a conflict ends planning with, and Allowed from them.
func (c Change[D]) plan(ctx context.Context) (api.Plan[D], error) {
	p := api.Plan[D]{Action: c.Action, Target: c.Target}
	err := c.Plan(ctx, &p)
	var refusal *Refusal
	if errors.As(err, &refusal) && refusal.Kind == api.KindConflict {
		if refusal.Cause != nil && refusal.Cause != ErrConflict {
			log.Printf("%s of %s refused %s: %v", c.Action, c.Target, refusal.Reason, refusal.Cause)
		}
		p.Refusals = append(p.Refusals, api.Refusal{Code: refusal.Reason, Target: refusal.Target})
		err = nil
	}
	if err != nil {
		return api.Plan[D]{}, err
	}
	for i := range p.Refusals {
		if p.Refusals[i].Target == c.Target {
			p.Refusals[i].Target = ""
		}
	}
	if p.Refusals == nil {
		p.Refusals = []api.Refusal{}
	}
	if p.Loss == nil {
		p.Loss = []api.Loss{}
	}
	if p.Gone == nil {
		p.Gone = []api.Loss{}
	}
	p.Allowed = len(p.Refusals) == 0
	return p, nil
}

// changeFacts is what the change hash covers: the change (its action, target
// and the caller's inputs) and its plan, which holds every fact the change
// was planned from and every effect planned, less what the plan shows for
// information only (Change.Shown).
type changeFacts struct {
	Format   string `json:"format"`
	Instance string `json:"instance"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Inputs   any    `json:"inputs"`
	Plan     any    `json:"plan"`
}

// hash is the change hash of plan p: the lowercase hex SHA-256 of
// changeFacts as encoding/json encodes them, which keeps struct field order
// and sorts map keys, so the same plan always gives the same hash.
func (c Change[D]) hash(p api.Plan[D]) (string, error) {
	if c.Shown != nil {
		p.Details = c.Shown(p.Details)
	}
	raw, err := json.Marshal(changeFacts{Format: changeHashFormat, Instance: c.Instance, Action: c.Action, Target: c.Target, Inputs: c.Inputs, Plan: p})
	if err != nil {
		return "", Internal(api.ReasonEncoding, err)
	}
	return digest(raw), nil
}

// changeTag is the change tag of change hash hash that expires at expiry:
// the expiry, to the second, then the SHA-256 of the expiry and the hash. The
// daemon never returns the hash, so editing the expiry breaks the tag.
func changeTag(expiry time.Time, hash string) string {
	e := expiry.UTC().Format(api.ChangeTagTimeLayout)
	return e + "-" + digest([]byte(e+"-"+hash))
}
