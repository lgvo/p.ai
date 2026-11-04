// Package protocol is how P's daemon reaches a runtime driver that runs as
// its own executable: JSON-RPC 2.0 over a Unix stream connection, one
// newline-terminated JSON frame per message, decoded strictly. Requests go
// both ways: core calls the driver's operations, and the driver calls core's
// gates. A Client implements runtimedriver.Driver over a connection; Serve
// serves a runtimedriver.Driver on one.
//
// The connection starts with a hello: core sends Version and the driver's
// configuration, and the driver answers with the version it agreed and its
// capabilities, or refuses. Nothing else is sent before it.
//
// Each terminal has its own connection, a socket pair whose far end core
// passes with the open_terminal request (SCM_RIGHTS). It carries the
// terminal's bytes only: input one way, output the other, and closing its
// write side hangs the terminal up. Resizing and the terminal's end travel
// as calls on the main connection.
//
// A call never returns while its gate runs: a gate that runs when the
// connection is lost or the call is cancelled is waited for. A call whose
// connection is lost after its request was sent then ends as follows: an
// effect returns runtimedriver.ErrUnknownOutcome, unless it took a gate that
// never ran, which proves that nothing happened, or that refused, whose
// refusal it returns; a read returns runtimedriver.ErrUnavailable. Errors
// keep their kinds across the connection, the context errors included. A
// call whose context ended returns an error that carries the context's
// error, whether the driver's answer or the end of waiting for it came
// first; a success the driver answered is returned.
//
// The connection, the hello, the calls both ways and the rules of a lost or
// cancelled call are driverwire's, which other drivers' protocols share;
// this package adds runtimedriver's methods, its error kinds and the
// terminals.
package protocol

import (
	"encoding/json"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Version is the protocol this build speaks. Core and the driver agree it in
// the hello; a driver that speaks another one is refused.
const Version = "p.runtime-driver/v1"

// MaxFrameBytes bounds one frame, its newline included. A session's assets,
// up to 1 MiB each, travel in the Create, OpenTerminal and Settle requests.
const MaxFrameBytes = driverwire.MaxFrameBytes

// The descriptors a supervisor gives the driver it starts: the connection,
// and the driver's own executable, which it was started from and may close.
const (
	ConnFD       = driverwire.ConnFD
	ExecutableFD = driverwire.ExecutableFD
)

// Hello is the driver's configuration, which core sends with the version.
type Hello struct {
	// Config is the driver's own part of host configuration, which only the
	// driver reads.
	Config json.RawMessage
	// Core holds the settings core owns that a driver needs.
	Core Core
}

// Core is the part of P's trusted host configuration that a driver needs but
// core owns.
type Core struct {
	InstanceID         string        `json:"instance_id"` // the P instance UUID the driver labels objects with
	EndpointPrefix     string        `json:"endpoint_prefix"`
	DiskSourceCeilings []string      `json:"disk_source_ceilings"`
	BuilderStoragePool string        `json:"builder_storage_pool"` // empty disables image builds
	PublicEgress       *PublicEgress `json:"public_egress"`        // nil when there is no egress substrate
}

// PublicEgress is the public-egress substrate host configuration names.
type PublicEgress struct {
	Network           string   `json:"network"`
	ACL               string   `json:"acl"`
	BridgeIPv4        string   `json:"bridge_ipv4"`
	DNS               []string `json:"dns"`
	SudoBinary        string   `json:"sudo_binary"`
	NftBinary         string   `json:"nft_binary"`
	BridgeProofBinary string   `json:"bridge_proof_binary"`
}

// The methods. Every driver operation but Describe is one; Describe's
// answer comes with the hello. The hello, gates and cancel are driverwire's.
const (
	methodCheck           = "check"
	methodInventory       = "inventory"
	methodInspect         = "inspect"
	methodCreate          = "create"
	methodStart           = "start"
	methodStop            = "stop"
	methodHostLog         = "host_log"
	methodOpenTerminal    = "open_terminal"
	methodResizeTerminal  = "resize_terminal"
	methodWaitTerminal    = "wait_terminal"
	methodReadWorkspace   = "read_workspace"
	methodWriteWorkspace  = "write_workspace"
	methodRemoveWorkspace = "remove_workspace"
	methodServices        = "services"
	methodServiceAction   = "service_action"
	methodServiceLogs     = "service_logs"
	methodBuildImage      = "build_image"
	methodImages          = "images"
	methodInspectImage    = "inspect_image"
	methodDeleteImage     = "delete_image"
	methodSettle          = "settle"
	methodDelete          = "delete"
)

// effects are the methods that change runtime state. A lost call to one may
// have happened; a lost call to any other method changed nothing.
var effects = map[string]bool{
	methodCreate: true, methodStart: true, methodStop: true, methodOpenTerminal: true,
	methodWriteWorkspace: true, methodRemoveWorkspace: true, methodServiceAction: true,
	methodBuildImage: true, methodDeleteImage: true, methodDelete: true,
}

// wire is the runtime driver's protocol over driverwire.
var wire = &driverwire.Protocol{
	Name:           "runtime driver",
	Version:        Version,
	Effects:        effects,
	Descriptors:    map[string][]string{methodOpenTerminal: {"terminal connection"}},
	Unavailable:    runtimedriver.ErrUnavailable,
	UnknownOutcome: runtimedriver.ErrUnknownOutcome,
	Kinds:          errorKinds{},
}
