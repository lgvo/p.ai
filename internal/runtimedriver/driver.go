// Package runtimedriver is the contract between P's core and the code that
// owns runtime machinery. Core names P's concepts; a driver decides how its
// backend realizes them. Step 1 of the interface rework lands the image
// operations only: building an image of a source commit, reading and deleting
// images, settling a build whose outcome is unknown, and removing what an
// unsettled build left behind. Sessions, the workspace, terminals and project
// services join the contract later (rework/runtime-driver.md, M1 to M7).
package runtimedriver

import (
	"context"
	"errors"
)

// Images is the image part of the runtime driver. An implementation acts only
// in its configured scope, on objects that carry this P instance's labels,
// and never adopts, changes or deletes anything else. It reads native state
// fresh on every call and claims nothing after an error.
type Images interface {
	// Describe says what the driver offers.
	Describe() Capabilities
	// BuildImage builds an image of b's commit for this runtime and labels it
	// with b.Labels. It calls g once, after every check and right before its
	// first effect; if g fails, nothing happened. On success, and on any error
	// other than ErrUnknownOutcome, it leaves nothing of the build behind.
	BuildImage(ctx context.Context, b Build, g Gate) (Image, error)
	// Images lists every image in scope that carries this P instance's labels.
	Images(ctx context.Context) ([]Image, error)
	// InspectImage reads one image by its reference. It reports false when the
	// reference finds nothing, and ErrMismatch for an image whose labels are
	// not this P instance's.
	InspectImage(ctx context.Context, ref ImageRef) (Image, bool, error)
	// DeleteImage deletes exactly img: the same reference with the same labels.
	// It calls g right before the delete and succeeds when the image is gone,
	// including when it was already gone. Runtimes created from the image are
	// never affected.
	DeleteImage(ctx context.Context, img Image, g Gate) error
	// Settle says what became of an effect whose call returned
	// ErrUnknownOutcome, from native state only, so the answer holds after a
	// restart.
	Settle(ctx context.Context, e Effect) (Settlement, error)
	// Delete removes what a build left behind, such as its builder, and
	// succeeds once nothing of the build remains. It never deletes an image.
	// Deleting a session's runtime joins this operation in step 8.
	Delete(ctx context.Context, r Ref, g Gate) error
}

// Capabilities describe a driver. Step 1 needs only the image builder's
// version, which is part of every image's identity: an upgrade that changes
// how images are built builds new images even for a commit that has one.
type Capabilities struct {
	ImageBuilder string
}

// Kind says what a Ref names.
type Kind string

const KindBuild Kind = "build"

// Ref names one driver-side object by P's identity for it. A build's ID is a
// UUID core chose for that one attempt: a new build gets a new reference.
type Ref struct {
	Kind Kind
	ID   string
}

// ImageRef is the driver's own reference to an image, opaque to core (an
// image fingerprint on Incus).
type ImageRef string

// Labels are P's identity facts for an object. The driver stores them with
// the object, immutably, and returns them verbatim.
type Labels map[string]string

// The labels core gives every commit image. The driver may store them under
// its own names; it returns them under these.
const (
	LabelProject        = "project"
	LabelSourceCommit   = "source_commit"
	LabelBaseImage      = "base_image"
	LabelSystem         = "system"
	LabelBuilderVersion = "builder_version"
	LabelBuild          = "build"
)

// Build asks for an image of one source commit.
type Build struct {
	Ref     Ref          // kind build, new for every attempt
	Labels  Labels       // the image's identity, LabelBuild equal to Ref.ID
	Source  SourceExport // the commit, as a Git bundle core staged
	Network Network      // the builder's network
}

// SourceExport is a Git bundle of one commit and every commit it reaches,
// with one ref naming the commit. Core owns the file and removes it after the
// build. The builder clones it into the image's workspace.
type SourceExport struct {
	Bundle string // host path of the bundle file
	Ref    string // the bundle's one ref
	Commit string // the commit it names
}

// Network is the builder's network: none, or public egress at the address
// core reserved for it.
type Network struct {
	PublicIPv4 string // empty means no network
}

// Image is one built image.
type Image struct {
	Ref    ImageRef
	Labels Labels
	Shell  Shell // what the build found at the commit
	Size   int64 // bytes the image occupies, as the backend reports it
}

// Shell is the devShell outcome a build recorded in its image.
type Shell struct {
	Selection Selection
	// MaterialSHA256 identifies the activation material in the image; set
	// only for SelectionDevShell. Core passes it to the session's runtime kit
	// until session assembly moves into the driver (step 8).
	MaterialSHA256 string
}

// Selection is which environment an image activates.
type Selection string

const (
	SelectionNoFlake   Selection = "no-flake"   // the commit has no flake.nix: base packages only
	SelectionNoDefault Selection = "no-default" // its flake has no default devShell for the system
	SelectionDevShell  Selection = "devshell"   // its default devShell, realized into the image
)

// Gate is a core function the driver calls once, after every check and right
// before the first effect. Core records its durable intent there.
type Gate func() error

// EffectKind names the effect an Effect describes.
type EffectKind string

const (
	EffectBuild       EffectKind = "build"
	EffectDeleteImage EffectKind = "delete-image"
)

// Effect is a call whose outcome core asks the driver to settle.
type Effect struct {
	Kind  EffectKind
	Build Build // EffectBuild
	Image Image // EffectDeleteImage
}

// Settlement is what became of an effect.
type Settlement int

const (
	// Unknown: the backend cannot tell yet. Core keeps its guard and never
	// sends the same non-idempotent effect again.
	Unknown Settlement = iota
	// Applied: the intended outcome is present exactly and nothing admitted
	// on the target is still running.
	Applied
	// NotApplied: the backend proves the effect can never be admitted later.
	NotApplied
)

func (s Settlement) String() string {
	switch s {
	case Applied:
		return "applied"
	case NotApplied:
		return "not-applied"
	}
	return "unknown"
}

var (
	ErrUnknownOutcome = errors.New("the effect may or may not have happened")
	ErrMismatch       = errors.New("an object differs from the spec; it is never adopted")
	ErrUnavailable    = errors.New("the backend cannot be reached; nothing is claimed")
	ErrUnsupported    = errors.New("the driver does not offer this operation")
)
