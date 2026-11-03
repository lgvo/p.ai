// Package tui is a terminal client of the daemon. It owns no native authority.
package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// SocketClient opens one connection per call; each connection agrees the
// protocol before its call.
type SocketClient struct{ Socket string }

// Call implements api.Caller. An error the daemon answers keeps its kind and
// message; any other failure says that the connection failed.
func (c SocketClient) Call(ctx context.Context, method string, params, result any) error {
	conn, err := api.Dial(ctx, c.Socket)
	if err == nil {
		defer conn.Close()
		err = conn.Call(ctx, method, params, result)
	}
	var rpcErr *api.RPCError
	if err != nil && !errors.As(err, &rpcErr) {
		return fmt.Errorf("connection unavailable: %w (an accepted operation may still continue)", err)
	}
	return err
}

// daemonCall is one call the model starts with begin. method names it for the
// pages that wait on it; creation.branches is the TUI's own sequence of reads.
type daemonCall struct {
	method string
	run    func(context.Context, api.Caller) (any, error)
}

// callOf calls m with req; its result is m's result type.
func callOf[Req api.Request, Res any](m api.Method[Req, Res], req Req) daemonCall {
	return daemonCall{m.Name, func(ctx context.Context, c api.Caller) (any, error) { return api.Do(ctx, c, m, req) }}
}

// resultOf is the result of a call that callOf made for m. The type assertion
// cannot fail when accept handles each method's reply with its own method.
func resultOf[Req api.Request, Res any](v actionDone, _ api.Method[Req, Res]) Res {
	return v.result.(Res)
}

type inventory struct {
	projects   []api.ProjectSummary
	sessions   []api.SessionView
	operations []api.OperationSummary
	hello      api.HelloResult
}

// inventoryLimit bounds the records the TUI keeps; inventoryPages bounds the
// pages of each list.
const (
	inventoryLimit = 4096
	inventoryPages = 512
)

func loadInventory(ctx context.Context, c api.Caller) (inventory, error) {
	var result inventory
	hello, err := api.Do(ctx, c, api.SystemHello, api.HelloRequest{Protocol: api.Protocol})
	if err != nil {
		return result, err
	}
	result.hello = hello
	records := 0
	if hello.Has(api.CapabilityGit) {
		result.projects, err = pages(ctx, c, api.ProjectList, &records,
			func(after string) api.ProjectListRequest { return api.ProjectListRequest{Limit: 100, After: after} },
			func(r api.ProjectListResult) ([]api.ProjectSummary, string) { return r.Projects, r.Next })
		if err != nil {
			return result, err
		}
	}
	if hello.Has(api.CapabilityRuntime) {
		result.sessions, err = pages(ctx, c, api.SessionList, &records,
			func(after string) api.SessionListRequest { return api.SessionListRequest{Limit: 8, After: after} },
			func(r api.SessionListResult) ([]api.SessionView, string) { return r.Sessions, r.Next })
		if err != nil {
			return result, err
		}
		result.operations, err = pages(ctx, c, api.OperationList, &records,
			func(after string) api.OperationListRequest { return api.OperationListRequest{Limit: 20, After: after} },
			func(r api.OperationListResult) ([]api.OperationSummary, string) { return r.Operations, r.Next })
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// pages reads every page of m, counting the records into *records.
func pages[Req api.Request, Res, T any](ctx context.Context, c api.Caller, m api.Method[Req, Res], records *int, page func(after string) Req, items func(Res) ([]T, string)) ([]T, error) {
	var all []T
	after := ""
	seen := map[string]bool{}
	for i := 0; i < inventoryPages; i++ {
		r, err := api.Do(ctx, c, m, page(after))
		if err != nil {
			return all, err
		}
		values, next := items(r)
		all = append(all, values...)
		*records += len(values)
		if *records > inventoryLimit {
			return all, errors.New("inventory exceeds 4096 records; use paginated CLI")
		}
		if next == "" {
			return all, nil
		}
		if seen[next] || next == after {
			return all, errors.New("daemon returned a repeated pagination cursor")
		}
		seen[next] = true
		after = next
	}
	return all, errors.New("inventory pagination exceeds bound")
}

func callContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 25*time.Second)
}
