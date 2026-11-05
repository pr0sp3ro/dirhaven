package cli

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/pelletier/go-toml/v2"
	"github.com/pr0sp3ro/dirhaven/internal/config"
	"github.com/pr0sp3ro/dirhaven/internal/tmux"
)

var version = "dev"

const usage = `Dirhaven - open projects in tmux

Usage:
  dirhaven
  dirhaven open <name|path>
  dirhaven list
  dirhaven source add|remove <path>
  dirhaven project add <name> <path>
  dirhaven project show|edit <name>
  dirhaven favorite add|remove <name|path>
  dirhaven config get [key]
  dirhaven config set <key> <value>
  dirhaven config edit
  dirhaven -h|--help
  dirhaven --version

Selector: Enter opens, Ctrl-F toggles favorite, Esc cancels.
Config: $XDG_CONFIG_HOME/dirhaven/config.toml (default ~/.config/dirhaven/config.toml).
`

func Run(args []string) error {
	if len(args) == 1 {
		switch args[0] {
		case "-h", "--help":
			fmt.Print(usage)
			return nil

		case "--version":
			fmt.Println("dirhaven", version)
			return nil
		}
	}

	path, err := config.ConfigPath()

	if err != nil {
		return err
	}

	writesConfig := false

	if len(args) >= 2 {
		switch args[0] {
		case "source", "favorite":
			writesConfig = len(args) == 3 && (args[1] == "add" || args[1] == "remove")

		case "project":
			writesConfig = (args[1] == "add" && len(args) == 4) || (args[1] == "edit" && len(args) == 3)

		case "config":
			writesConfig = (args[1] == "set" && len(args) == 4) || (args[1] == "edit" && len(args) == 2)
		}

		if writesConfig {
			lock, err := config.LockConfig(path, syscall.LOCK_EX)

			if err != nil {
				return err
			}

			defer lock.Close()
		}
	}

	if len(args) >= 2 && args[0] == "config" && args[1] == "edit" {
		if len(args) != 2 {
			return fmt.Errorf("usage: dirhaven config edit")
		}

		return config.EditConfig(path, config.Config{})
	}

	var c config.Config

	if writesConfig {
		c, err = config.LoadConfig(path)
	} else {
		c, err = config.ReadConfig(path)
	}

	if err != nil {
		return err
	}

	if len(args) == 0 {
		return selectProject(path, c)
	}

	switch args[0] {
	case "open":
		if len(args) != 2 {
			return fmt.Errorf("usage: dirhaven open <name|path>")
		}

		p, err := config.ResolveProject(c, args[1])

		if err != nil {
			return err
		}

		return tmux.OpenProject(p)

	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: dirhaven list")
		}

		list, err := config.Projects(c)

		if err != nil {
			return err
		}

		if len(list) == 0 {
			fmt.Println("No projects found. Use dirhaven source add <path> or dirhaven project add <name> <path>.")
			return nil
		}

		for _, p := range list {
			marker := " "

			if p.Favorite {
				marker = "*"
			}

			fmt.Printf("%s %s\t%s\n", marker, display(p.Name), display(p.Path))
		}

		return nil

	case "source":
		if len(args) != 3 || (args[1] != "add" && args[1] != "remove") {
			return fmt.Errorf("usage: dirhaven source add|remove <path>")
		}

		target, err := config.ExpandPath(args[2])

		if err != nil {
			return err
		}

		if resolved, resolveErr := config.CanonicalPath(args[2]); resolveErr == nil {
			target = resolved
		}

		if args[1] == "add" {
			target, err = config.CanonicalPath(args[2])

			if err != nil {
				return err
			}
		}

		found := false
		sources := make([]config.Source, 0, len(c.Sources))

		for _, s := range c.Sources {
			resolved, err := config.CanonicalPath(s.Path)

			if err != nil {
				resolved, err = config.ExpandPath(s.Path)

				if err != nil {
					return err
				}
			}

			same := resolved == target

			if same {
				found = true
			}

			if !same || args[1] == "add" {
				sources = append(sources, s)
			}
		}

		if args[1] == "add" && !found {
			sources = append(sources, config.Source{Path: target})
		}

		if args[1] == "remove" && !found {
			return fmt.Errorf("source %q not configured", args[2])
		}

		c.Sources = sources
		return config.SaveConfig(path, c)

	case "project":
		if len(args) < 3 {
			return fmt.Errorf("usage: dirhaven project add <name> <path> | show|edit <name>")
		}

		name := args[2]

		switch args[1] {
		case "add":
			if len(args) != 4 {
				return fmt.Errorf("usage: dirhaven project add <name> <path>")
			}

			if !config.ValidName.MatchString(name) {
				return fmt.Errorf("invalid project name: use letters, numbers, underscores or hyphens")
			}

			if _, ok := c.Projects[name]; ok {
				return fmt.Errorf("project %q already configured", name)
			}

			target, err := config.CanonicalPath(args[3])

			if err != nil {
				return err
			}

			c.Projects[name] = config.Project{Path: target}
			return config.SaveConfig(path, c)

		case "show", "edit":
			if len(args) != 3 {
				return fmt.Errorf("usage: dirhaven project show|edit <name>")
			}

			p, ok := c.Projects[name]

			if !ok {
				return fmt.Errorf("project %q not configured, use project add first", name)
			}

			if args[1] == "show" {
				return printTOML(p)
			}

			fmt.Fprintf(os.Stderr, "Editing %s, section [projects.%s]\n", path, name)
			return config.EditConfig(path, c)
		}

	case "favorite":
		if len(args) != 3 || (args[1] != "add" && args[1] != "remove") {
			return fmt.Errorf("usage: dirhaven favorite add|remove <name|path>")
		}

		// Allow removing a favorite even when its directory is no longer available.
		p, ok := c.Projects[args[2]]

		if ok && args[1] == "remove" {
			if _, pathErr := config.CanonicalPath(p.Path); pathErr != nil {
				p.Favorite = false
				c.Projects[args[2]] = p
				return config.SaveConfig(path, c)
			}
		}

		p, err = config.ResolveProject(c, args[2])

		if err != nil {
			return err
		}

		if err = config.Favorite(&c, p, args[1] == "add"); err != nil {
			return err
		}

		return config.SaveConfig(path, c)

	case "config":
		if len(args) < 2 {
			return fmt.Errorf("usage: dirhaven config get [key] | set <key> <value> | edit")
		}

		switch args[1] {
		case "get":
			if len(args) == 2 {
				return printTOML(c)
			}

			if len(args) != 3 {
				return fmt.Errorf("usage: dirhaven config get [key]")
			}

			value, err := config.ConfigValue(c, args[2])

			if err != nil {
				return err
			}

			switch v := value.(type) {
			case string:
				fmt.Println(v)

			case bool:
				fmt.Println(v)

			default:
				return printTOML(map[string]any{"value": value})
			}

			return nil

		case "set":
			if len(args) != 4 {
				return fmt.Errorf("usage: dirhaven config set <key> <value>")
			}

			if err = config.SetConfigValue(&c, args[2], args[3]); err != nil {
				return err
			}

			return config.SaveConfig(path, c)

		}
	}

	return fmt.Errorf("unknown command %q\n%s", strings.Join(args, " "), usage)
}

func printTOML(value any) error {
	data, err := toml.Marshal(value)

	if err != nil {
		return err
	}

	_, err = os.Stdout.Write(data)
	return err
}
