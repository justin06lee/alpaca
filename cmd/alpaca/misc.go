package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/justin06lee/alpaca/internal/client"
	"github.com/justin06lee/alpaca/internal/config"
	"github.com/justin06lee/alpaca/internal/discovery"
)

// runAsk answers a single prompt and prints it to stdout.
//
// Output is plain text with no terminal styling so it composes with pipes:
// `alpaca ask "..." | pbcopy` and `... > notes.md` both do the obvious thing.
func runAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, strings.TrimLeft(`
alpaca ask — one-shot question, answer printed to stdout

  alpaca ask "why is the sky blue?"
  cat error.log | alpaca ask "what went wrong here?"

  --profile NAME   which linked server to use
  --model NAME     model to use (default: the server's first)
  --system TEXT    system prompt
  --temperature N  sampling temperature

`, "\n"))
	}
	profileName := fs.String("profile", "", "")
	model := fs.String("model", "", "")
	system := fs.String("system", "", "")
	temperature := fs.Float64("temperature", -1, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	prompt, err := readPrompt(fs.Args())
	if err != nil {
		return err
	}

	profiles, c, err := connectProfile(*profileName, client.Options{})
	if err != nil {
		return err
	}

	chosen := *model
	if chosen == "" {
		chosen = profiles.Entries[c.Profile().Name].Model
	}
	if chosen == "" {
		ctx, cancel := contextWithTimeout(15 * time.Second)
		models, err := c.Models(ctx)
		cancel()
		if err != nil {
			return err
		}
		if len(models) == 0 {
			return errors.New("the server has no models installed")
		}
		chosen = models[0].ID
	}

	messages := []client.Message{}
	if *system != "" {
		messages = append(messages, client.Message{Role: client.RoleSystem, Content: *system})
	}
	messages = append(messages, client.Message{Role: client.RoleUser, Content: prompt})

	req := client.ChatRequest{Model: chosen, Messages: messages}
	if *temperature >= 0 {
		req.Temperature = temperature
	}

	// No timeout on the whole generation: a long answer is not an error.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = c.Chat(ctx, req, func(ch client.Chunk) error {
		if ch.Content != "" {
			fmt.Print(ch.Content)
		}
		return nil
	})
	fmt.Println()
	return err
}

func readPrompt(args []string) (string, error) {
	inline := strings.TrimSpace(strings.Join(args, " "))

	var piped string
	if stat, err := os.Stdin.Stat(); err == nil && stat.Mode()&os.ModeCharDevice == 0 {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		piped = strings.TrimSpace(string(data))
	}

	switch {
	case inline != "" && piped != "":
		// Both given: the argument is the instruction, stdin is the material.
		return inline + "\n\n" + piped, nil
	case inline != "":
		return inline, nil
	case piped != "":
		return piped, nil
	default:
		return "", errors.New("no prompt given — try `alpaca ask \"your question\"`")
	}
}

func runModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	profileName := fs.String("profile", "", "which linked server to use")
	if err := fs.Parse(args); err != nil {
		return err
	}

	_, c, err := connectProfile(*profileName, client.Options{})
	if err != nil {
		return err
	}

	ctx, cancel := contextWithTimeout(20 * time.Second)
	defer cancel()

	models, err := c.Models(ctx)
	if err != nil {
		return err
	}
	if len(models) == 0 {
		fmt.Println("no models installed — run `ollama pull llama3.2` on the server")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tPARAMS\tQUANT\tSIZE")
	for _, m := range models {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.ID, dash(m.ParameterSize), dash(m.Quantization), humanBytes(m.Size))
	}
	return w.Flush()
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	profiles, err := config.LoadProfiles()
	if err != nil {
		return err
	}
	if len(profiles.Entries) == 0 {
		fmt.Println("no servers linked yet.")
		fmt.Println("Run `alpaca serve` on the machine with the model, then paste what it prints:")
		fmt.Println("  alpaca link alpaca1:...")
		return nil
	}

	for _, name := range profiles.Names() {
		profile := profiles.Entries[name]
		marker := "  "
		if name == profiles.Default {
			marker = "* "
		}
		fmt.Printf("%s%s\n", marker, name)

		ctx, cancel := contextWithTimeout(8 * time.Second)
		c, err := client.Connect(ctx, profile, client.Options{})
		if err != nil {
			cancel()
			fmt.Printf("    unreachable\n")
			// The full route-by-route breakdown is long; indent it so the
			// listing stays scannable.
			for _, line := range strings.Split(err.Error(), "\n") {
				fmt.Printf("    %s\n", strings.TrimSpace(line))
			}
			fmt.Println()
			continue
		}

		fmt.Printf("    %s\n", c.Route().Describe())
		if info, err := c.Info(ctx); err == nil {
			fmt.Printf("    ollama %s · %d models · alpaca %s\n",
				dash(info.Ollama.Version), info.Models, dash(info.Version))
		}
		_ = c.RememberRoute(profiles)
		cancel()
		fmt.Println()
	}
	return nil
}

func runProfiles(args []string) error {
	if len(args) == 0 {
		return listProfiles()
	}

	profiles, err := config.LoadProfiles()
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		return listProfiles()

	case "remove", "rm":
		if len(args) < 2 {
			return errors.New("usage: alpaca profiles remove <name>")
		}
		if _, err := config.UpdateProfiles(func(p *config.Profiles) error {
			if !p.Remove(args[1]) {
				return fmt.Errorf("no profile named %q", args[1])
			}
			return nil
		}); err != nil {
			return err
		}
		fmt.Printf("removed %q\n", args[1])
		return nil

	case "default":
		if len(args) < 2 {
			fmt.Println(profiles.Default)
			return nil
		}
		if _, err := config.UpdateProfiles(func(p *config.Profiles) error {
			if _, ok := p.Entries[args[1]]; !ok {
				return fmt.Errorf("no profile named %q", args[1])
			}
			p.Default = args[1]
			return nil
		}); err != nil {
			return err
		}
		fmt.Printf("default is now %q\n", args[1])
		return nil

	default:
		return fmt.Errorf("unknown subcommand %q (try list, remove, default)", args[0])
	}
}

func listProfiles() error {
	profiles, err := config.LoadProfiles()
	if err != nil {
		return err
	}
	if len(profiles.Entries) == 0 {
		fmt.Println("no servers linked yet — run `alpaca link <connect-string>`")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\tNAME\tMODEL\tLAST GOOD\tPUBLIC")
	for _, name := range profiles.Names() {
		p := profiles.Entries[name]
		marker := ""
		if name == profiles.Default {
			marker = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", marker, name, dash(p.Model), dash(p.LastGood), dash(p.Public))
	}
	return w.Flush()
}

func runDiscover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 3*time.Second, "how long to scan")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Printf("scanning the local network for %s…\n\n", *timeout)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	found, err := discovery.List(ctx)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Println("no alpaca servers found.")
		fmt.Println("Make sure `alpaca serve` is running and that this machine is on the same network.")
		return nil
	}

	profiles, _ := config.LoadProfiles()
	for _, result := range found {
		linked := ""
		if profiles != nil {
			if p, ok := profiles.ByID(result.ID); ok {
				linked = fmt.Sprintf("  (linked as %q)", p.Name)
			}
		}
		fmt.Printf("%s  %s%s\n", result.Name, result.ID, linked)
		for _, endpoint := range result.Endpoints {
			fmt.Printf("    %s\n", endpoint)
		}
		if linked == "" {
			fmt.Println("    not linked — you still need the connect string for its API key")
		}
		fmt.Println()
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func humanBytes(n int64) string {
	if n <= 0 {
		return "-"
	}
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGT"[exp])
}
