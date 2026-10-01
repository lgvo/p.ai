package main

import (
	"fmt"
	"os"

	"github.com/lgvo/p.ai/internal/runtimekit"
)

func main() {
	var err error
	if len(os.Args) != 2 {
		err = fmt.Errorf("expected one fixed runtime-kit operation")
	} else {
		switch os.Args[1] {
		case "prepare-endpoints":
			err = runtimekit.PrepareEndpoints()
		case "prepare-grants":
			err = runtimekit.PrepareFilesystemMounts()
		case "prepare-network":
			err = runtimekit.PreparePublicNetwork()
		case "validate":
			err = runtimekit.Validate()
		case "init-workspace":
			err = runtimekit.InitWorkspace()
		case "stage-workspace":
			err = runtimekit.StageWorkspace()
		case "populate-workspace":
			err = runtimekit.PopulateWorkspace()
		case "prepare-host":
			err = runtimekit.PrepareHost()
		case "run-command":
			err = runtimekit.RunCommand()
		case "start-host":
			err = runtimekit.StartHost()
		case "git-stream":
			err = runtimekit.GitStream()
		case "shutdown":
			err = runtimekit.Shutdown()
		default:
			err = fmt.Errorf("unknown runtime-kit operation")
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "p-runtime-kit:", err)
		os.Exit(1)
	}
}
