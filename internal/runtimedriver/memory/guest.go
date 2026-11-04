package memory

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// The memory driver's guest layout. The workspace is the one place core
// knows; everything else is the driver's own choice.
const (
	workspaceRoot = "/workspace"
	workspaceUID  = 1000
)

// guestFile is one file of a session's guest plan, at an absolute guest
// path.
type guestFile struct {
	path string
	node node
}

// guestPlan is every guest file Create installs for s, parents before
// children. The first two are regular files of several bytes each, so that
// a Create can die between them or in the middle of the second.
func guestPlan(s runtimedriver.Session) []guestFile {
	root := func(path string, mode uint32, data string) guestFile {
		return guestFile{path, node{typ: runtimedriver.FileRegular, mode: mode, data: []byte(data)}}
	}
	user := func(path string, typ runtimedriver.FileType, mode uint32, data string) guestFile {
		return guestFile{path, node{typ: typ, uid: workspaceUID, gid: workspaceUID, mode: mode, data: []byte(data)}}
	}
	plan := []guestFile{
		root("/etc/p/command", 0o644, strings.Join(s.Command, "\x00")),
		root("/etc/p/git-host-key", 0o644, s.GitHostKey+"\n"),
		user("/etc/p/git-key", runtimedriver.FileRegular, 0o400, string(s.GitKey)),
	}
	for _, a := range s.Assets {
		plan = append(plan, root("/etc/p/assets/"+a.Role, a.Mode, string(a.Data)))
	}
	return append(plan,
		user(workspaceRoot, runtimedriver.FileDirectory, 0o755, ""),
		user(workspaceRoot+"/.git", runtimedriver.FileDirectory, 0o755, ""),
		user(workspaceRoot+"/.git/HEAD", runtimedriver.FileRegular, 0o644, "ref: refs/heads/"+s.Workspace.Branch+"\n"),
	)
}

// install gives the session's stopped runtime every device and guest file
// it lacks and marks it assembled. A guest file already there must be
// identical to the spec's: one that differs refuses the whole install
// before anything is installed.
func (d *Driver) install(s runtimedriver.Session) (runtimedriver.Observation, error) {
	w := d.w
	plan := guestPlan(s)
	w.mu.Lock()
	o, err := w.observe(s)
	if err == nil && (o == nil || o.state != runtimedriver.StateStopped) {
		err = errors.New("the runtime to install is gone or no longer stopped")
	}
	if err == nil {
		err = o.differingGuestFile(plan)
	}
	if err != nil {
		w.mu.Unlock()
		return runtimedriver.Observation{}, err
	}
	o.devices.endpoints = s.Endpoints
	o.devices.address = s.Network.PublicIPv4
	for _, g := range s.Grants {
		o.devices.grants[g.Name] = g
	}
	w.mu.Unlock()
	written := 0
	for _, f := range plan {
		w.mu.Lock()
		present := o.files[f.path] != nil
		w.mu.Unlock()
		if present {
			continue
		}
		if written == 1 {
			if err := d.dieBeforeSecondWrite(o, f); err != nil {
				return runtimedriver.Observation{}, err
			}
		}
		w.mu.Lock()
		o.files[f.path] = &f.node
		w.mu.Unlock()
		written++
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	o.assembled = true
	return o.observation(s), nil
}

// differingGuestFile refuses a plan whose file differs from one already in
// the private root. Called with w.mu held.
func (o *object) differingGuestFile(plan []guestFile) error {
	for _, f := range plan {
		if current := o.files[f.path]; current != nil && !current.equals(f.node.file()) {
			return fmt.Errorf("%w: guest file %s differs from the spec", runtimedriver.ErrMismatch, f.path)
		}
	}
	return nil
}

// dieBeforeSecondWrite dies at a death armed for the second guest write of
// a Create: before it, after it landed whole, or after half of it landed.
func (d *Driver) dieBeforeSecondWrite(o *object, f guestFile) error {
	dd := d.armedDeath(BetweenGuestWrites, DuringGuestWrite, DuringPartialGuestWrite)
	if dd == nil {
		return nil
	}
	if dd.point != BetweenGuestWrites {
		landed := f.node
		if dd.point == DuringPartialGuestWrite {
			landed.data = landed.data[:len(landed.data)/2]
		}
		d.w.mu.Lock()
		o.files[f.path] = &landed
		d.w.mu.Unlock()
	}
	return d.die(dd)
}

// armedDeath takes the armed death when it is at one of points.
func (d *Driver) armedDeath(points ...CreatePoint) *death {
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	dd := w.death
	if dd == nil {
		return nil
	}
	for _, p := range points {
		if dd.point == p {
			w.death = nil
			return dd
		}
	}
	return nil
}

// die stops the driver at a death: the call waits for the driver to be
// killed and effects nothing more.
func (d *Driver) die(dd *death) error {
	close(dd.reached)
	<-d.dead
	return errDied
}
