package control

import (
	"context"

	"github.com/lgvo/p.ai/internal/api"
)

type RemovalPreviewRequest struct {
	UUID                      string `json:"uuid"`
	Kind                      string `json:"kind"`
	AcknowledgeMissingRuntime bool   `json:"acknowledge_missing_runtime,omitempty"`
}

type RemovalPreviewAPI interface {
	PreviewRemoval(context.Context, RemovalPreviewRequest) (api.RemovalPreview, error)
}

type DiscardConfirmRequest struct {
	Key               string `json:"key"`
	UUID              string `json:"uuid"`
	ConfirmationToken string `json:"confirmation_token"`
}

type DiscardAPI interface {
	DiscardSession(context.Context, DiscardConfirmRequest) (api.Operation, error)
}
type DeleteAPI interface {
	DeleteSession(context.Context, DiscardConfirmRequest) (api.Operation, error)
}

func validHexToken(value string) bool { return api.ValidToken(value) }
