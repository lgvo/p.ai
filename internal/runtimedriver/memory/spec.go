package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path"
	"slices"
	"strings"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// checkSession refuses a spec the driver cannot realize exactly. It reads
// only the spec and the World's configuration.
func (w *World) checkSession(s runtimedriver.Session) error {
	if s.Ref.Kind != runtimedriver.KindSession || s.Ref.ID == "" {
		return errors.New("invalid session reference")
	}
	if s.Labels[runtimedriver.LabelProject] == "" {
		return errors.New("the session's project label is missing")
	}
	if len(s.Command) == 0 || s.GitHostKey == "" || s.Workspace.Branch == "" {
		return errors.New("the session's command, Git host key and branch are required")
	}
	if !path.IsAbs(s.Endpoints) || path.Clean(s.Endpoints) != s.Endpoints {
		return errors.New("the endpoint directory must be an absolute clean path")
	}
	if err := checkGrants(s.Grants); err != nil {
		return err
	}
	if err := checkAssets(s.Assets); err != nil {
		return err
	}
	if s.Image == "" {
		return errors.New("the session's image is required")
	}
	if s.Network.PublicIPv4 != "" {
		if !slices.Contains(w.config.Capabilities.Networks, runtimedriver.NetworkPublicEgress) {
			return errors.New("the driver offers no public egress")
		}
		if ip := net.ParseIP(s.Network.PublicIPv4); ip == nil || ip.To4() == nil {
			return errors.New("invalid public-egress address")
		}
	}
	return nil
}

func checkGrants(grants []runtimedriver.Grant) error {
	seen := map[string]bool{}
	for _, g := range grants {
		if g.Name == "" || strings.ContainsAny(g.Name, "/\x00") || g.Name == "." || g.Name == ".." || seen[g.Name] {
			return fmt.Errorf("invalid grant name %q", g.Name)
		}
		seen[g.Name] = true
		if !path.IsAbs(g.Source) || g.Type != "file" && g.Type != "directory" || g.Access != "read-only" && g.Access != "read-write" {
			return fmt.Errorf("invalid grant %q", g.Name)
		}
	}
	return nil
}

func checkAssets(assets []runtimedriver.Asset) error {
	seen := map[string]bool{}
	for _, a := range assets {
		sum := sha256.Sum256(a.Data)
		if a.Role == "" || strings.ContainsAny(a.Role, "/\x00") || seen[a.Role] || a.Mode&^0o777 != 0 || a.SHA256 != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("invalid session asset %q", a.Role)
		}
		seen[a.Role] = true
	}
	return nil
}
