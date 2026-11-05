package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/pr0sp3ro/dirhaven/internal/config"
)

func TestCommandsRejectEmptyPathsWithoutChangingConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := config.ConfigPath()

	if err != nil {
		t.Fatal(err)
	}

	c := config.Config{
		Sources:  []config.Source{{Path: root}},
		Projects: map[string]config.Project{"demo": {Path: root}},
	}

	if err := config.SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []string{"", "  "} {
		for _, args := range [][]string{
			{"source", "add", value},
			{"source", "remove", value},
			{"project", "add", "other", value},
			{"config", "set", "projects.demo.path", value},
			{"config", "set", "sources.0.path", value},
		} {
			if err := Run(args); err == nil {
				t.Fatalf("accepted an empty path: %q", args)
			}

			after, err := os.ReadFile(path)

			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected command changed configuration: %q, %v", args, err)
			}
		}
	}
}

func TestConfigEditRejectsDuplicateCanonicalPaths(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("VISUAL", `sh -c 'cat "$DIRHAVEN_TEST_EDIT_CONTENT" > "$1"' dirhaven-test`)
	path, err := config.ConfigPath()

	if err != nil {
		t.Fatal(err)
	}

	projectPath := t.TempDir()
	alias := filepath.Join(root, "alias")

	if err := os.Symlink(projectPath, alias); err != nil {
		t.Fatal(err)
	}

	content := filepath.Join(root, "edited.toml")
	t.Setenv("DIRHAVEN_TEST_EDIT_CONTENT", content)

	for _, duplicate := range []string{projectPath, alias} {
		if err := config.SaveConfig(path, config.Config{}); err != nil {
			t.Fatal(err)
		}

		c := config.Config{Projects: map[string]config.Project{
			"alpha": {Path: projectPath},
			"beta":  {Path: duplicate},
		}}
		data, err := toml.Marshal(c)

		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(content, data, 0600); err != nil {
			t.Fatal(err)
		}

		if err := config.SaveConfig(path, c); err == nil {
			t.Fatal("save accepted duplicate canonical paths")
		}

		if err := Run([]string{"config", "edit"}); err == nil {
			t.Fatal("editor validation accepted duplicate canonical paths")
		}

		if _, err := config.LoadConfig(path); err == nil {
			t.Fatal("load accepted duplicate canonical paths")
		}
	}
}

func TestConfigSetRejectsDuplicateProjectPaths(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	alphaPath := t.TempDir()
	betaPath := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")

	if err := os.Symlink(alphaPath, alias); err != nil {
		t.Fatal(err)
	}

	path, err := config.ConfigPath()

	if err != nil {
		t.Fatal(err)
	}

	c := config.Config{Projects: map[string]config.Project{
		"alpha": {Path: alphaPath},
		"beta":  {Path: betaPath},
	}}

	if err := config.SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path)

	if err != nil {
		t.Fatal(err)
	}

	for _, duplicate := range []string{alphaPath, alias} {
		if err := Run([]string{"config", "set", "projects.beta.path", duplicate}); err == nil {
			t.Fatalf("accepted duplicate path: %s", duplicate)
		}

		after, err := os.ReadFile(path)

		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("rejected update changed configuration: %v", err)
		}
	}

	if err := Run([]string{"config", "set", "projects.beta.path", betaPath}); err != nil {
		t.Fatalf("rejected project's own path: %v", err)
	}

	newPath := t.TempDir()

	if err := Run([]string{"config", "set", "projects.beta.path", newPath}); err != nil {
		t.Fatalf("rejected unique path: %v", err)
	}

	loaded, err := config.LoadConfig(path)

	if err != nil || loaded.Projects["beta"].Path != newPath {
		t.Fatalf("unique path was not saved: %v, %v", loaded, err)
	}
}

func TestConfigEditCanRepairInvalidConfiguration(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("VISUAL", `sh -c 'printf "sources = []\n" > "$1"' dirhaven-test`)

	path, err := config.ConfigPath()

	if err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("invalid = ["), 0600); err != nil {
		t.Fatal(err)
	}

	if err := Run([]string{"config", "edit"}); err != nil {
		t.Fatalf("could not repair configuration: %v", err)
	}

	if _, err := config.LoadConfig(path); err != nil {
		t.Fatalf("configuration was not repaired: %v", err)
	}

	t.Setenv("VISUAL", `sh -c 'printf "invalid = [" > "$1"' dirhaven-test`)

	if err := Run([]string{"config", "edit"}); err == nil {
		t.Fatal("invalid configuration was accepted after editing")
	}
}

func TestConfigEditPreservesSymlinkWithReplacingEditor(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, args := range [][]string{{"config", "edit"}, {"project", "edit", "demo"}} {
		t.Run(args[0], func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", root)
			t.Setenv("VISUAL", `sh -c 'printf "sources = []\n" > "$1.new" && mv "$1.new" "$1"' dirhaven-test`)

			path, err := config.ConfigPath()

			if err != nil {
				t.Fatal(err)
			}

			target := filepath.Join(root, "target.toml")
			c := config.Config{Projects: map[string]config.Project{"demo": {Path: root}}}

			if err := config.SaveConfig(target, c); err != nil {
				t.Fatal(err)
			}

			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}

			linkTarget := filepath.Join("..", "target.toml")

			if err := os.Symlink(linkTarget, path); err != nil {
				t.Fatal(err)
			}

			if err := Run(args); err != nil {
				t.Fatal(err)
			}

			actual, err := os.Readlink(path)

			if err != nil || actual != linkTarget {
				t.Fatalf("editor replaced the symlink: %q, %v", actual, err)
			}

			data, err := os.ReadFile(target)

			if err != nil || string(data) != "sources = []\n" {
				t.Fatalf("editor did not update the target: %q, %v", data, err)
			}
		})
	}
}

func TestConcurrentConfigUpdatesPreserveSettings(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const count = 8
	paths := make([]string, count)
	start := make(chan struct{})
	errors := make(chan error, count)

	for i := range paths {
		paths[i] = t.TempDir()

		go func(i int) {
			<-start
			errors <- Run([]string{"project", "add", fmt.Sprintf("project_%d", i), paths[i]})
		}(i)
	}

	close(start)

	for range paths {
		if err := <-errors; err != nil {
			t.Error(err)
		}
	}

	path, err := config.ConfigPath()

	if err != nil {
		t.Fatal(err)
	}

	c, err := config.LoadConfig(path)

	if err != nil || len(c.Projects) != count {
		t.Fatalf("concurrent writes lost projects: %v, %v", c, err)
	}

	// An even number of concurrent toggles must leave the favorite disabled.
	start = make(chan struct{})

	for range paths {
		go func() {
			<-start
			_, err := config.ToggleFavorite(path, paths[0])
			errors <- err
		}()
	}

	close(start)

	for range paths {
		if err := <-errors; err != nil {
			t.Error(err)
		}
	}

	c, err = config.LoadConfig(path)

	if err != nil || len(c.Projects) != count || c.Projects["project_0"].Favorite {
		t.Fatalf("concurrent toggles lost settings: %v, %v", c, err)
	}
}
