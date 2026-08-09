// Command alpaca self-hosts an LLM behind a networked API and chats with it.
//
// One binary plays both parts: `alpaca serve` on the machine with Ollama and a
// GPU, `alpaca chat` everywhere else.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

// version is overridden at build time with -ldflags "-X main.version=…".
var version = ""

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	command := os.Args[1]
	args := os.Args[2:]

	var err error
	switch command {
	case "serve":
		err = runServe(args)
	case "chat":
		err = runChat(args)
	case "link":
		err = runLink(args)
	case "ask":
		err = runAsk(args)
	case "models":
		err = runModels(args)
	case "status":
		err = runStatus(args)
	case "profiles":
		err = runProfiles(args)
	case "discover":
		err = runDiscover(args)
	case "version", "--version", "-v":
		fmt.Println("alpaca " + buildVersion())
		return
	case "help", "--help", "-h":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "alpaca: unknown command %q\n\n", command)
		usage()
		os.Exit(2)
	}

	if err != nil {
		// Errors are written for a person reading a terminal, so they get a
		// bare message rather than a Go-style wrapped chain prefix.
		fmt.Fprintln(os.Stderr, "alpaca: "+err.Error())
		os.Exit(1)
	}
}

func buildVersion() string {
	if version != "" {
		return version
	}
	// A `go install`-ed binary has no ldflags, but does carry VCS metadata.
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				rev := setting.Value
				if len(rev) > 12 {
					rev = rev[:12]
				}
				return "dev (" + rev + ")"
			}
		}
	}
	return "dev"
}

func usage() {
	fmt.Fprint(os.Stderr, strings.TrimLeft(`
alpaca — self-host a model, use it everywhere

ON THE MACHINE WITH THE MODEL
  alpaca serve                 start the API and print a connect string

ON EVERY OTHER MACHINE
  alpaca link <connect-string> save the server (paste what serve printed)
  alpaca chat                  open the chat interface
  alpaca ask "question"        one-shot answer, prints to stdout

OTHER
  alpaca models                list models on the server
  alpaca status                show which servers are linked and reachable
  alpaca profiles              manage saved servers
  alpaca discover              find alpaca servers on this network
  alpaca version

Run any command with --help for its options.
`, "\n"))
}
