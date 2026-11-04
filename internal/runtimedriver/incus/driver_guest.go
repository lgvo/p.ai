package incus

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimekit"
)

// agentAssetRole is the one asset role of a session: the agent adapter,
// which a session may have.
const agentAssetRole = "p-codex-adapter"

// sessionGuestPlan is the guest file plan of a session, from its spec and the
// image its runtime is created from.
func (b *Backend) sessionGuestPlan(ctx context.Context, s runtimedriver.Session) (guestPlan, error) {
	shell, err := b.imageShell(ctx, s.Image)
	if err != nil {
		return guestPlan{}, err
	}
	return guestPlanFor(s, shell, b.config.PublicEgress)
}

// imageShell reads the devShell outcome of the image a session is created
// from: a commit image of this P instance carries one; any other image is the
// base image, which has none.
func (b *Backend) imageShell(ctx context.Context, ref runtimedriver.ImageRef) (*runtimedriver.Shell, error) {
	images, err := b.imageList(ctx)
	if err != nil {
		return nil, err
	}
	var found []builderImageJSON
	for _, image := range images {
		if image.Fingerprint == string(ref) {
			found = append(found, image)
		}
	}
	switch {
	case len(found) == 0:
		return nil, errors.New("pinned container image not present in confined project")
	case len(found) > 1:
		return nil, errors.New("ambiguous image reference")
	case found[0].Properties["p.instance"] != b.config.PInstanceID:
		return nil, nil
	}
	image, err := b.commitImage(found[0])
	if err != nil {
		return nil, err
	}
	return &image.Shell, nil
}

// guestPlanFor is the guest file plan of a session created from an image
// with shell, nil for the base image. The image's devShell selection decides
// activation and marks the workspace as already cloned; without a commit
// image, the runtime kit clones the workspace from P at start.
func guestPlanFor(s runtimedriver.Session, shell *runtimedriver.Shell, egress *PublicEgressConfig) (guestPlan, error) {
	assets, agentSHA256, err := sessionAssets(s.Assets)
	if err != nil {
		return guestPlan{}, err
	}
	config := runtimekit.Config{Schema: "p.runtime-session/v1", Activation: "base", Command: slices.Clone(s.Command)}
	workspace := runtimekit.WorkspaceConfig{Schema: "p.workspace/v1", Repository: s.Workspace.Repository, Branch: s.Workspace.Branch, InitialOID: s.Workspace.Commit}
	if shell != nil {
		if shell.Selection == runtimedriver.SelectionDevShell {
			config = runtimekit.Config{Schema: "p.runtime-session/v2", Activation: "devshell", MaterialSHA256: shell.MaterialSHA256, Command: config.Command}
		}
		workspace.Schema, workspace.EnvironmentSelection = "p.workspace/v2", string(shell.Selection)
	}
	// Each later schema adds one part of the session to the earlier ones.
	if agentSHA256 != "" {
		config.Schema, config.AgentSHA256 = "p.runtime-session/v3", agentSHA256
	}
	if len(s.Grants) != 0 {
		config.Schema, config.FilesystemMounts = "p.runtime-session/v4", guestGrants(s.Grants)
	}
	if s.Network.PublicIPv4 != "" {
		if egress == nil {
			return guestPlan{}, errors.New("public network requires the public egress substrate")
		}
		config.Schema = "p.runtime-session/v5"
		config.PublicNetwork = &runtimekit.PublicNetwork{Address: s.Network.PublicIPv4, Gateway: strings.SplitN(egress.BridgeIPv4, "/", 2)[0], DNS: slices.Clone(egress.DNS)}
	}
	files, err := sessionGuestFiles(config, workspace, s.GitKey, s.GitHostKey)
	if err != nil {
		return guestPlan{}, err
	}
	return guestPlan{files: slices.Concat(hostFiles(), assets, files), material: config.MaterialSHA256}, nil
}

// sessionAssets checks the session's assets: the agent adapter, if the
// session has one, and nothing else. It returns the adapter's guest file and
// digest.
func sessionAssets(assets []runtimedriver.Asset) ([]guestInstall, string, error) {
	switch {
	case len(assets) == 0:
		return nil, "", nil
	case len(assets) > 1 || assets[0].Role != agentAssetRole:
		return nil, "", errors.New("a session's only asset is its agent adapter")
	}
	a := assets[0]
	install, err := assetInstall(a.Role, a.Mode, a.Data, a.SHA256)
	if err != nil {
		return nil, "", err
	}
	return []guestInstall{install}, a.SHA256, nil
}

// guestGrants are what the guest's runtime kit knows of the session's grants:
// their names, flags and source identity, never their host paths.
func guestGrants(grants []runtimedriver.Grant) []runtimekit.FilesystemGrant {
	guest := make([]runtimekit.FilesystemGrant, 0, len(grants))
	for _, g := range grants {
		guest = append(guest, runtimekit.FilesystemGrant{Name: g.Name, Type: g.Type, Access: g.Access, Executable: g.Executable, Device: g.Device, Inode: g.Inode})
	}
	return guest
}
