// Package protocol is how P's daemon reaches a source driver that runs as
// its own executable: JSON-RPC 2.0 over a Unix stream connection, one
// newline-terminated JSON frame per message, decoded strictly. A Client
// implements sourcedriver.Driver over a connection; Serve serves a
// sourcedriver.Driver on one.
//
// The connection starts with a hello: core sends Version, the driver's
// configuration and the settings core owns, and the driver answers with the
// version it agreed and its Description, or refuses. Nothing else is sent
// before it. Each other operation is one method.
//
// Requests go both ways. Core calls the driver's operations, and the driver
// calls back what a call lent it while the call is in flight: a gate of
// DeleteRepository, CreateBranch, DeleteBranch and Publish, and ServeGit's
// open, approve and input. open and approve answer as core's Open and
// Approve. open is ServeGit's gate: it runs once, before any Git, so a
// ServeGit lost before open ran changed nothing.
//
// Each ServeGit has its own connection for the SSH channel's bytes, a
// socket pair whose far end core passes with the serve_git request
// (SCM_RIGHTS): the client's input one way, Git's output the other, and
// core's closing its write side ends the input. Core reads the client's
// input only from the driver's first read of it, which calls input, so a
// service that ends before reading leaves it untouched, as a driver in
// core's process would. The channel's standard error is a pipe whose write
// end goes with the request: a stream of its own, so it needs no framing,
// cannot hold Git's output back, and ends when the driver closes it.
//
// A call whose connection is lost ends by what may have happened, as
// driverwire says: an effect (creating or deleting a repository or a head,
// fetching from or publishing to the origin, and ServeGit) returns
// sourcedriver.ErrUnknownOutcome, unless its gate never ran, which proves
// nothing happened, or refused, whose refusal it returns; a read returns
// sourcedriver.ErrUnavailable. Errors keep their kinds across the
// connection, the context errors and the facts of *sourcedriver.InvalidName
// and *sourcedriver.WorkspaceMismatch included.
package protocol

import (
	"encoding/json"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// Version is the protocol this build speaks. Core and the driver agree it in
// the hello; a driver that speaks another one is refused.
const Version = "p.source-driver/v1"

// MaxFrameBytes bounds one frame, its newline included.
const MaxFrameBytes = driverwire.MaxFrameBytes

// Hello is the driver's configuration, which core sends with the version.
type Hello struct {
	// Config is the driver's own part of host configuration, which only the
	// driver reads.
	Config json.RawMessage
	// Core holds the settings core owns that a driver needs.
	Core Core
}

// Core is the part of P's trusted host configuration that a source driver
// needs but core owns.
type Core struct {
	// Storage is the directory the driver keeps P's repositories in, an
	// absolute path whose parent exists: P's state directory.
	Storage string `json:"storage"`
	// Executable is the path of the driver's executable, which the daemon
	// checked and started: a program the driver starts as itself, such as
	// its Git service's hook, is this file.
	Executable string `json:"executable"`
}

// The methods core calls. Every driver operation but Describe is one;
// Describe's answer comes with the hello. The hello, gates and cancel are
// driverwire's.
const (
	methodCheck              = "check"
	methodCheckNames         = "check_names"
	methodInspectRepository  = "inspect_repository"
	methodCreateRepository   = "create_repository"
	methodDeleteRepository   = "delete_repository"
	methodBranches           = "branches"
	methodInspectBranch      = "inspect_branch"
	methodCreateBranch       = "create_branch"
	methodDeleteBranch       = "delete_branch"
	methodInspectCommit      = "inspect_commit"
	methodLosingCommits      = "losing_commits"
	methodExport             = "export"
	methodRenameWorkspace    = "rename_workspace"
	methodServeGit           = "serve_git"
	methodObserveOrigin      = "observe_origin"
	methodFetchOrigin        = "fetch_origin"
	methodComparePublication = "compare_publication"
	methodPublish            = "publish"
	methodSettle             = "settle"
)

// The methods the driver calls back during a serve_git call.
const (
	methodOpen    = "open"
	methodApprove = "approve"
	methodInput   = "input"
)

// effects are the methods that change P's repositories or the origin. A
// lost call to one may have happened; a lost call to any other method
// changed nothing. Export and ComparePublication write only what core
// discards or what no head holds, and are reads.
var effects = map[string]bool{
	methodCreateRepository: true, methodDeleteRepository: true, methodCreateBranch: true, methodDeleteBranch: true,
	methodFetchOrigin: true, methodPublish: true, methodServeGit: true,
}

// wire is the source driver's protocol over driverwire.
var wire = &driverwire.Protocol{
	Name:           "source driver",
	Version:        Version,
	Effects:        effects,
	Descriptors:    map[string][]string{methodServeGit: {"Git stream", "Git error stream"}},
	Callbacks:      map[string]bool{methodOpen: true, methodApprove: true, methodInput: true},
	Unavailable:    sourcedriver.ErrUnavailable,
	UnknownOutcome: sourcedriver.ErrUnknownOutcome,
	Kinds:          errorKinds{},
}
