package protocol

import (
	"encoding/json"
	"fmt"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// The wire forms of runtimedriver's types: the same fields, named for the
// protocol. Nil and empty slices and maps keep their difference, so a value
// crosses the connection unchanged.

type wireRef struct {
	Kind runtimedriver.Kind `json:"kind"`
	ID   string             `json:"id"`
}

type wireWorkspace struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
}

type wireGrant struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	Type       string `json:"type"`
	Access     string `json:"access"`
	Executable bool   `json:"executable"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
	OwnerUID   uint32 `json:"owner_uid"`
	OwnerGID   uint32 `json:"owner_gid"`
}

type wireNetwork struct {
	PublicIPv4 string `json:"public_ipv4"`
}

type wireAsset struct {
	Role   string `json:"role"`
	Mode   uint32 `json:"mode"`
	Data   []byte `json:"data"`
	SHA256 string `json:"sha256"`
}

type wireSession struct {
	Ref        wireRef                `json:"ref"`
	Labels     runtimedriver.Labels   `json:"labels"`
	Image      runtimedriver.ImageRef `json:"image"`
	Command    []string               `json:"command"`
	Workspace  wireWorkspace          `json:"workspace"`
	GitKey     []byte                 `json:"git_key"`
	GitHostKey string                 `json:"git_host_key"`
	Endpoints  string                 `json:"endpoints"`
	Grants     []wireGrant            `json:"grants"`
	Network    wireNetwork            `json:"network"`
	Assets     []wireAsset            `json:"assets"`
}

type wireTarget struct {
	Session wireSession               `json:"session"`
	At      runtimedriver.Incarnation `json:"at"`
}

type wireHostState struct {
	Ready      bool   `json:"ready"`
	Unknown    bool   `json:"unknown"`
	Diagnostic string `json:"diagnostic"`
}

type wireObservation struct {
	State       runtimedriver.State       `json:"state"`
	Name        string                    `json:"name"`
	Incarnation runtimedriver.Incarnation `json:"incarnation"`
	Complete    bool                      `json:"complete"`
	Host        wireHostState             `json:"host"`
}

type wireObject struct {
	Name        string                    `json:"name"`
	Ref         wireRef                   `json:"ref"`
	Labels      runtimedriver.Labels      `json:"labels"`
	Incarnation runtimedriver.Incarnation `json:"incarnation"`
	State       runtimedriver.State       `json:"state"`
}

type wireInventory struct {
	Capacity int          `json:"capacity"`
	Objects  []wireObject `json:"objects"`
}

type wireSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type wireFile struct {
	Type runtimedriver.FileType `json:"type"`
	UID  int                    `json:"uid"`
	GID  int                    `json:"gid"`
	Mode uint32                 `json:"mode"`
	Data []byte                 `json:"data"`
}

type wireService struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	State       runtimedriver.ServiceState `json:"state"`
	Detail      string                     `json:"detail"`
}

type wireLogs struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type wireSourceExport struct {
	Bundle string `json:"bundle"`
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

type wireBuild struct {
	Ref     wireRef              `json:"ref"`
	Labels  runtimedriver.Labels `json:"labels"`
	Source  wireSourceExport     `json:"source"`
	Network wireNetwork          `json:"network"`
}

type wireShell struct {
	Selection      runtimedriver.Selection `json:"selection"`
	MaterialSHA256 string                  `json:"material_sha256"`
}

type wireImage struct {
	Ref    runtimedriver.ImageRef `json:"ref"`
	Labels runtimedriver.Labels   `json:"labels"`
	Shell  wireShell              `json:"shell"`
	Size   int64                  `json:"size"`
}

type wireEffect struct {
	Kind     runtimedriver.EffectKind `json:"kind"`
	Target   wireTarget               `json:"target"`
	Build    wireBuild                `json:"build"`
	Image    wireImage                `json:"image"`
	Path     string                   `json:"path"`
	Content  wireFile                 `json:"content"`
	Terminal string                   `json:"terminal"`
}

type wireCapabilities struct {
	ImageBuilder    string   `json:"image_builder"`
	BuilderPolicy   string   `json:"builder_policy"`
	Networks        []string `json:"networks"`
	EgressAddresses int      `json:"egress_addresses"`
	Services        bool     `json:"services"`
}

// each converts every element of in with f, keeping nil nil.
func each[A, B any](in []A, f func(A) B) []B {
	if in == nil {
		return nil
	}
	out := make([]B, len(in))
	for i := range in {
		out[i] = f(in[i])
	}
	return out
}

func toSession(s runtimedriver.Session) wireSession {
	return wireSession{
		Ref: wireRef(s.Ref), Labels: s.Labels, Image: s.Image, Command: s.Command, Workspace: wireWorkspace(s.Workspace),
		GitKey: s.GitKey, GitHostKey: s.GitHostKey, Endpoints: s.Endpoints,
		Grants:  each(s.Grants, func(g runtimedriver.Grant) wireGrant { return wireGrant(g) }),
		Network: wireNetwork(s.Network),
		Assets:  each(s.Assets, func(a runtimedriver.Asset) wireAsset { return wireAsset(a) }),
	}
}

func (s wireSession) session() runtimedriver.Session {
	return runtimedriver.Session{
		Ref: runtimedriver.Ref(s.Ref), Labels: s.Labels, Image: s.Image, Command: s.Command, Workspace: runtimedriver.Workspace(s.Workspace),
		GitKey: s.GitKey, GitHostKey: s.GitHostKey, Endpoints: s.Endpoints,
		Grants:  each(s.Grants, func(g wireGrant) runtimedriver.Grant { return runtimedriver.Grant(g) }),
		Network: runtimedriver.Network(s.Network),
		Assets:  each(s.Assets, func(a wireAsset) runtimedriver.Asset { return runtimedriver.Asset(a) }),
	}
}

func toTarget(t runtimedriver.Target) wireTarget {
	return wireTarget{Session: toSession(t.Session), At: t.At}
}

func (t wireTarget) target() runtimedriver.Target {
	return runtimedriver.Target{Session: t.Session.session(), At: t.At}
}

func toObservation(o runtimedriver.Observation) wireObservation {
	return wireObservation{State: o.State, Name: o.Name, Incarnation: o.Incarnation, Complete: o.Complete, Host: wireHostState(o.Host)}
}

func (o wireObservation) observation() runtimedriver.Observation {
	return runtimedriver.Observation{State: o.State, Name: o.Name, Incarnation: o.Incarnation, Complete: o.Complete, Host: runtimedriver.HostState(o.Host)}
}

func toInventory(inv runtimedriver.Inventory) wireInventory {
	return wireInventory{Capacity: inv.Capacity, Objects: each(inv.Objects, func(o runtimedriver.Object) wireObject {
		return wireObject{Name: o.Name, Ref: wireRef(o.Ref), Labels: o.Labels, Incarnation: o.Incarnation, State: o.State}
	})}
}

func (inv wireInventory) inventory() runtimedriver.Inventory {
	return runtimedriver.Inventory{Capacity: inv.Capacity, Objects: each(inv.Objects, func(o wireObject) runtimedriver.Object {
		return runtimedriver.Object{Name: o.Name, Ref: runtimedriver.Ref(o.Ref), Labels: o.Labels, Incarnation: o.Incarnation, State: o.State}
	})}
}

func toBuild(b runtimedriver.Build) wireBuild {
	return wireBuild{Ref: wireRef(b.Ref), Labels: b.Labels, Source: wireSourceExport(b.Source), Network: wireNetwork(b.Network)}
}

func (b wireBuild) build() runtimedriver.Build {
	return runtimedriver.Build{Ref: runtimedriver.Ref(b.Ref), Labels: b.Labels, Source: runtimedriver.SourceExport(b.Source), Network: runtimedriver.Network(b.Network)}
}

func toImage(img runtimedriver.Image) wireImage {
	return wireImage{Ref: img.Ref, Labels: img.Labels, Shell: wireShell(img.Shell), Size: img.Size}
}

func (img wireImage) image() runtimedriver.Image {
	return runtimedriver.Image{Ref: img.Ref, Labels: img.Labels, Shell: runtimedriver.Shell(img.Shell), Size: img.Size}
}

func toEffect(e runtimedriver.Effect) wireEffect {
	return wireEffect{Kind: e.Kind, Target: toTarget(e.Target), Build: toBuild(e.Build), Image: toImage(e.Image), Path: e.Path, Content: wireFile(e.Content), Terminal: e.Terminal}
}

func (e wireEffect) effect() runtimedriver.Effect {
	return runtimedriver.Effect{Kind: e.Kind, Target: e.Target.target(), Build: e.Build.build(), Image: e.Image.image(), Path: e.Path, Content: runtimedriver.File(e.Content), Terminal: e.Terminal}
}

// The settlements by name: a number on the wire could be read as another
// answer.
var settlements = map[runtimedriver.Settlement]string{
	runtimedriver.Unknown: "unknown", runtimedriver.Applied: "applied", runtimedriver.NotApplied: "not-applied",
}

type wireSettlement struct {
	Settlement string `json:"settlement"`
}

func toSettlement(s runtimedriver.Settlement) (wireSettlement, error) {
	name, ok := settlements[s]
	if !ok {
		return wireSettlement{}, fmt.Errorf("the driver answered settlement %d, which P does not know", int(s))
	}
	return wireSettlement{Settlement: name}, nil
}

func (w wireSettlement) settlement() (runtimedriver.Settlement, error) {
	for s, name := range settlements {
		if name == w.Settlement {
			return s, nil
		}
	}
	return runtimedriver.Unknown, fmt.Errorf("unknown settlement %q", w.Settlement)
}

// The parameters and results of the methods.

type noParams struct{}

type helloParams struct {
	Protocol string          `json:"protocol"`
	Config   json.RawMessage `json:"config"`
	Core     Core            `json:"core"`
}

type helloResult struct {
	Protocol     string           `json:"protocol"`
	Capabilities wireCapabilities `json:"capabilities"`
}

type sessionParams struct {
	Session wireSession `json:"session"`
}

type createParams struct {
	Session wireSession               `json:"session"`
	At      runtimedriver.Incarnation `json:"at"`
	Gate    bool                      `json:"gate"`
}

type targetParams struct {
	Target wireTarget `json:"target"`
}

type hostLogResult struct {
	Text string `json:"text"`
}

type openTerminalParams struct {
	Target wireTarget `json:"target"`
	Size   wireSize   `json:"size"`
}

type terminalParams struct {
	Terminal string `json:"terminal"`
}

type resizeTerminalParams struct {
	Terminal string   `json:"terminal"`
	Size     wireSize `json:"size"`
}

type waitTerminalResult struct {
	Proven bool `json:"proven"`
}

type readWorkspaceParams struct {
	Target wireTarget `json:"target"`
	Path   string     `json:"path"`
	Limit  int64      `json:"limit"`
}

type writeWorkspaceParams struct {
	Target wireTarget `json:"target"`
	Path   string     `json:"path"`
	File   wireFile   `json:"file"`
	Gate   bool       `json:"gate"`
}

type removeWorkspaceParams struct {
	Target wireTarget `json:"target"`
	Path   string     `json:"path"`
	Gate   bool       `json:"gate"`
}

type serviceParams struct {
	Target wireTarget `json:"target"`
	Name   string     `json:"name"`
}

type serviceActionParams struct {
	Target wireTarget           `json:"target"`
	Name   string               `json:"name"`
	Action runtimedriver.Action `json:"action"`
}

type buildImageParams struct {
	Build wireBuild `json:"build"`
	Gate  bool      `json:"gate"`
}

type inspectImageParams struct {
	Ref runtimedriver.ImageRef `json:"ref"`
}

type inspectImageResult struct {
	Image wireImage `json:"image"`
	Found bool      `json:"found"`
}

type deleteImageParams struct {
	Image wireImage `json:"image"`
	Gate  bool      `json:"gate"`
}

type settleParams struct {
	Effect wireEffect `json:"effect"`
}

type deleteParams struct {
	Ref  wireRef                   `json:"ref"`
	At   runtimedriver.Incarnation `json:"at"`
	Gate bool                      `json:"gate"`
}

// gateParams name the call whose gate the driver calls.
type gateParams struct {
	Call uint64 `json:"call"`
}

// cancelParams name the call core no longer waits for.
type cancelParams struct {
	ID uint64 `json:"id"`
}
