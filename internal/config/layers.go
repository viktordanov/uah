package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

// LayerDir holds configuration layers: every *.toml in it merges after the
// user file, in lexical order.
func LayerDir() string { return filepath.Join(Dir(), "config.d") }

// ExtraLayerName names the layer that UAH_EXTRA_CONFIG points at.
const ExtraLayerName = home.EnvExtraConfig

// Load reads the user file at userPath, the configuration layers, and, when
// the workspace is trusted, its project file, each over the one before. It
// returns the files it read. Missing files are not errors, except a file
// that UAH_EXTRA_CONFIG names.
func Load(userPath, workspace string) (Config, []string, error) {
	l, err := LoadLayers(userPath, workspace)
	if err != nil {
		return Config{}, nil, err
	}

	return l.Merged(), l.Files(), nil
}

// Layers are the configuration files for a workspace as read, before they
// are merged.
type Layers struct {
	User Config
	// Extra are the layers between the user file and the project file, in
	// merge order: config.d, then UAH_EXTRA_CONFIG.
	Extra   []Layer
	Project Config
	// UserFile and ProjectFile are the paths read ("" when not read).
	UserFile    string
	ProjectFile string
	// Trusted reports whether the user file and the layers trust the
	// workspace.
	Trusted bool
}

// Layer is one configuration file between the user file and the project
// file. It merges as a project file does and may set [projects].
type Layer struct {
	// Name is how `uah config` names it: config.d/<file>, or
	// UAH_EXTRA_CONFIG.
	Name   string
	Path   string
	Config Config
}

// Merged is the user file with each layer, then the project file, over it.
func (l Layers) Merged() Config {
	c := l.User
	for _, x := range l.Extra {
		c = merge(c, x.Config)
	}

	return merge(c, l.Project)
}

// Files are the files read, in merge order.
func (l Layers) Files() []string {
	var files []string
	if l.UserFile != "" {
		files = append(files, l.UserFile)
	}
	for _, x := range l.Extra {
		files = append(files, x.Path)
	}
	if l.ProjectFile != "" {
		files = append(files, l.ProjectFile)
	}

	return files
}

// LoadLayers reads the user file at userPath, the configuration layers, and,
// when those trust the workspace, the workspace's project file.
func LoadLayers(userPath, workspace string) (Layers, error) {
	var l Layers
	found, err := decode(userPath, &l.User)
	if err != nil {
		return Layers{}, err
	}
	if found {
		l.UserFile = userPath
	}
	tagHooks(l.User.Hooks, hooks.SourceUser)
	if l.Extra, err = loadExtra(); err != nil {
		return Layers{}, err
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return Layers{}, fmt.Errorf("failed to resolve workspace: %w", err)
	}
	projects := l.User.Projects
	for _, x := range l.Extra {
		projects = mergeMap(projects, x.Config.Projects, func(_, b Project) Project { return b })
	}
	if l.Trusted = projects[abs].Trusted; !l.Trusted {
		return l, nil
	}
	path := ProjectFile(abs)
	if found, err = decode(path, &l.Project); err != nil {
		return Layers{}, err
	}
	if !found {
		return l, nil
	}
	if len(l.Project.Projects) > 0 {
		return Layers{}, fmt.Errorf("%s: [projects] belongs in the user file or a layer only", path)
	}
	tagHooks(l.Project.Hooks, hooks.SourceProject)
	l.ProjectFile = path

	return l, nil
}

// loadExtra reads every *.toml in LayerDir, in lexical order, then the file
// UAH_EXTRA_CONFIG names, which must exist. Their hooks run as written.
func loadExtra() ([]Layer, error) {
	paths, err := filepath.Glob(filepath.Join(LayerDir(), "*.toml"))
	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", LayerDir(), err)
	}
	slices.Sort(paths)
	var out []Layer
	for _, path := range paths {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue
		}
		out = append(out, Layer{Name: "config.d/" + filepath.Base(path), Path: path})
	}
	if extra := strings.TrimSpace(os.Getenv(home.EnvExtraConfig)); extra != "" {
		abs, err := filepath.Abs(extra)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve %s: %w", home.EnvExtraConfig, err)
		}
		out = append(out, Layer{Name: ExtraLayerName, Path: abs})
	}
	for i := range out {
		x := &out[i]
		found, err := decode(x.Path, &x.Config)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("%s names %s, which does not exist", home.EnvExtraConfig, x.Path)
		}
		tagHooks(x.Config.Hooks, hooks.Source(x.Name))
	}

	return out, nil
}

func decode(path string, into *Config) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to read %s: %w", path, err)
	}

	if err := decodeBytes(path, data, into); err != nil {
		return true, err
	}
	if err := validateTools(path, into.Tools); err != nil {
		return true, err
	}
	if v := into.TUI.FileLinks; v != "" && !slices.Contains(FileLinksModes, v) {
		return true, fmt.Errorf("%s: [tui] file_links is %q; want one of %s", path, v, strings.Join(FileLinksModes, ", "))
	}

	return true, resolvePaths(into, filepath.Dir(path))
}

// validateTools checks the names in [tools] allow and deny.
func validateTools(path string, t Tools) error {
	var names []string
	if t.Allow != nil {
		names = *t.Allow
	}
	if err := toolpolicy.Validate(append(slices.Clone(names), t.Deny...)); err != nil {
		return fmt.Errorf("%s: [tools]: %w", path, err)
	}

	return nil
}

func tagHooks(byEvent map[string][]Hook, source hooks.Source) {
	for event, list := range byEvent {
		for i := range list {
			list[i].Source = source
		}
		byEvent[event] = list
	}
}
