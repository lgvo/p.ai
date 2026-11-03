package gitnames

import (
	"fmt"
	"strings"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// Check is CheckNames for a driver whose backend is Git. It checks each
// value by its kind's rule and returns the values as core uses them: a
// creation source as a NameBranch with the branch's name or as a
// NameCommit, a publication destination as the origin branch's name. The
// first value at fault is named by an *sourcedriver.InvalidName.
//
// renameBranch is the driver's rule for NameRenameBranch, which may run
// Git; it fails only when it cannot decide.
func Check(names []sourcedriver.Name, renameBranch func(string) (bool, error)) ([]sourcedriver.Name, error) {
	checked := make([]sourcedriver.Name, 0, len(names))
	for i, name := range names {
		value, ok, err := classify(name, renameBranch)
		if err != nil {
			return nil, fmt.Errorf("%s value %d: %w", name.Kind, i, err)
		}
		if !ok {
			return nil, &sourcedriver.InvalidName{Index: i, Name: name}
		}
		checked = append(checked, value)
	}
	return checked, nil
}

// classify checks one value and returns it as core uses it.
func classify(name sourcedriver.Name, renameBranch func(string) (bool, error)) (sourcedriver.Name, bool, error) {
	v := name.Value
	switch name.Kind {
	case sourcedriver.NameBranch:
		return name, Branch(v), nil
	case sourcedriver.NameRenameBranch:
		if !RenameBranch(v) {
			return name, false, nil
		}
		ok, err := renameBranch(v)
		return name, ok, err
	case sourcedriver.NameOriginRef:
		return name, OriginRef(v), nil
	case sourcedriver.NameOriginURL:
		return name, OriginURL(v), nil
	case sourcedriver.NameCommit:
		return name, OID(v), nil
	case sourcedriver.NameCreationSource:
		if branch, ok := strings.CutPrefix(v, "refs/heads/"); ok {
			return sourcedriver.Name{Kind: sourcedriver.NameBranch, Value: branch}, Branch(branch), nil
		}
		return sourcedriver.Name{Kind: sourcedriver.NameCommit, Value: v}, OID(v), nil
	case sourcedriver.NameDestination:
		branch, ok := strings.CutPrefix(v, "refs/heads/")
		return sourcedriver.Name{Kind: sourcedriver.NameDestination, Value: branch}, ok && OriginRef(v), nil
	}
	return name, false, nil
}

// HeadEffect reports whether e names a head effect's states as Settle takes
// them: a creation from absent to a commit, a deletion from a commit to
// absent, of a P branch.
func HeadEffect(e sourcedriver.Effect) bool {
	from, to := e.Old, e.New
	switch e.Kind {
	case sourcedriver.EffectCreateBranch:
	case sourcedriver.EffectDeleteBranch:
		from, to = e.New, e.Old
	default:
		return false
	}
	return Branch(e.Branch) && from == "" && OID(string(to))
}
