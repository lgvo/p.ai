package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// MoveDuringRead moves the head through the driver's between-reads seam.
func (f *fixture) MoveDuringRead(project, name string, tip sourcedriver.OID) {
	d := f.driver
	d.seams.mu.Lock()
	defer d.seams.mu.Unlock()
	d.seams.betweenReads = func() { f.SetHead(project, name, tip) }
}

// SetHeads points every named head at tip in one Git transaction.
func (f *fixture) SetHeads(project string, names []string, tip sourcedriver.OID) {
	var input strings.Builder
	for _, name := range names {
		input.WriteString("update refs/heads/" + name + " " + string(tip) + "\n")
	}
	f.git(project, input.String(), "update-ref", "--stdin")
}

// DieAfterHeadEffect fails the next head effect right after Git's ref
// update, through the driver's effect-landed seam.
func (f *fixture) DieAfterHeadEffect() {
	d := f.driver
	d.seams.mu.Lock()
	defer d.seams.mu.Unlock()
	d.seams.effectLanded = func() error { return errors.New("the driver died") }
}

// DamageHead writes the loose ref file of the head directly.
func (f *fixture) DamageHead(project, name string, tip sourcedriver.OID) {
	f.t.Helper()
	repo, err := f.driver.repository(project)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "refs", "heads", name), []byte(string(tip)+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}
