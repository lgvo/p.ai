// Package tui is a terminal client of the daemon. It owns no native authority.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lgvo/p.ai/internal/control"
)

type Client interface {
	Call(context.Context, string, any) (json.RawMessage, error)
}
type SocketClient struct{ Socket string }

func (c SocketClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	reply, err := control.Call(ctx, c.Socket, method, raw)
	if err != nil {
		return nil, fmt.Errorf("connection unavailable: %w (an accepted operation may still continue)", err)
	}
	var envelope struct {
		Result json.RawMessage   `json:"result"`
		Error  *control.RPCError `json:"error"`
	}
	if err = json.Unmarshal(reply, &envelope); err != nil {
		return nil, err
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("%s: %s", envelope.Error.Kind, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 {
		return nil, errors.New("missing daemon result")
	}
	return envelope.Result, nil
}

func request[T any](ctx context.Context, c Client, method string, params any) (T, error) {
	var result T
	raw, err := c.Call(ctx, method, params)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}

type params = map[string]any
type inventory struct {
	projects     []control.ProjectSummary
	sessions     []control.SessionView
	operations   []control.OperationSummary
	capabilities map[string]bool
}

func loadInventory(ctx context.Context, c Client) (inventory, error) {
	var result inventory
	caps, err := request[struct {
		Available []string `json:"available"`
	}](ctx, c, "system.capabilities", params{"v": 1})
	if err != nil {
		return result, err
	}
	result.capabilities = map[string]bool{}
	for _, m := range caps.Available {
		result.capabilities[m] = true
	}
	for _, spec := range []struct {
		method, field string
		limit         int
	}{{"project.list", "projects", 100}, {"session.list", "sessions", 8}, {"operation.list", "operations", 20}} {
		if !result.capabilities[spec.method] {
			continue
		}
		after := ""
		seen := map[string]bool{}
		for page := 0; page < 512; page++ {
			raw, e := c.Call(ctx, spec.method, params{"v": 1, "limit": spec.limit, "after": after})
			if e != nil {
				return result, e
			}
			var body struct {
				Projects   []control.ProjectSummary   `json:"projects"`
				Sessions   []control.SessionView      `json:"sessions"`
				Operations []control.OperationSummary `json:"operations"`
				Next       string                     `json:"next"`
			}
			if e = json.Unmarshal(raw, &body); e != nil {
				return result, e
			}
			result.projects = append(result.projects, body.Projects...)
			result.sessions = append(result.sessions, body.Sessions...)
			result.operations = append(result.operations, body.Operations...)
			if len(result.projects)+len(result.sessions)+len(result.operations) > 4096 {
				return result, errors.New("inventory exceeds 4096 records; use paginated CLI")
			}
			if body.Next == "" {
				break
			}
			if seen[body.Next] || body.Next == after {
				return result, errors.New("daemon returned a repeated pagination cursor")
			}
			seen[body.Next] = true
			after = body.Next
			if page == 511 {
				return result, errors.New("inventory pagination exceeds bound")
			}
		}
	}
	return result, nil
}

func callContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 25*time.Second)
}
