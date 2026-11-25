package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/pr0sp3ro/dirhaven/internal/config"
)

func TestSessionIdentity(t *testing.T) {
	first := SessionName("/one/my project")

	if !regexp.MustCompile(`^my_project-[a-f0-9]{16}$`).MatchString(first) {
		t.Fatalf("unexpected name: %s", first)
	}

	if first != SessionName("/one/my project") || first == SessionName("/two/my project") {
		t.Fatal("session identity is not path-specific and stable")
	}
}

func TestExistingSessionDoesNotInitializeAgain(t *testing.T) {
	var calls [][]string
	p := config.Project{Path: "/missing/project", Windows: []config.Window{{Name: "editor", Command: "nvim"}}}
	client := tmux{run: func(args ...string) (string, error) { calls = append(calls, args); return "", nil }}
	name, err := client.ensure(p)

	if err != nil || name != SessionName(p.Path) {
		t.Fatalf("ensure: %s, %v", name, err)
	}

	want := [][]string{{"has-session", "-t", "=" + name}}

	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("existing session was modified: %v", calls)
	}
}

func TestCreationFailureOnlyCleansUpOwnedSession(t *testing.T) {
	for _, created := range []bool{false, true} {
		t.Run(map[bool]string{false: "creation rejected", true: "window setup failed"}[created], func(t *testing.T) {
			var calls [][]string
			client := tmux{run: func(args ...string) (string, error) {
				calls = append(calls, args)

				switch args[0] {
				case "has-session":
					return "", errors.New("absent")

				case "new-session":
					if created {
						return "$42 @7", nil
					}

					return "", errors.New("already exists")

				case "show-options":
					return "/bin/sh", nil

				case "set-option":
					return "", errors.New("setup failed")

				default:
					return "", nil
				}

			}}

			if _, err := client.ensure(config.Project{Path: t.TempDir()}); err == nil {
				t.Fatal("expected failure")
			}

			killed := false

			for _, call := range calls {
				if call[0] == "kill-session" {
					killed = true

					if !reflect.DeepEqual(call, []string{"kill-session", "-t", "$42"}) {
						t.Fatalf("wrong cleanup: %v", call)
					}
				}
			}

			if killed != created {
				t.Fatalf("cleanup ownership mismatch: %v", calls)
			}
		})
	}
}

func TestStartupRetainsFastCommandsAndSelectsFirstWindow(t *testing.T) {
	dir := t.TempDir()
	windowDir := filepath.Join(dir, "~cache")

	if err := os.Mkdir(windowDir, 0700); err != nil {
		t.Fatal(err)
	}

	var calls [][]string
	client := tmux{run: func(args ...string) (string, error) {
		calls = append(calls, args)

		switch args[0] {
		case "has-session":
			return "", errors.New("absent")

		case "new-session":
			return "$42 @7", nil

		case "new-window":
			return "@8", nil

		case "show-options":
			if args[2] == "default-shell" {
				return "/bin/sh", nil
			}
		}

		return "", nil
	}}
	p := config.Project{Path: dir, Windows: []config.Window{
		{Name: "output", Command: "printf done", Directory: "~cache"},
		{Name: "shell"},
	}}

	if _, err := client.ensure(p); err != nil {
		t.Fatal(err)
	}

	retained := map[string]bool{}
	commands := [][]string{}

	for _, call := range calls {
		if call[0] == "set-option" {
			retained[call[3]] = true
		}

		if call[0] == "respawn-window" {
			if !retained[call[3]] {
				t.Fatalf("command starts before remain-on-exit: %v", call)
			}

			commands = append(commands, call[7:])

			if call[3] == "@7" && call[5] != windowDir {
				t.Fatalf("relative window directory resolved outside the project: %s", call[5])
			}
		}
	}

	if !reflect.DeepEqual(commands, [][]string{{"printf done"}, {"/bin/sh", "-l"}}) {
		t.Fatalf("unexpected commands: %v", commands)
	}

	if !reflect.DeepEqual(calls[len(calls)-1], []string{"select-window", "-t", "@7"}) {
		t.Fatal("first window not selected")
	}
}

func TestPanesKeepOrderDirectoriesAndLayout(t *testing.T) {
	dir := t.TempDir()

	for _, sub := range []string{"web", filepath.Join("web", "src")} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}

	var calls [][]string
	panes := 0
	client := tmux{run: func(args ...string) (string, error) {
		calls = append(calls, args)

		switch args[0] {
		case "has-session":
			return "", errors.New("absent")

		case "new-session":
			return "$42 @7", nil

		case "split-window":
			panes++
			return "%" + strconv.Itoa(panes), nil

		case "show-options":
			if args[2] == "default-shell" {
				return "/bin/sh", nil
			}
		}

		return "", nil
	}}
	p := config.Project{Path: dir, Windows: []config.Window{{
		Name:      "dev",
		Directory: "web",
		Layout:    "main-vertical",
		Panes: []config.Pane{
			{Command: "nvim"},
			{Command: "npm run dev", Directory: "src"},
			{},
		},
	}}}

	if _, err := client.ensure(p); err != nil {
		t.Fatal(err)
	}

	web := filepath.Join(dir, "web")
	want := [][]string{
		{"respawn-window", "-k", "-t", "@7", "-c", web, "--", "nvim"},
		{"split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "@7", "-c", filepath.Join(web, "src"), "--", "npm run dev"},
		{"select-layout", "-t", "@7", "tiled"},
		{"split-window", "-d", "-P", "-F", "#{pane_id}", "-t", "%1", "-c", web, "--", "/bin/sh", "-l"},
		{"select-layout", "-t", "@7", "tiled"},
		{"select-layout", "-t", "@7", "main-vertical"},
		{"set-hook", "-w", "-t", "@7", "window-resized", "select-layout -t @7 main-vertical ; set-hook -uw -t @7 window-resized"},
		{"select-window", "-t", "@7"},
	}

	if got := calls[len(calls)-len(want):]; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected pane setup:\n%v", got)
	}
}

func TestPaneDirectoryResolvesAgainstExpandedHomeWindowDirectory(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	shared := filepath.Join(dir, "shared")

	for _, path := range []string{home, shared} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("HOME", home)
	var started string
	client := tmux{run: func(args ...string) (string, error) {
		switch args[0] {
		case "has-session":
			return "", errors.New("absent")

		case "new-session":
			return "$42 @7", nil

		case "respawn-window":
			started = args[5]
		}

		return "", nil
	}}
	p := config.Project{Path: dir, Windows: []config.Window{{
		Name:      "shell",
		Directory: "~",
		Panes:     []config.Pane{{Directory: "../shared"}},
	}}}

	if _, err := client.ensure(p); err != nil || started != shared {
		t.Fatalf("pane directory resolved to %q: %v", started, err)
	}
}

func TestTmuxRejectsUnsafeWindowSettingsBeforeCreation(t *testing.T) {
	for _, window := range []config.Window{
		{Name: "editor;"},
		{Name: "editor[1]"},
		{Name: "editor\nname"},
		{Name: "editor", Directory: "bad;"},
		{Name: "editor", Layout: "main-vertial"},
		{Name: "editor", Panes: []config.Pane{{}, {Directory: "bad;"}}},
	} {
		client := tmux{run: func(args ...string) (string, error) {
			if args[0] != "has-session" {
				t.Fatalf("invalid settings reached tmux initialization: %v", args)
			}

			return "", errors.New("absent")
		}}

		if _, err := client.ensure(config.Project{Path: t.TempDir(), Windows: []config.Window{window}}); err == nil {
			t.Fatalf("accepted unsafe settings: %v", window)
		}
	}
}
