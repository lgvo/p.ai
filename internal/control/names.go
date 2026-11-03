package control

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// NameField is one request field whose syntax the source driver decides,
// such as a branch name, an origin ref or a commit ID.
type NameField struct {
	Field string // the request's field, as an invalid answer names it
	Name  sourcedriver.Name
}

// CheckNames has the source driver check fields and returns their values as
// core uses them, in order. A handler calls it right after its replay
// lookup, so a recorded request is answered from SQLite alone, and before it
// records anything. A value the driver refuses answers invalid naming its
// field; a driver that cannot check answers failed with reason git.
func CheckNames(ctx context.Context, driver sourcedriver.Driver, fields ...NameField) ([]sourcedriver.Name, error) {
	names := make([]sourcedriver.Name, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	checked, err := driver.CheckNames(ctx, names)
	var invalid *sourcedriver.InvalidName
	if errors.As(err, &invalid) && invalid.Index >= 0 && invalid.Index < len(fields) {
		return nil, Invalid(fields[invalid.Index].Field)
	}
	if err != nil {
		return nil, Failed(api.ReasonGit, err)
	}
	if len(checked) != len(fields) {
		return nil, Failed(api.ReasonGit, errors.New("the source driver answered for another number of names"))
	}
	return checked, nil
}
