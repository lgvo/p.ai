package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// Exit statuses of the client commands.
const (
	// exitFailed: the daemon could not be reached or answered a failure, or
	// the operation failed.
	exitFailed = 1
	// exitUsage: the arguments or flags are invalid.
	exitUsage = 2
	// exitNotRun: nothing changed, because the state does not allow it.
	exitNotRun = 3
	// exitUnfinished: the operation is blocked, or runs on without the CLI.
	exitUnfinished = 4
)

// exitError ends a command with an exit status. Its text is for the person
// and goes to standard error; it is empty when the command already said
// everything. err is the daemon's answer it explains, if any, which --json
// prints.
type exitError struct {
	code int
	text string
	err  error
}

func (e *exitError) Error() string { return e.text }

func (e *exitError) Unwrap() error { return e.err }

func usageError(format string, args ...any) error {
	return &exitError{code: exitUsage, text: fmt.Sprintf(format, args...)}
}

// exitStatus is the exit status err carries: 0 for nil, the kind's status
// for an error the daemon answered, 1 for any other.
func exitStatus(err error) int {
	var exit *exitError
	var answer *api.Error
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return exit.code
	case errors.As(err, &answer):
		switch answer.Kind() {
		case api.KindInvalid:
			return exitUsage
		case api.KindConflict, api.KindCapacity, api.KindPreconditionFailed, api.KindPreconditionRequired:
			return exitNotRun
		}
	}
	return exitFailed
}

// cli runs the client task commands. It holds what a command reaches outside
// the daemon, so a test can give it buffers and its own pause.
type cli struct {
	stdout, stderr io.Writer
	// pause waits d between automatic repeats, or until ctx ends.
	pause func(ctx context.Context, d time.Duration) error
}

func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// A command is one task command, p NOUN VERB; VERB is empty for p status and
// p attach.
type command struct {
	noun, verb string
	// args names the positional arguments in the usage line.
	args    string
	summary string
	// minArgs and maxArgs bound the positional arguments.
	minArgs, maxArgs int
	// socketArg: with maxArgs arguments, the first selects the control
	// socket, as in p attach [SOCKET] SESSION.
	socketArg bool
	// flags declares the command's flags beyond --socket and --json.
	flags func(*flag.FlagSet, *invocation)
	run   func(*invocation) error
}

func (c *command) name() string { return strings.TrimSpace("p " + c.noun + " " + c.verb) }

// invocation is one run of a command: its arguments, flags and context.
type invocation struct {
	*cli
	ctx context.Context
	cmd *command
	// argv is the command line after "p", to show a command to repeat.
	argv []string
	// args are the positional arguments.
	args   []string
	socket string
	json   bool
	// key and noWait are the flags of changes.
	key    string
	noWait bool
	// The flags of single commands.
	origin, from, fromOrigin, project string
	attach, retained, refresh         bool
}

// findCommand is the command args name, or nil.
func findCommand(args []string) *command {
	if len(args) == 0 {
		return nil
	}
	for _, c := range commands {
		if c.noun == args[0] && (c.verb == "" || len(args) > 1 && c.verb == args[1]) {
			return c
		}
	}
	return nil
}

func isNoun(name string) bool {
	for _, c := range commands {
		if c.noun == name {
			return true
		}
	}
	return false
}

// runCommand runs the task command args name. An error it returns has been
// printed with --json, as {"error": …} for one the daemon answered.
func (c *cli) runCommand(ctx context.Context, args []string) error {
	cmd := findCommand(args)
	if cmd == nil {
		return usageError("%s has no command %q; run p %s --help", args[0], strings.Join(args[1:2], ""), args[0])
	}
	words := 1
	if cmd.verb != "" {
		words = 2
	}
	out := &outputWriter{w: c.stdout}
	local := *c
	local.stdout = out
	in := &invocation{cli: &local, ctx: ctx, cmd: cmd, argv: args}
	fs := in.flagSet()
	positional, err := parseInterspersed(fs, args[words:])
	if errors.Is(err, flag.ErrHelp) {
		return printNounHelp(c.stdout, cmd.noun)
	}
	if err != nil {
		return usageError("%v\nusage: %s", err, usageLine(cmd))
	}
	if len(positional) < cmd.minArgs || len(positional) > cmd.maxArgs {
		return usageError("usage: %s", usageLine(cmd))
	}
	for _, arg := range positional {
		if arg == "" {
			return usageError("an argument is empty\nusage: %s", usageLine(cmd))
		}
	}
	if cmd.socketArg && len(positional) == cmd.maxArgs {
		if in.socket != "" {
			return usageError("give the control socket once: as --socket or as the first argument")
		}
		in.socket, positional = positional[0], positional[1:]
	}
	in.args = positional
	if in.socket, err = api.ClientSocket(in.socket); err != nil {
		return &exitError{code: exitUsage, text: err.Error()}
	}
	err = cmd.run(in)
	var answer *api.Error
	if in.json && errors.As(err, &answer) {
		in.printJSON(map[string]any{"error": answer})
	}
	if err == nil {
		err = out.failure()
	}
	return in.explain(err)
}

// outputWriter keeps the first error writing standard output, so a command
// whose result could not be written does not succeed.
type outputWriter struct {
	w   io.Writer
	err error
}

// failure is the error to end with when a write failed, or nil.
func (o *outputWriter) failure() error {
	if o.err == nil {
		return nil
	}
	return &exitError{code: exitFailed, text: "Cannot write the output: " + o.err.Error()}
}

func (o *outputWriter) Write(p []byte) (int, error) {
	if o.err != nil {
		return 0, o.err
	}
	n, err := o.w.Write(p)
	o.err = err
	return n, err
}

// flagSet declares the command's flags on a new set.
func (in *invocation) flagSet() *flag.FlagSet {
	fs := flag.NewFlagSet(in.cmd.name(), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stringFlag(fs, &in.socket, "socket", "the control socket `PATH`")
	fs.BoolVar(&in.json, "json", false, "print the API's own types as JSON")
	if in.cmd.flags != nil {
		in.cmd.flags(fs, in)
	}
	return fs
}

// keyFlags are the flags of a change that takes an idempotency key.
func keyFlags(fs *flag.FlagSet, in *invocation) {
	fs.Func("key", "the idempotency `KEY`, 1 to 128 bytes, of a change to repeat; it is made at most once", func(key string) error {
		if len(key) < 1 || len(key) > 128 {
			return errors.New("a key has 1 to 128 bytes")
		}
		in.key = key
		return nil
	})
	noWaitFlag(fs, in)
}

// stringFlag declares a string flag that refuses an empty value: an empty
// variable in a script would otherwise read as an omitted flag and change
// what the command does.
func stringFlag(fs *flag.FlagSet, p *string, name, usage string) {
	fs.Func(name, usage, func(value string) error {
		if value == "" {
			return errors.New("an empty value")
		}
		*p = value
		return nil
	})
}

// noWaitFlag is the flag of a change that records an operation.
func noWaitFlag(fs *flag.FlagSet, in *invocation) {
	fs.BoolVar(&in.noWait, "no-wait", false, "print the operation ID once the daemon accepts the change, without waiting")
}

// parseInterspersed parses flags placed before, between or after the
// positional arguments, which Go's flag package stops at; after "--" every
// argument is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional, rest []string
	for i, arg := range args {
		if arg == "--" {
			args, rest = args[:i], args[i+1:]
			break
		}
	}
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return append(positional, rest...), nil
		}
		positional, args = append(positional, args[0]), args[1:]
	}
}

// usageLine is the command, its arguments and its flags.
func usageLine(cmd *command) string {
	parts := []string{cmd.name()}
	if cmd.args != "" {
		parts = append(parts, cmd.args)
	}
	in := &invocation{cmd: cmd}
	in.flagSet().VisitAll(func(f *flag.Flag) {
		if f.Name == "socket" || f.Name == "json" {
			return
		}
		value, _ := flag.UnquoteUsage(f)
		if value == "" {
			parts = append(parts, "[--"+f.Name+"]")
		} else {
			parts = append(parts, "[--"+f.Name+" "+value+"]")
		}
	})
	return strings.Join(parts, " ")
}
