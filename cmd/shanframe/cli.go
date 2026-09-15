package main

// The command tree, on cobra: it gives every shell (bash, zsh, fish) tab
// completion for device names and verbs from one description, and `up`
// wires that completion into the user's shell. Running a verb still goes
// through dispatch — cobra only routes and completes; it does not parse
// flags (DisableFlagParsing), so every verb keeps its own argument shape.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/shawnpana/shanframe/internal/rendezvous"
	"github.com/spf13/cobra"
)

// actionFlags is what each device action accepts after its positional args.
var actionFlags = map[string][]string{
	"tunnel":     {"--socks", "--install", "--uninstall"},
	"cdp":        {"--port", "--local", "--json"},
	"startcmd":   {"--clear"},
	"screenshot": {"--json"},
	"click":      {"--right", "--double"},
	"tap":        {"--right", "--double"},
}

func actionNames() []string {
	out := []string{"run", "tunnel", "cdp", "startcmd", "term"}
	for v := range screenVerbs {
		out = append(out, v)
	}
	return out
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:                "shanframe",
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		CompletionOptions:  cobra.CompletionOptions{DisableDefaultCmd: true},
		RunE:               func(_ *cobra.Command, args []string) error { return dispatch(append([]string{"shanframe"}, args...)) },
		ValidArgsFunction:  completeDeviceLine,
	}
	root.SetHelpFunc(func(*cobra.Command, []string) { usage() })
	verb := func(name string, complete func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)) *cobra.Command {
		return &cobra.Command{Use: name, DisableFlagParsing: true, ValidArgsFunction: complete,
			RunE: func(_ *cobra.Command, args []string) error {
				return dispatch(append([]string{"shanframe", name}, args...))
			}}
	}
	flagsOnly := func(flags ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return flags, cobra.ShellCompDirectiveNoFileComp
		}
	}
	root.AddCommand(
		verb("ls", flagsOnly("--json")),
		verb("join", flagsOnly("--name", "--no-install")),
		verb("up", flagsOnly()),
		verb("down", flagsOnly()),
		verb("serve", flagsOnly()),
		verb("tunnels", flagsOnly()),
		verb("rename", flagsOnly()),
		verb("rm", completeDeviceArg),
		verb("connect", completeDeviceArg),
		verb("run", completeDeviceArg),
		verb("tunnel", completeDeviceArg),
		verb("startcmd", completeDeviceArg),
		completionCmd(root),
	)
	screencap := verb("_screencap", flagsOnly())
	screencap.Hidden = true
	root.AddCommand(screencap)
	return root
}

// completeDeviceLine completes `shanframe <device> <action> [flags]`.
func completeDeviceLine(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return deviceNames(), cobra.ShellCompDirectiveNoFileComp
	case 1:
		return actionNames(), cobra.ShellCompDirectiveNoFileComp
	}
	if strings.HasPrefix(toComplete, "-") {
		return actionFlags[args[1]], cobra.ShellCompDirectiveNoFileComp
	}
	if args[1] == "screenshot" && len(args) == 2 {
		return nil, cobra.ShellCompDirectiveDefault // a file name
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// completeDeviceArg completes the verbs that take a device first (rm, run …).
func completeDeviceArg(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return deviceNames(), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func deviceCachePath() string { return filepath.Join(configDir(), "devices.cache") }

// deviceNames is the live device list, falling back to the last one seen
// (every CLI run refreshes the cache) so completion still answers offline.
func deviceNames() []string {
	c, cancel, err := newClientTimeout(2 * time.Second)
	if err == nil {
		defer cancel()
		return names(<-c.devs)
	}
	b, err := os.ReadFile(deviceCachePath())
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func names(devs []rendezvous.Device) []string {
	out := make([]string, 0, len(devs))
	for _, d := range devs {
		out = append(out, d.Name)
	}
	return out
}

func cacheDeviceNames(devs []rendezvous.Device) {
	if len(devs) == 0 {
		return
	}
	_ = os.WriteFile(deviceCachePath(), []byte(strings.Join(names(devs), "\n")+"\n"), 0o600)
}

// completionCmd: `completion bash|zsh|fish` prints the script; `install`
// puts it where the shell loads it; `uninstall` takes it back out.
func completionCmd(root *cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: "completion", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		fmt.Println("usage: shanframe completion bash|zsh|fish   (prints the script)\n       shanframe completion install|uninstall  (for your shell; `up` installs it too)")
		return nil
	}}
	c.AddCommand(
		&cobra.Command{Use: "bash", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return root.GenBashCompletionV2(os.Stdout, true) }},
		&cobra.Command{Use: "zsh", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return root.GenZshCompletion(os.Stdout) }},
		&cobra.Command{Use: "fish", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return root.GenFishCompletion(os.Stdout, true) }},
		&cobra.Command{Use: "install", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
			note, err := installCompletion(root)
			if note != "" {
				fmt.Println(note)
			}
			return err
		}},
		&cobra.Command{Use: "uninstall", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return uninstallCompletion() }},
	)
	return c
}

const completionMarker = "# shanframe tab completion"

// shellRC is the startup file bash and zsh read; fish loads completions
// from a directory instead.
func shellRC(shell string) string {
	switch shell {
	case "zsh":
		return ".zshrc"
	case "bash":
		if runtime.GOOS == "darwin" {
			return ".bash_profile" // Terminal.app opens login shells
		}
		return ".bashrc"
	}
	return ""
}

func fishCompletionPath(home string) string {
	return filepath.Join(home, ".config", "fish", "completions", "shanframe.fish")
}

// installCompletion sets up completion for $SHELL: the script goes next to
// the config and one marked line in the rc file sources it (fish: the file
// goes in its completions dir). The note is what to tell the user; a shell
// we have no script for is not an error, just no note.
func installCompletion(root *cobra.Command) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch shell := filepath.Base(os.Getenv("SHELL")); shell {
	case "fish":
		p := fishCompletionPath(home)
		f, err := createFile(p)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if err := root.GenFishCompletion(f, true); err != nil {
			return "", err
		}
		return "tab completion for device names: added for fish (new terminals pick it up)", nil
	case "zsh", "bash":
		p := filepath.Join(configDir(), "completion."+shell)
		f, err := createFile(p)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if shell == "zsh" {
			// compdef needs the completion system; most rc files load it, not all
			fmt.Fprintln(f, "(( $+functions[compdef] )) || { autoload -Uz compinit && compinit; }")
			err = root.GenZshCompletion(f)
		} else {
			err = root.GenBashCompletionV2(f, true)
		}
		if err != nil {
			return "", err
		}
		rc := shellRC(shell)
		line := fmt.Sprintf("[ -f %q ] && . %q  %s", p, p, completionMarker)
		if err := setMarkedLine(filepath.Join(home, rc), completionMarker, line); err != nil {
			return "", err
		}
		return fmt.Sprintf("tab completion for device names: added to ~/%s (new terminals pick it up)", rc), nil
	}
	return "", nil
}

func uninstallCompletion() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var errs []error
	for _, rc := range []string{".zshrc", ".bashrc", ".bash_profile"} {
		errs = append(errs, setMarkedLine(filepath.Join(home, rc), completionMarker, ""))
	}
	for _, p := range []string{filepath.Join(configDir(), "completion.zsh"), filepath.Join(configDir(), "completion.bash"), fishCompletionPath(home)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func createFile(p string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	return os.Create(p)
}

// setMarkedLine replaces every line of the file carrying marker with line
// (appended at the end if none did), or removes them when line is empty. A
// missing file is created only when there is something to add.
func setMarkedLine(path, marker, line string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err != nil && line == "" {
		return nil
	}
	var kept []string
	had := false
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.Contains(l, marker) {
			had = true
			continue
		}
		kept = append(kept, l)
	}
	if len(kept) == 1 && kept[0] == "" {
		kept = nil
	}
	if line != "" {
		kept = append(kept, line)
	} else if !had {
		return nil
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out), 0o644)
}
