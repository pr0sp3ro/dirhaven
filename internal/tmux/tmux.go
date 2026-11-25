// Package tmux creates and opens project sessions in tmux.
package tmux

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pr0sp3ro/dirhaven/internal/config"
)

func SessionName(path string) string {
	var name strings.Builder

	for _, b := range []byte(filepath.Base(path)) {
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-' {
			name.WriteByte(b)
		} else {
			name.WriteByte('_')
		}
	}

	if name.Len() == 0 {
		name.WriteString("project")
	}

	hash := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%s-%x", name.String(), hash[:8])
}

type tmux struct {
	run func(...string) (string, error)
}

func NewTmux() tmux {
	return tmux{run: func(args ...string) (string, error) {
		cmd := exec.Command("tmux", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()

		if err != nil {
			return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}

		if stderr.Len() > 0 {
			fmt.Fprint(os.Stderr, stderr.String())
		}

		return strings.TrimSpace(string(output)), nil
	}}
}

func (t tmux) Sessions() (map[string]bool, error) {
	// Distinguish an absent server from other failures without hiding errors.
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").CombinedOutput()

	if err != nil {
		message := string(out)

		if strings.Contains(message, "no server running") || strings.Contains(message, "No such file or directory") {
			return map[string]bool{}, nil
		}

		return nil, fmt.Errorf("list tmux sessions: %w: %s", err, strings.TrimSpace(message))
	}

	result := map[string]bool{}

	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		result[name] = true
	}

	return result, nil
}

func (t tmux) ensure(p config.Project) (string, error) {
	if _, err := config.ExpandPath(p.Path); err != nil {
		return "", err
	}

	name := SessionName(p.Path)

	if _, err := t.run("has-session", "-t", "="+name); err == nil {
		return name, nil
	}

	windows := p.Windows

	if len(windows) == 0 {
		windows = []config.Window{{Name: "terminal"}}
	}

	// One directory per pane, in the order of windowPanes.
	dirs := make([][]string, len(windows))

	for i, w := range windows {
		if strings.ContainsRune(w.Name+w.Command+w.Directory, 0) {
			return "", fmt.Errorf("window settings contain a NUL character")
		}

		if strings.TrimSpace(w.Name) == "" || !config.ValidWindowName.MatchString(w.Name) {
			return "", fmt.Errorf("invalid window name %q: use ASCII letters, digits, spaces, _, - or .", w.Name)
		}

		if w.Layout != "" && !config.ValidLayout(w.Layout) {
			return "", fmt.Errorf("window %s: unknown layout %q", w.Name, w.Layout)
		}

		// Expand "~" first, so pane directories with ".." climb from the real path.
		windowDir, err := config.ExpandPath(joinDirectory(p.Path, w.Directory))

		if err != nil {
			return "", fmt.Errorf("window %s: %w", w.Name, err)
		}

		for _, pane := range windowPanes(w) {
			if strings.ContainsRune(pane.Command+pane.Directory, 0) {
				return "", fmt.Errorf("pane settings contain a NUL character")
			}

			resolved, err := config.CanonicalPath(joinDirectory(windowDir, pane.Directory))

			if err != nil {
				return "", fmt.Errorf("window %s: %w", w.Name, err)
			}

			dirs[i] = append(dirs[i], resolved)
		}
	}

	// Start a placeholder first so remain-on-exit is set before a fast command finishes.
	out, err := t.run(
		"new-session", "-d", "-P",
		"-F", "#{session_id} #{window_id}",
		"-s", name,
		"-n", windows[0].Name,
		"-c", dirs[0][0],
		"sleep 2147483647",
	)

	if err != nil {
		return "", err
	}

	ids := strings.Fields(out)

	if len(ids) != 2 {
		return "", fmt.Errorf("unexpected tmux session identifiers: %q", out)
	}

	session, first := ids[0], ids[1]
	cleanup := func(err error) (string, error) {
		if _, cleanupErr := t.run("kill-session", "-t", session); cleanupErr != nil {
			return "", fmt.Errorf("%w (cleanup failed: %v)", err, cleanupErr)
		}

		return "", err
	}
	shell, err := t.run("show-options", "-gv", "default-shell")

	if err != nil {
		return cleanup(err)
	}

	defaultCommand, err := t.run("show-options", "-gv", "default-command")

	if err != nil {
		return cleanup(err)
	}

	startCommand := func(command string) []string {
		if command != "" {
			return []string{command}
		}

		if defaultCommand != "" {
			return []string{defaultCommand}
		}

		return []string{shell, "-l"}
	}

	for i, w := range windows {
		id := first

		if i > 0 {
			id, err = t.run(
				"new-window", "-d", "-P",
				"-F", "#{window_id}",
				"-t", session+":",
				"-n", w.Name,
				"-c", dirs[i][0],
				"sleep 2147483647",
			)

			if err != nil {
				return cleanup(err)
			}
		}

		if _, err = t.run("set-option", "-w", "-t", id, "remain-on-exit", "on"); err != nil {
			return cleanup(err)
		}

		panes := windowPanes(w)
		args := []string{"respawn-window", "-k", "-t", id, "-c", dirs[i][0], "--"}

		if _, err = t.run(append(args, startCommand(panes[0].Command)...)...); err != nil {
			return cleanup(err)
		}

		// Splitting the newest pane keeps the configured order. New panes inherit
		// remain-on-exit from the window, so they start their commands directly.
		target := id

		for j := 1; j < len(panes); j++ {
			args := []string{
				"split-window", "-d", "-P",
				"-F", "#{pane_id}",
				"-t", target,
				"-c", dirs[i][j],
				"--",
			}
			target, err = t.run(append(args, startCommand(panes[j].Command)...)...)

			if err != nil {
				return cleanup(err)
			}

			// Rebalance after each split, because repeatedly halving one pane quickly runs out of space.
			if _, err = t.run("select-layout", "-t", id, "tiled"); err != nil {
				return cleanup(err)
			}
		}

		if w.Layout != "" {
			if _, err = t.run("select-layout", "-t", id, w.Layout); err != nil {
				return cleanup(err)
			}

			// A detached session starts at default-size, and main-* layouts keep their
			// absolute pane sizes when the window grows. Apply the layout once more on
			// the first resize, so later manual pane resizing is preserved.
			hook := fmt.Sprintf("select-layout -t %s %s ; set-hook -uw -t %s window-resized", id, w.Layout, id)

			if _, err = t.run("set-hook", "-w", "-t", id, "window-resized", hook); err != nil {
				return cleanup(err)
			}
		}
	}

	if _, err = t.run("select-window", "-t", first); err != nil {
		return cleanup(err)
	}

	return name, nil
}

// A window without configured panes has one pane running the window command.
func windowPanes(w config.Window) []config.Pane {
	if len(w.Panes) > 0 {
		return w.Panes
	}

	return []config.Pane{{Command: w.Command}}
}

// Relative directories resolve against base. Home paths are expanded by config.CanonicalPath.
func joinDirectory(base, dir string) string {
	if dir == "" {
		return base
	}

	if filepath.IsAbs(dir) || dir == "~" || strings.HasPrefix(dir, "~/") {
		return dir
	}

	return filepath.Join(base, dir)
}

func OpenProject(p config.Project) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux is required: %w", err)
	}

	name, err := NewTmux().ensure(p)

	if err != nil {
		return err
	}

	action := "attach-session"

	if os.Getenv("TMUX") != "" {
		action = "switch-client"
	}

	cmd := exec.Command("tmux", action, "-t", "="+name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
