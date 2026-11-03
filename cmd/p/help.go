package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// helpArguments says what each argument name of the usage lines means.
const helpArguments = `Arguments:
  PROJECT         a project path, such as team/app
  PROJECT:BRANCH  a P branch of a project, such as team/app:work
  SESSION         a session UUID, or PROJECT:BRANCH for the session that holds that branch
  ID              an operation ID
  NAME            a service name as p service list prints it
`

// helpFlags says what the flags every client command shares do.
const helpFlags = `Every client command takes --socket PATH and --json. The control socket is
--socket (or SOCKET where a usage line shows it), then a nonempty P_SOCKET,
then /var/lib/p/control.sock. --json prints the API's own types as one JSON
value; an error the daemon answered prints as {"error": …}.

A change waits for the operation it starts and says how it ended; --no-wait
prints the operation's ID once the daemon accepts it. A change makes its own
idempotency key; after a lost reply it prints the command to repeat with
--key KEY, which makes the change at most once.

A change that can lose work or replace something (project delete, branch
delete, origin publish, session discard, delete and cleanup, and session
create --replace) first shows its plan, with a warning of everything it
loses, and asks [y/N] on a terminal. --force runs it without asking, after a
10-second countdown when it loses work. --dry-run shows the plan and prints
its change tag; --if-match TAG runs that plan without asking, if nothing
changed since and within two minutes.

Exit status: 0 done; 1 failed; 2 invalid arguments; 3 not run, nothing
changed; 4 not finished: the operation is blocked, or runs on without the CLI.
`

// helpOthers lists the client commands outside the task commands, and the
// host commands.
const helpOthers = `Other client commands:
  p tui [SOCKET] [--snapshot …] [--theme auto|light|dark]
                  Open the terminal browser
  p api [SOCKET] METHOD [JSON_PARAMS]
                  Send one control-API request and print its whole reply;
                  for protocol work, with no keys, waiting or prompts

Host commands (run on the P host as the instance owner):
  p daemon HOST_JSON
  p plugins install | update | remove | defaults | conformance | list |
            activate | plan-assets | emit | run-event …
  p version
`

// printHelp prints p help: every command, then the arguments, flags, other
// commands and host commands.
func printHelp(to io.Writer) error {
	w := &outputWriter{w: to}
	fmt.Fprint(w, "Usage: p NOUN VERB ARGUMENTS [FLAGS]; p NOUN --help describes one noun's commands.\n\nCommands:\n")
	t := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, cmd := range commands {
		fmt.Fprintf(t, "  %s\t%s\n", strings.TrimSpace(cmd.name()+" "+cmd.args), cmd.summary)
	}
	t.Flush()
	fmt.Fprint(w, "\n"+helpArguments+"\n"+helpFlags+"\n"+helpOthers)
	return w.failure()
}

// printNounHelp prints the commands of noun with their flags.
func printNounHelp(to io.Writer, noun string) error {
	w := &outputWriter{w: to}
	for _, cmd := range commands {
		if cmd.noun != noun {
			continue
		}
		fmt.Fprintf(w, "%s\n    %s\n", usageLine(cmd), cmd.summary)
		in := &invocation{cmd: cmd}
		in.flagSet().VisitAll(func(f *flag.Flag) {
			if f.Name == "socket" || f.Name == "json" {
				return
			}
			value, usage := flag.UnquoteUsage(f)
			fmt.Fprintf(w, "    --%s %s\n        %s\n", f.Name, value, usage)
		})
		fmt.Fprintln(w)
	}
	fmt.Fprint(w, helpArguments+"\nRun p help for the flags every command takes and the exit statuses.\n")
	return w.failure()
}

// usage is the error for a command line p cannot run.
func usage() error {
	return usageError("unknown command; run p help for the commands")
}
