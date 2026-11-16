//go:build linux || darwin

// Terminal front end for macOS and Linux: prompts, progress and sudo.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const silentFlag = "-y, --yes"

// ---------------------------------------------------------------- output

var useColor = isTerminal(os.Stdout) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"

func paint(code, s string) string {
	if !useColor {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func message(opts options, kind msgKind, text string) {
	switch kind {
	case msgPlain:
		fmt.Println(text)
	case msgError:
		fmt.Fprintln(os.Stderr, "\n"+paint("1;31", "✗ ")+indentRest(text))
	case msgWarning:
		fmt.Fprintln(os.Stderr, "\n"+paint("1;33", "! ")+indentRest(text))
	default:
		if opts.onlyDir {
			return // the parent process reports the result
		}
		fmt.Println("\n" + paint("1;32", "✓ ") + indentRest(text))
	}
}

func progress(_ options, line string) {
	header()
	fmt.Println(paint("2", "  • "+line))
}

var headerShown bool

// header prints the installer's name once, before the first output.
func header() {
	if !headerShown {
		headerShown = true
		fmt.Println(paint("1", title) + paint("2", " "+version) + "\n")
	}
}

// indentRest indents every line after the first so they line up after the
// "✓ " marker.
func indentRest(text string) string {
	lines := strings.Split(text, "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = "  " + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func reportCanceled() {
	fmt.Println("\nCanceled. Nothing was changed.")
}

// ---------------------------------------------------------------- prompts

// input is where answers come from. When the installer runs from
// "curl ... | bash", stdin is the script, so read from the terminal instead.
var input = func() *bufio.Reader {
	if isTerminal(os.Stdin) {
		return bufio.NewReader(os.Stdin)
	}
	if tty, err := os.Open("/dev/tty"); err == nil {
		return bufio.NewReader(tty)
	}
	return nil
}()

// ask prints the question and returns the trimmed, lower-cased answer. ok is
// false when there is no terminal to ask on or input ended (Ctrl+D).
func ask(text, prompt string) (answer string, ok bool) {
	header()
	fmt.Println("  " + indentRest(strings.TrimRight(text, "\n")))
	if input == nil {
		fmt.Fprintln(os.Stderr, "\nNo terminal to ask on. Run again with --yes to answer yes to everything.")
		return "", false
	}
	fmt.Print("\n" + paint("1", prompt))
	line, err := input.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		fmt.Println()
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(line)), true
}

func confirm(text string) bool {
	a, ok := ask(text, "Continue? [Y/n] ")
	return ok && (a == "" || a == "y" || a == "yes")
}

func askRetry(text string) bool {
	a, ok := ask(text, "Press Enter when Zen is closed (or q to quit): ")
	return ok && a != "q" && a != "quit" && a != "n" && a != "no"
}

func askUpdateOrUninstall(text string) choice {
	for {
		a, ok := ask(text, "[U]pdate, [R]emove or [C]ancel? [U] ")
		if !ok {
			return choiceCancel
		}
		switch a {
		case "", "u", "update", "y", "yes":
			return choiceUpdate
		case "r", "remove", "uninstall":
			return choiceUninstall
		case "c", "cancel", "n", "no", "q", "quit":
			return choiceCancel
		}
	}
}

// ---------------------------------------------------------------- elevation

// runElevatedHelper runs the install-dir half through sudo and returns its
// exit code. Only that half runs as root, so files in the profile stay owned
// by the user.
func runElevatedHelper(opts options, shownDir string, args []string) int {
	exe, err := os.Executable()
	if err != nil {
		return exitFailed
	}
	if os.Geteuid() == 0 {
		return exitFailed // already root and still can't write: sudo won't help
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		message(opts, msgError, "Zen's program folder needs administrator rights:\n"+shownDir+
			"\n\nsudo was not found. Run this as root, then run the installer again:\n  "+
			shellQuote(append([]string{exe}, args...)))
		return exitFailed
	}
	fmt.Println("\n" + paint("1", "Administrator rights needed") +
		"\n  Zen's program folder belongs to the system:\n  " + shownDir +
		"\n  sudo will ask for your password to copy the loader there.")
	cmd := exec.Command(sudo, append([]string{"--", exe}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if tty, err := os.Open("/dev/tty"); err == nil {
		defer tty.Close()
		cmd.Stdin = tty
	} else {
		cmd.Stdin = os.Stdin
	}
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &exitErr) && exitErr.ExitCode() > 0:
		return exitErr.ExitCode()
	default:
		return exitFailed
	}
}

func shellQuote(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r == '/' || r == '-' || r == '_' || r == '.' || r == '=' ||
				r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
		}) < 0 {
			out[i] = a
		} else {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}

// ---------------------------------------------------------------- paths

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

// expandHome turns a leading ~ into the home folder (shells don't expand it
// inside quotes or after "--zen-dir=").
func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" {
		return homeDir()
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(homeDir(), rest)
	}
	return p
}

func dirExistsAt(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
