package syncer

import (
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func browseLocal(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", problem("Invalid local folder")
	}
	allowed := false
	for _, root := range []string{"/home", "/srv", "/mnt", "/media"} {
		if path == root || strings.HasPrefix(path, root+"/") {
			allowed = true
		}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if !allowed || err != nil || resolved != path {
		return "", problem("Choose an accessible local folder without symlinks")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || unix.Access(path, unix.R_OK|unix.X_OK) != nil {
		return "", problem("Local folder is unavailable")
	}
	return path, nil
}

func (e *Engine) localFolders(path string) (any, error) {
	rows := []map[string]string{}
	if path == "" {
		roots, err := e.localRoots()
		if err != nil {
			return nil, err
		}
		return map[string]any{"folders": rows, "roots": roots, "selectable": false}, nil
	}
	path, err := browseLocal(path)
	if err != nil {
		return nil, err
	}
	if _, err = e.Mount(path); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, problem("Local folder is unavailable")
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		child := filepath.Join(path, entry.Name())
		if _, err := browseLocal(child); err != nil {
			continue
		}
		if _, err := e.Mount(child); err != nil {
			continue
		}
		rows = append(rows, map[string]string{"name": entry.Name(), "path": child})
	}
	_, err = e.Local(path)
	return map[string]any{"folders": rows, "selectable": err == nil}, nil
}

func (e *Engine) createLocalFolder(path, name string) (any, error) {
	parent, err := e.Local(path)
	if err != nil {
		return nil, err
	}
	if _, err = e.Mount(parent); err != nil {
		return nil, err
	}
	if name == "" || name != strings.TrimSpace(name) || name == "." || name == ".." || len(name) > 255 || strings.ContainsAny(name, "/\\\x00") {
		return nil, problem("Invalid folder name")
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return nil, problem("Invalid folder name")
		}
	}
	// Opening each component without following links keeps creation inside the selected directory.
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(parent, "/"), "/") {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return nil, problem("Local folder is unavailable")
		}
		fd = next
	}
	defer unix.Close(fd)
	if err = unix.Mkdirat(fd, name, 0777); err != nil {
		return nil, problem("Cannot create folder. Check permissions and whether the name already exists.")
	}
	return map[string]string{"path": filepath.Join(parent, name)}, nil
}

type FolderRoot struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

func volumeRoots(raw string) []FolderRoot {
	byVolume := map[string]FolderRoot{}
	for _, line := range strings.Split(raw, "\n") {
		halves := strings.SplitN(line, " - ", 2)
		if len(halves) != 2 {
			continue
		}
		l, r := strings.Fields(halves[0]), strings.Fields(halves[1])
		if len(l) < 5 || len(r) < 2 {
			continue
		}
		path, source := unescape(l[4]), unescape(r[1])
		if !strings.HasPrefix(source, "/dev/") {
			continue
		}
		name := filepath.Base(source) + " · " + r[0]
		reason := ""
		if path == "/" {
			reason = "Select your home folder or a data volume for synchronization"
		} else if _, err := browseLocal(path); err != nil {
			continue
		}
		if reason == "" {
			if _, err := mountFrom(line, path); err != nil {
				reason = err.Error()
			}
		}
		root := FolderRoot{Name: name, Path: path, Kind: "volume", Reason: reason}
		key := l[2] + ":" + l[3]
		previous, ok := byVolume[key]
		if !ok || len(path) < len(previous.Path) {
			byVolume[key] = root
		}
	}
	roots := []FolderRoot{}
	for _, root := range byVolume {
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Path < roots[j].Path })
	return roots
}

func (e *Engine) localRoots() ([]FolderRoot, error) {
	roots := []FolderRoot{}
	account, err := user.Lookup(e.Owner)
	if err != nil {
		return nil, problem("Local folder is unavailable")
	}
	if home, err := browseLocal(account.HomeDir); err == nil {
		reason := ""
		if _, err := e.Mount(home); err != nil {
			reason = err.Error()
		}
		roots = append(roots, FolderRoot{Name: "home", Path: home, Kind: "home", Reason: reason})
	}
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	volumes := volumeRoots(string(raw))
	names := localMountNames()
	for i := range volumes {
		if name := names[volumes[i].Path]; name != "" {
			volumes[i].Name = name
		}
	}
	return append(roots, volumes...), nil
}

type namedBlock struct {
	Name        string       `json:"name"`
	Path        string       `json:"path"`
	Type        string       `json:"type"`
	Model       string       `json:"model"`
	Label       string       `json:"label"`
	Mountpoints []string     `json:"mountpoints"`
	Children    []namedBlock `json:"children"`
}

func blockMountNames(blocks []namedBlock, aliases map[string]string) map[string]string {
	names := map[string]string{}
	var visit func(namedBlock, string, bool)
	visit = func(block namedBlock, model string, multiple bool) {
		if strings.HasPrefix(block.Type, "raid") {
			multiple = false
			path := block.Path
			if path == "" {
				path = "/dev/" + block.Name
			}
			model = aliases[path]
			if model == "" {
				model = block.Name
			}
		} else if strings.TrimSpace(block.Model) != "" {
			model = strings.TrimSpace(block.Model)
		}
		label := strings.TrimSpace(block.Label)
		name := model
		if name == "" {
			name = label
		}
		if name == "" {
			name = block.Name
		}
		if model != "" && multiple {
			suffix := label
			if suffix == "" {
				suffix = block.Name
			}
			name += " · " + suffix
		}
		for _, point := range block.Mountpoints {
			if point != "" {
				names[point] = name
			}
		}
		for _, child := range block.Children {
			visit(child, model, len(block.Children) > 1)
		}
	}
	for _, block := range blocks {
		visit(block, "", false)
	}
	return names
}
func localMountNames() map[string]string {
	aliases := map[string]string{}
	paths, _ := filepath.Glob("/dev/md/*")
	for i := len(paths) - 1; i >= 0; i-- {
		if target, err := filepath.EvalSymlinks(paths[i]); err == nil {
			aliases[target] = filepath.Base(paths[i])
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "lsblk", "--json", "--output", "NAME,PATH,TYPE,MODEL,LABEL,MOUNTPOINTS").Output()
	if err != nil {
		return nil
	}
	var data struct {
		Blocks []namedBlock `json:"blockdevices"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return nil
	}
	return blockMountNames(data.Blocks, aliases)
}
