package syncer

import (
	"encoding/binary"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func mountIdentity(path string) (string, error) {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	return mountFrom(string(raw), path)
}
func unescape(s string) string {
	for _, p := range []struct{ a, b string }{{`\040`, " "}, {`\011`, "\t"}, {`\012`, "\n"}, {`\134`, `\`}} {
		s = strings.ReplaceAll(s, p.a, p.b)
	}
	return s
}
func mountFrom(raw, path string) (string, error) {
	best := -1
	var selected []string
	for _, line := range strings.Split(raw, "\n") {
		halves := strings.SplitN(line, " - ", 2)
		if len(halves) != 2 {
			continue
		}
		l, r := strings.Fields(halves[0]), strings.Fields(halves[1])
		if len(l) < 5 || len(r) < 2 {
			continue
		}
		target := unescape(l[4])
		if (path == target || strings.HasPrefix(path, strings.TrimRight(target, "/")+"/")) && len(target) > best {
			best = len(target)
			selected = []string{l[2], l[3], target, r[0], r[1]}
		}
	}
	if best < 0 {
		return "", problem("Local volume is unavailable")
	}
	switch selected[3] {
	case "ext4", "xfs", "btrfs", "ext3", "ext2":
	default:
		return "", problem("Use a local Linux filesystem; network mounts and removable FAT/NTFS are not supported in this prototype.")
	}
	return marshal(selected), nil
}
func sameMount(a, b string) bool {
	var x, y []string
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && strings.Join(x, "\x00") == strings.Join(y, "\x00")
}
func localFolder(value string) (string, error) {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", problem("Select an absolute local folder path")
	}
	allowed := false
	for _, prefix := range []string{"/home/", "/srv/", "/mnt/", "/media/"} {
		allowed = allowed || strings.HasPrefix(value, prefix)
	}
	if !allowed {
		return "", problem("Choose a folder inside /home, /srv, /mnt or /media")
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil || resolved != value {
		return "", problem("Symlink paths are not supported")
	}
	s, err := os.Stat(value)
	if err != nil || !s.IsDir() || unix.Access(value, unix.R_OK|unix.W_OK|unix.X_OK) != nil {
		return "", problem("Local folder must exist and be writable by your Linux user")
	}
	return value, nil
}
func remoteFolder(s string) (string, error) {
	if len(s) > 2048 {
		return "", problem("Invalid cloud folder")
	}
	for _, r := range s {
		if r < 32 {
			return "", problem("Invalid cloud folder")
		}
	}
	s = strings.Trim(s, "/")
	for _, part := range strings.Split(s, "/") {
		if part == "." || part == ".." {
			return "", problem("Invalid cloud folder")
		}
	}
	return s, nil
}
func overlap(a, b string) bool {
	return a == b || a == "" || b == "" || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
func excluded(name string) bool { return strings.HasPrefix(name, ".panasms-cloud-") }

type watchPath struct{ Path, Task string }
type Watcher struct {
	fd      int
	paths   map[int]watchPath
	Rebuild map[string]bool
}

func newWatcher() (*Watcher, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	return &Watcher{fd: fd, paths: map[int]watchPath{}, Rebuild: map[string]bool{}}, err
}
func (w *Watcher) Close() { _ = unix.Close(w.fd) }
func (w *Watcher) Add(path, task string) error {
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return problem("Cannot monitor local files; check folder permissions.")
		}
		if !d.IsDir() {
			return nil
		}
		if p != path && excluded(d.Name()) {
			return filepath.SkipDir
		}
		if len(w.paths) >= 100000 {
			return problem("Prototype directory watch limit reached")
		}
		fd, err := unix.InotifyAddWatch(w.fd, p, unix.IN_MODIFY|unix.IN_ATTRIB|unix.IN_CLOSE_WRITE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_CREATE|unix.IN_DELETE|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_UNMOUNT|unix.IN_ONLYDIR|unix.IN_DONT_FOLLOW)
		if err != nil {
			return problem("Local watch limit reached; increase inotify limits before resuming")
		}
		w.paths[fd] = watchPath{p, task}
		return nil
	})
}
func (w *Watcher) Remove(task string) {
	for wd, p := range w.paths {
		if p.Task == task {
			_, _ = unix.InotifyRmWatch(w.fd, uint32(wd))
			delete(w.paths, wd)
		}
	}
}
func (w *Watcher) Drain() (map[string]bool, error) {
	affected := map[string]bool{}
	buf := make([]byte, 1<<20)
	for {
		n, err := unix.Read(w.fd, buf)
		if err == syscall.EAGAIN {
			return affected, nil
		}
		if err != nil {
			return affected, err
		}
		if n == 0 {
			return affected, nil
		}
		for pos := 0; pos+16 <= n; {
			wd := int(int32(binary.NativeEndian.Uint32(buf[pos:])))
			mask := binary.NativeEndian.Uint32(buf[pos+4:])
			size := int(binary.NativeEndian.Uint32(buf[pos+12:]))
			if pos+16+size > n {
				return affected, problem("Invalid filesystem event")
			}
			name := strings.TrimRight(string(buf[pos+16:pos+16+size]), "\x00")
			pos += 16 + size
			if mask&unix.IN_Q_OVERFLOW != 0 {
				for _, p := range w.paths {
					affected[p.Task] = true
				}
				return affected, problem("Filesystem event queue overflowed. Resume tasks to rebuild monitoring.")
			}
			p, ok := w.paths[wd]
			if !ok || excluded(name) {
				continue
			}
			if mask&unix.IN_IGNORED != 0 {
				delete(w.paths, wd)
				continue
			}
			affected[p.Task] = true
			if mask&unix.IN_ISDIR != 0 && mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 {
				if err = w.Add(filepath.Join(p.Path, name), p.Task); err != nil {
					return affected, err
				}
			}
			if mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_UNMOUNT) != 0 {
				w.Rebuild[p.Task] = true
			}
		}
	}
}
func uidString(uid uint32) string { return strconv.FormatUint(uint64(uid), 10) }
