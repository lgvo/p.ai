// Package conformance is the suite every source driver must pass. A driver
// package runs it from its own tests with a Fixture: the driver under test
// and native controls that act on the backend's state directly, the way a
// session's push, an administrator, a crash or Git itself would.
//
// The cases are grouped by the part of the contract they check; every driver
// runs every group.
package conformance

import (
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// Fixture is one backend under test, with native state of its own that no
// other fixture shares.
//
// Every case uses Driver, Restart and Natives; the other control accessors
// are called only by the cases of their group.
type Fixture interface {
	// Driver is the driver under test. After Restart it is the new driver.
	Driver() sourcedriver.Driver
	// Restart replaces the driver with a new one over the same native
	// state, as after the driver's process died: calls in flight are lost
	// and return an error, and nothing the old driver started goes on.
	Restart()
	// Natives act on repositories directly.
	Natives() Natives

	Repositories() RepositoryControls
	Settling() SettlingControls
	Service() ServiceControls
	Origin() OriginControls
}

// Natives act on the repository of a project the driver created, as Git, a
// session's push or an administrator would, without the driver.
type Natives interface {
	// Commit writes a new commit object with the given parents into the
	// project's repository, reachable from no head, and returns its ID. Each
	// call writes a distinct commit.
	Commit(project string, parents ...sourcedriver.OID) sourcedriver.OID
	// Tree writes an object that is not a commit, a tree, into the
	// project's repository and returns its ID.
	Tree(project string) sourcedriver.OID
	// SetHead points the head refs/heads/<name> at tip, or removes it when
	// tip is empty.
	SetHead(project, name string, tip sourcedriver.OID)
	// SetSymbolic makes refs/heads/<name> a symbolic ref to
	// refs/heads/<target>, which need not exist.
	SetSymbolic(project, name, target string)
}

// testCase is one rule of the contract, checked on a fresh fixture.
type testCase struct {
	rule  string
	check func(t *testing.T, f Fixture)
}

// Run runs every case, each as a subtest named by its group and the rule it
// checks, on a fixture of its own.
func Run(t *testing.T, newFixture func(t *testing.T) Fixture) {
	groups := []struct {
		name  string
		cases []testCase
	}{
		{"names", nameCases},              // CheckNames, and every operation refusing what it refuses
		{"repositories", repositoryCases}, // Check and the repository operations
		{"heads", headCases},              // Branches, InspectBranch, CreateBranch, DeleteBranch
		{"commits", commitCases},          // InspectCommit, LosingCommits, Export
		{"settling", settlingCases},       // head effects whose outcome was lost, settled after a restart
		{"workspace", workspaceCases},     // RenameWorkspace on fixture files
		{"service", serviceCases},         // Describe's commands, ServeGit, and settling a lost service
		{"origin", originCases},           // ObserveOrigin, FetchOrigin, ComparePublication, Publish
	}
	for _, g := range groups {
		t.Run(g.name, func(t *testing.T) {
			for _, c := range g.cases {
				t.Run(c.rule, func(t *testing.T) { c.check(t, newFixture(t)) })
			}
		})
	}
}
