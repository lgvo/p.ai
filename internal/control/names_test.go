package control

import (
	"context"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

func TestCheckNamesAnswersInvalidNamingTheFieldTheDriverRefuses(t *testing.T) {
	driver := sourcememory.NewDriver(sourcememory.NewWorld())
	_, err := CheckNames(context.Background(), driver,
		NameField{"branch", sourcedriver.Name{Kind: sourcedriver.NameBranch, Value: "work"}},
		NameField{"expected_old_tip", sourcedriver.Name{Kind: sourcedriver.NameCommit, Value: "not-a-commit"}})
	answer := answerError(context.Background(), err)
	if answer == nil || answer.Kind() != api.KindInvalid || answer.Data.Field != "expected_old_tip" {
		t.Fatalf("an invalid commit ID answered %v", err)
	}
}

func TestCheckNamesReturnsTheValuesAsCoreUsesThem(t *testing.T) {
	driver := sourcememory.NewDriver(sourcememory.NewWorld())
	names, err := CheckNames(context.Background(), driver,
		NameField{"source", sourcedriver.Name{Kind: sourcedriver.NameCreationSource, Value: "refs/heads/main"}})
	if err != nil || len(names) != 1 || names[0] != (sourcedriver.Name{Kind: sourcedriver.NameBranch, Value: "main"}) {
		t.Fatalf("a branch source came back as %v, %v", names, err)
	}
}

func TestCheckNamesAnswersFailedGitWhenTheDriverCannotCheck(t *testing.T) {
	driver := sourcememory.NewDriver(sourcememory.NewWorld())
	driver.Kill()
	_, err := CheckNames(context.Background(), driver,
		NameField{"branch", sourcedriver.Name{Kind: sourcedriver.NameBranch, Value: "work"}})
	answer := answerError(context.Background(), err)
	if answer == nil || answer.Kind() != api.KindFailed || answer.Reason() != api.ReasonGit {
		t.Fatalf("a driver that cannot check answered %v", err)
	}
}
