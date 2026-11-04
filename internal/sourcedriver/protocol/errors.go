package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/driverwire"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/strictjson"
)

// errorData keeps an error's kinds and the facts its typed errors carry.
type errorData struct {
	Kinds []string `json:"kinds"`
	// InvalidName is a *sourcedriver.InvalidName's index and name.
	InvalidName *wireInvalidName `json:"invalid_name,omitempty"`
	// Mismatch is a *sourcedriver.WorkspaceMismatch's fields.
	Mismatch *wireMismatch `json:"workspace_mismatch,omitempty"`
	// Supported lists the versions the driver speaks, for a refused hello.
	Supported []string `json:"supported,omitempty"`
}

type wireInvalidName struct {
	Index int      `json:"index"`
	Name  wireName `json:"name"`
}

type wireMismatch struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

// The kinds an error can have, by name: sourcedriver's, and the context
// errors, which core tells apart from other failures as it does for a
// driver in its own process.
var sentinels = driverwire.Sentinels{
	{Name: "unknown_outcome", Err: sourcedriver.ErrUnknownOutcome},
	{Name: "invalid", Err: sourcedriver.ErrInvalid},
	{Name: "conflict", Err: sourcedriver.ErrConflict},
	{Name: "not_found", Err: sourcedriver.ErrNotFound},
	{Name: "limit", Err: sourcedriver.ErrLimit},
	{Name: "unavailable", Err: sourcedriver.ErrUnavailable},
	{Name: "canceled", Err: context.Canceled},
	{Name: "deadline_exceeded", Err: context.DeadlineExceeded},
}

const (
	kindInvalidName = "invalid_name"       // a *sourcedriver.InvalidName
	kindMismatch    = "workspace_mismatch" // a *sourcedriver.WorkspaceMismatch
)

// errorKinds keeps sourcedriver's error kinds across the connection.
type errorKinds struct{}

// Data is err's data object, with every kind it has.
func (errorKinds) Data(err error) any {
	data := &errorData{Kinds: sentinels.Names(err)}
	var invalid *sourcedriver.InvalidName
	if errors.As(err, &invalid) {
		data.Kinds = append(data.Kinds, kindInvalidName)
		data.InvalidName = &wireInvalidName{Index: invalid.Index, Name: wireName(invalid.Name)}
	}
	var mismatch *sourcedriver.WorkspaceMismatch
	if errors.As(err, &mismatch) {
		data.Kinds = append(data.Kinds, kindMismatch)
		data.Mismatch = &wireMismatch{Field: mismatch.Field, Expected: mismatch.Expected, Actual: mismatch.Actual}
	}
	return data
}

func (errorKinds) Decode(raw json.RawMessage) (driverwire.ErrorData, error) {
	var data errorData
	if err := strictjson.Decode(raw, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// Errors are the kinds d names. A kind P does not know makes the error one
// of its own, since its meaning cannot be kept.
func (d *errorData) Errors(message string) ([]error, error) {
	var kinds []error
	for _, name := range d.Kinds {
		switch name {
		case kindInvalidName:
			if d.InvalidName == nil {
				return nil, fmt.Errorf("the driver named no value for an invalid name: %s", message)
			}
			kinds = append(kinds, &sourcedriver.InvalidName{Index: d.InvalidName.Index, Name: sourcedriver.Name(d.InvalidName.Name)})
		case kindMismatch:
			if d.Mismatch == nil {
				return nil, fmt.Errorf("the driver named no field for a workspace mismatch: %s", message)
			}
			kinds = append(kinds, &sourcedriver.WorkspaceMismatch{Field: d.Mismatch.Field, Expected: d.Mismatch.Expected, Actual: d.Mismatch.Actual})
		default:
			kind := sentinels.Kind(name)
			if kind == nil {
				return nil, fmt.Errorf("the driver answered an error of unknown kind %q: %s", name, message)
			}
			kinds = append(kinds, kind)
		}
	}
	return kinds, nil
}

// RemoteError is an error the other side of the connection returned, with
// the kinds it had there: errors.Is and errors.As find them.
type RemoteError = driverwire.RemoteError
