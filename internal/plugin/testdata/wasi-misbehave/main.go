// Command wasi-misbehave is a test module that ignores its command and either
// runs forever, floods stdout, or exits with a failure status, as selected at
// link time with -X main.mode.
package main

import (
	"os"
	"strings"
)

var mode string

func main() {
	switch mode {
	case "loop":
		for {
		}
	case "flood":
		os.Stdout.WriteString(strings.Repeat("x", 8192))
	case "exit":
		os.Exit(3)
	}
}
