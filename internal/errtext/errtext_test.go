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

// apiLossKinds is the value of every loss kind constant in internal/api.
func apiLossKinds(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../api/plans.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		value, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, ident := range value.Names {
			if i >= len(value.Values) || !strings.HasPrefix(ident.Name, "Loss") || ident.Name == "LossNotEnumerated" {
				continue
			}
			if lit, ok := value.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				kinds[ident.Name], _ = strconv.Unquote(lit.Value)
			}
		}
		return false
	})
	if len(kinds) == 0 {
		t.Fatal("found no loss kinds in internal/api")
	}
	return kinds
}

func TestEveryLossKindHasASentence(t *testing.T) {
	for name, kind := range apiLossKinds(t) {
		if lossSentences[kind] == nil {
			t.Errorf("%s (%q) has no sentence", name, kind)
		}
	}
}

// A loss sentence names where the lost thing lives and how many there are;
// one P cannot rule out reads "possibly".
func TestLossNamesItsTargetAndCount(t *testing.T) {
	session := "4f1c2a9e-0000-4000-8000-000000000001"
	for _, test := range []struct {
		item api.Loss
		want []string
	}{
		{api.Loss{Kind: api.LossRuntimeData, Target: api.TargetSession(session), Certain: true}, []string{"session " + session}},
		{api.Loss{Kind: api.LossPCommits, Target: api.TargetBranch("team/app", "work"), Count: 2, Certain: true}, []string{"2 commits", "branch work"}},
		{api.Loss{Kind: api.LossPCommits, Target: api.TargetBranch("team/app", "work"), Count: 1, Certain: true}, []string{"1 commit ", "branch work"}},
		{api.Loss{Kind: api.LossPCommits, Target: api.TargetProject("team/app"), Count: 3, Certain: true}, []string{"3 commits", "project team/app"}},
		{api.Loss{Kind: "something_new", Target: api.TargetProject("team/app"), Certain: true}, []string{"something_new", "project team/app"}},
	} {
		got := Loss(test.item)
		for _, want := range test.want {
			if !strings.Contains(got, want) {
				t.Errorf("%+v reads %q, without %q", test.item, got, want)
			}
		}
		if strings.HasPrefix(got, "possibly") {
			t.Errorf("a certain loss reads %q", got)
		}
	}
	uncertain := Loss(api.Loss{Kind: api.LossUnreachableObjects, Target: api.TargetProject("team/app"), Reason: api.LossNotEnumerated})
	if !strings.HasPrefix(uncertain, "possibly: ") {
		t.Errorf("an uncertain loss reads %q", uncertain)
	}
	if gone := Gone(api.Loss{Kind: api.LossRuntimeData, Target: api.TargetSession(session), Certain: true}); !strings.Contains(gone, "session "+session) || gone == Loss(api.Loss{Kind: api.LossRuntimeData, Target: api.TargetSession(session), Certain: true}) {
		t.Errorf("a runtime already gone reads %q", gone)
	}
}

// A target reads as the command line names it.
func TestTargetReadsAsTheCommandLineNamesIt(t *testing.T) {
	for target, want := range map[string]string{
		api.TargetSession("4f1c"):                  "session 4f1c",
		api.TargetOperation("9b0e"):                "operation 9b0e",
		api.TargetProject("team/app"):              "project team/app",
		api.TargetBranch("team/app", "work"):       "branch team/app:work",
		api.TargetOriginBranch("team/app", "main"): "branch main of the origin of team/app",
		"something": "something",
	} {
		if got := Target(target); got != want {
			t.Errorf("%s reads %q, want %q", target, got, want)
		}
	}
}
