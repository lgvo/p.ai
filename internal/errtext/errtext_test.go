package errtext

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

// kindSentenceSuffices lists the reasons whose kind's sentence already says
// what to do, so they have no sentence of their own.
var kindSentenceSuffices = map[string]bool{
	api.ReasonHelloRequired: true, api.ReasonDuplicateID: true, api.ReasonEnvelope: true, api.ReasonConnection: true,
	api.ReasonProtocol: true, api.ReasonJSONRPC: true, api.ReasonParam: true,
	api.ReasonPanic: true, api.ReasonStore: true, api.ReasonEncoding: true, api.ReasonUnexpected: true,
}

// apiReasons is the value of every Reason constant in internal/api.
func apiReasons(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("../api/*.go")
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			value, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, ident := range value.Names {
				if i >= len(value.Values) || !strings.HasPrefix(ident.Name, "Reason") {
					continue
				}
				if lit, ok := value.Values[i].(*ast.BasicLit); ok {
					reasons[ident.Name], _ = strconv.Unquote(lit.Value)
				}
			}
			return false
		})
	}
	return reasons
}

func TestEveryKindAndReasonHasASentence(t *testing.T) {
	for _, kind := range api.Kinds() {
		if kindSentences[kind] == "" {
			t.Errorf("kind %s has no sentence", kind)
		}
	}
	for name, reason := range apiReasons(t) {
		found := kindSentenceSuffices[reason] || operationSentences[reason] != "" || refusalSentences[reason] != ""
		for _, sentences := range reasonSentences {
			found = found || sentences[reason] != ""
		}
		if !found {
			t.Errorf("%s (%q) has no sentence", name, reason)
		}
	}
}

func TestSentenceFallsBack(t *testing.T) {
	retry := api.NewError(api.KindRetry, "something_new")
	if got := Sentence(retry); got != kindSentences[api.KindRetry] {
		t.Errorf("an unknown reason gave %q", got)
	}
	if got := Sentence(api.NewError(api.KindRetry, api.ReasonSessionHeld)); got != reasonSentences[api.KindRetry][api.ReasonSessionHeld] {
		t.Errorf("a known reason gave %q", got)
	}
	if got := Sentence(api.InvalidParam("limit")); got != "The request's limit is not valid." {
		t.Errorf("an invalid field gave %q", got)
	}
	if got := Sentence(&api.Error{Code: -32003, Message: "busy"}); got != "The daemon refused the request (busy)." {
		t.Errorf("an unknown kind gave %q", got)
	}
	if got := Sentence(errors.New("connection unavailable")); got != "connection unavailable" {
		t.Errorf("a plain error gave %q", got)
	}
	// A reason names a record kind or an authority only within its kind.
	if Sentence(api.NewError(api.KindNotConfigured, api.ReasonRuntime)) == Sentence(api.NewError(api.KindFailed, api.ReasonRuntime)) {
		t.Error("not_configured and failed runtime read the same")
	}
}

func TestOperationSentenceNamesTheNextStep(t *testing.T) {
	if got := Operation("blocked", api.ReasonRuntime); got != "The runtime failed; check Incus. Retry it once that is resolved." {
		t.Errorf("a blocked operation gave %q", got)
	}
	if got := Operation("failed", api.ReasonWorkspaceMismatch); got != operationSentences[api.ReasonWorkspaceMismatch]+" It ended; start a new request." {
		t.Errorf("a failed operation gave %q", got)
	}
	if got := Operation("blocked", "something_new"); got != "It stopped: something_new. Retry it once that is resolved." {
		t.Errorf("an unknown reason gave %q", got)
	}
	if Operation("running", "") != "" {
		t.Error("an operation without a reason has a sentence")
	}
}
