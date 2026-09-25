package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type cacheLife struct {
	statusLife
	listCalls, previewCalls, collectCalls int
}

func (l *cacheLife) ListEnvironmentCache(_ context.Context, project, after string, limit int) ([]EnvironmentCacheItem, string, error) {
	l.listCalls++
	if project != "team/a" || after != "" || limit != 2 {
		return nil, "", ErrInvalid
	}
	return []EnvironmentCacheItem{{Project: project, Key: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), LogicalSize: 4096, ImageStatus: "present", RelatedCount: 1}}, "", nil
}
func (l *cacheLife) PreviewEnvironmentCollection(_ context.Context, project, key, token, after string, limit int) (EnvironmentCollectionPreview, error) {
	l.previewCalls++
	if project != "team/a" || key != strings.Repeat("a", 64) || token != "" || after != "" || limit != 2 {
		return EnvironmentCollectionPreview{}, ErrInvalid
	}
	return EnvironmentCollectionPreview{Token: strings.Repeat("e", 32), Item: EnvironmentCacheItem{Project: project, Key: key, ImageStatus: "present"}, Warning: "reviewed warning"}, nil
}
func (l *cacheLife) CollectEnvironmentCache(_ context.Context, key, token string) (Operation, error) {
	l.collectCalls++
	if key != "collect-one" || token != strings.Repeat("e", 32) {
		return Operation{}, ErrInvalid
	}
	return Operation{ID: "collection-op", Kind: "environment.collect", Status: "running", Phase: "accepted"}, nil
}

func TestEnvironmentCacheRPCSurfaceIsClosedAndBounded(t *testing.T) {
	s, _ := openTestStore(t)
	l := &cacheLife{statusLife: statusLife{store: s}}
	h := StateHandlerWithLifecycle(s, nil, nil, l)
	ctx := context.Background()
	capabilities, rpcErr := h(ctx, "system.capabilities", json.RawMessage(`{"v":1}`))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	capJSON, _ := json.Marshal(capabilities)
	if !strings.Contains(string(capJSON), "environment.cache.collect") {
		t.Fatal("cache capability hidden")
	}
	for _, tc := range []struct{ method, params string }{
		{"environment.cache.list", `{"v":1,"project":"team/a","limit":2}`},
		{"environment.cache.preview", `{"v":1,"project":"team/a","environment_key":"` + strings.Repeat("a", 64) + `","limit":2}`},
		{"environment.cache.collect", `{"v":1,"key":"collect-one","confirmation_token":"` + strings.Repeat("e", 32) + `"}`},
	} {
		result, e := h(ctx, tc.method, json.RawMessage(tc.params))
		if e != nil || result == nil {
			t.Fatalf("%s: %v", tc.method, e)
		}
		bad := strings.TrimSuffix(tc.params, "}") + `,"command":"rm -rf /"}`
		if _, e := h(ctx, tc.method, json.RawMessage(bad)); e == nil {
			t.Fatalf("%s accepted extra authority", tc.method)
		}
	}
	if l.listCalls != 1 || l.previewCalls != 1 || l.collectCalls != 1 {
		t.Fatalf("invalid request reached broker: %+v", l)
	}
}
