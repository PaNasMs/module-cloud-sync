package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/PaNasMs/module-sdk/external"
)

const accountID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const mount = `["8:0","/","/srv/test","ext4","/dev/test"]`

func fixture(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	e, err := Open(filepath.Join(dir, "state"), filepath.Join(dir, "runtime"), "owner", func(context.Context, string) (external.Access, error) {
		return external.Access{AccessToken: "temporary-access", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour), Scope: "https://www.googleapis.com/auth/drive"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.Mount = func(string) (string, error) { return mount, nil }
	e.Local = func(s string) (string, error) {
		if _, err := os.Stat(s); err != nil {
			return "", err
		}
		return s, nil
	}
	t.Cleanup(func() { e.Close() })
	_, err = e.DB.Exec("INSERT INTO accounts(id,provider,label,identity,cursor) VALUES(?,?,?,?,?)", accountID, "local", "Test", "owner", "cursor")
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicFile(filepath.Join(e.Root, accountID+".conf"), []byte("[cloud]\ntype = local\n")); err != nil {
		t.Fatal(err)
	}
	return e
}
func addTask(t *testing.T, e *Engine, direction string) Task {
	t.Helper()
	local := filepath.Join(t.TempDir(), "local")
	os.Mkdir(local, 0700)
	task := Task{ID: newID(), Account: accountID, Name: "test", Local: local, Remote: filepath.Join(t.TempDir(), "remote"), Mount: mount, Direction: direction}
	os.Mkdir(task.Remote, 0700)
	_, err := e.DB.Exec("INSERT INTO tasks(id,account,name,local,remote,mount,direction) VALUES(?,?,?,?,?,?,?)", task.ID, task.Account, task.Name, task.Local, task.Remote, task.Mount, task.Direction)
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func TestLegacySchemaAndState(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE accounts(id TEXT PRIMARY KEY,provider TEXT NOT NULL,label TEXT NOT NULL,identity TEXT NOT NULL,cursor TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT ''); INSERT INTO accounts(id,provider,label,identity) VALUES('legacy','drive','Preserve','identity');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	e, err := Open(dir, t.TempDir(), "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s, err := e.State()
	if err != nil || len(s.Accounts) != 1 || s.Accounts[0].Label != "Preserve" {
		t.Fatalf("%+v %v", s, err)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "grant_id") {
		t.Fatal("grant leaked")
	}
}
func TestMountIdentityPreservesPythonEncoding(t *testing.T) {
	raw := "1 0 8:0 / /srv/test rw - ext4 /dev/test rw\n"
	m, err := mountFrom(raw, "/srv/test/folder")
	if err != nil || !sameMount(m, `["8:0", "/", "/srv/test", "ext4", "/dev/test"]`) {
		t.Fatal(m, err)
	}
	if _, err = mountFrom("1 0 8:0 / /srv/test rw - nfs server:/data rw", "/srv/test"); err == nil {
		t.Fatal("network accepted")
	}
}
func TestVolumeLossStopsBeforeRclone(t *testing.T) {
	e := fixture(t)
	task := addTask(t, e, "upload")
	e.Mount = func(string) (string, error) { return "other", nil }
	e.Runner = func(context.Context, Account, []string, func() error) ([]byte, error) {
		t.Fatal("transfer started")
		return nil, nil
	}
	if err := e.runTask(context.Background(), task); err == nil {
		t.Fatal("missing rejection")
	}
}
func TestInitialBisyncAndInterruptedInitialization(t *testing.T) {
	e := fixture(t)
	task := addTask(t, e, "both")
	os.WriteFile(filepath.Join(task.Local, "keep"), []byte("keep"), 0600)
	e.Runner = func(_ context.Context, _ Account, args []string, _ func() error) ([]byte, error) {
		if args[0] != "lsjson" {
			t.Fatal("unsafe bisync started")
		}
		return []byte(`[{"Path":"existing","IsDir":false}]`), nil
	}
	if err := e.runTask(context.Background(), task); err == nil || !strings.Contains(err.Error(), "one folder") {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(task.Local, "keep"))
	e.Runner = func(_ context.Context, _ Account, args []string, _ func() error) ([]byte, error) {
		if args[0] == "lsjson" {
			return []byte(`[]`), nil
		}
		return nil, errRotated
	}
	if err := e.runTask(context.Background(), task); err == nil || err.Error() != recoveryMessage {
		t.Fatal(err)
	}
	tasks, _ := e.tasks()
	if tasks[0].Initialized != -1 {
		t.Fatal("initialization not persisted")
	}
	if err := e.runTask(context.Background(), tasks[0]); err == nil || err.Error() != recoveryMessage {
		t.Fatal("unsafe retry", err)
	}
}
func TestCopyRotationAndSnapshot(t *testing.T) {
	e := fixture(t)
	task := addTask(t, e, "download")
	calls := 0
	listings := 0
	e.Runner = func(_ context.Context, _ Account, args []string, _ func() error) ([]byte, error) {
		if args[0] == "lsjson" {
			listings++
			return []byte(`[]`), nil
		}
		calls++
		if args[0] != "copy" || !strings.Contains(strings.Join(args, " "), "--backup-dir") {
			t.Fatal(args)
		}
		if calls == 1 {
			return nil, errRotated
		}
		return nil, nil
	}
	if err := e.runTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || listings != 1 {
		t.Fatal(calls, listings)
	}
	s, _ := e.State()
	if s.Tasks[0].Status != "idle" || len(s.History) != 1 {
		t.Fatal(s)
	}
}
func TestTaskRemovalAndRestart(t *testing.T) {
	e := fixture(t)
	task := addTask(t, e, "both")
	e.DB.Exec("UPDATE tasks SET status='running',initialized=-1")
	if _, err := e.taskAction("task.remove", task.ID); err == nil {
		t.Fatal("removed running")
	}
	if _, err := e.taskAction("task.pause", task.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.initialize(); err != nil {
		t.Fatal(err)
	}
	tasks, _ := e.tasks()
	if tasks[0].Paused != 1 || tasks[0].Status != "error" || tasks[0].Initialized != -1 {
		t.Fatal(tasks)
	}
}
func TestGrantRechecksAndRevocationErasesRuntime(t *testing.T) {
	e := fixture(t)
	a := Account{ID: accountID, Provider: "drive", Grant: strings.Repeat("f", 32), Owner: "owner"}
	calls := 0
	e.Broker = func(context.Context, string) (external.Access, error) {
		calls++
		if calls > 1 {
			return external.Access{}, &external.Error{Status: 403, Code: "denied"}
		}
		return external.Access{AccessToken: "secret-access", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	if _, err := e.ensure(context.Background(), a, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(e.config(a))
	if err != nil || strings.Contains(string(b), "refresh_token") || !strings.Contains(e.config(a), e.Runtime) {
		t.Fatal(string(b), err)
	}
	l := e.tokens[a.ID]
	l.Checked = time.Now().Add(-31 * time.Second)
	e.tokens[a.ID] = l
	if _, err = e.ensure(context.Background(), a, false); err == nil {
		t.Fatal("cached authorization bypassed")
	}
	if _, err = os.Stat(e.config(a)); !os.IsNotExist(err) {
		t.Fatal("runtime credentials retained")
	}
}

type doFunc func(*http.Request) (*http.Response, error)

func (f doFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }
func TestCloudCursorFailureDoesNotAdvance(t *testing.T) {
	e := fixture(t)
	a := Account{ID: accountID, Provider: "drive", Grant: strings.Repeat("f", 32), Owner: "owner", Cursor: "original"}
	count := 0
	e.HTTP = doFunc(func(*http.Request) (*http.Response, error) {
		count++
		if count > 1 {
			return nil, errors.New("offline")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"changes":[{}],"nextPageToken":"next"}`))}, nil
	})
	if _, _, err := e.poll(context.Background(), a); err == nil {
		t.Fatal("failure hidden")
	}
	row, _ := e.account(accountID)
	if row.Cursor != "cursor" {
		t.Fatal(row)
	}
}
func TestFailedReconnectPreservesConfig(t *testing.T) {
	e := fixture(t)
	e.DB.Exec("UPDATE accounts SET provider='drive',grant_id=?,owner='owner'", strings.Repeat("a", 32))
	a, _ := e.account(accountID)
	e.ensure(context.Background(), a, true)
	old, _ := os.ReadFile(e.config(a))
	e.HTTP = doFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"user":{"permissionId":"different"}}`))}, nil
	})
	_, err := e.saveAccount(context.Background(), map[string]any{"id": accountID, "provider": "drive", "grantId": strings.Repeat("b", 32)})
	if err == nil {
		t.Fatal("identity replacement accepted")
	}
	current, _ := e.account(accountID)
	data, _ := os.ReadFile(e.config(a))
	if current.Grant != a.Grant || string(old) != string(data) {
		t.Fatal("previous consent corrupted")
	}
}
func TestWatcherIncludesNewSubdirectories(t *testing.T) {
	w, err := newWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	root := t.TempDir()
	if err = w.Add(root, "task"); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	os.Mkdir(sub, 0700)
	if _, err = w.Drain(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sub, "file"), []byte("x"), 0600)
	affected, err := w.Drain()
	if err != nil || !affected["task"] {
		t.Fatal(affected, err)
	}
	affected, err = w.Drain()
	if err != nil || len(affected) != 0 {
		t.Fatal("idle watcher activity")
	}
}
func TestRevocationAlwaysReapsRclone(t *testing.T) {
	e := fixture(t)
	bin := t.TempDir()
	pidfile := filepath.Join(bin, "pid")
	script := "#!/bin/sh\necho $$ > '" + pidfile + "'\nexec sleep 120\n"
	os.WriteFile(filepath.Join(bin, "rclone"), []byte(script), 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	a := Account{ID: accountID, Provider: "drive", Grant: strings.Repeat("f", 32), Owner: "owner"}
	e.Broker = func(context.Context, string) (external.Access, error) {
		return external.Access{}, &external.Error{Status: 403}
	}
	_, err := e.runProcess(context.Background(), a, []string{"copy"}, nil)
	if err == nil {
		t.Fatal("revocation ignored")
	}
	raw, _ := os.ReadFile(pidfile)
	var pid int
	fmt.Sscan(string(raw), &pid)
	if pid == 0 {
		t.Fatal("process never started")
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("rclone leaked")
	}
}
func TestWorkerRPCAndShutdown(t *testing.T) {
	e := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Serve(ctx, func(bool) {}) }()
	path := filepath.Join(e.Root, "worker.sock")
	var conn net.Conn
	var err error
	for range 100 {
		conn, err = net.Dial("unix", path)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	json.NewEncoder(conn).Encode(map[string]string{"mode": "state"})
	var result State
	if err = json.NewDecoder(conn).Decode(&result); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker stuck")
	}
}
func TestRealRcloneLocalTransfers(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone unavailable")
	}
	for _, direction := range []string{"upload", "download", "both"} {
		t.Run(direction, func(t *testing.T) {
			e := fixture(t)
			task := addTask(t, e, direction)
			source, dest := task.Local, task.Remote
			if direction == "download" {
				source, dest = dest, source
			}
			os.WriteFile(filepath.Join(source, "file"), []byte("first"), 0600)
			if direction == "both" {
				for i := range 20 {
					os.WriteFile(filepath.Join(source, fmt.Sprint(i)), []byte("unchanged"), 0600)
				}
			}
			if err := e.runTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(filepath.Join(dest, "file"))
			if string(b) != "first" {
				t.Fatal(string(b))
			}
			task.Initialized = 1
			os.WriteFile(filepath.Join(source, "file"), []byte("second longer"), 0600)
			if err := e.runTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			b, _ = os.ReadFile(filepath.Join(dest, "file"))
			if string(b) != "second longer" {
				t.Fatal(string(b))
			}
			if direction != "both" {
				os.Remove(filepath.Join(source, "file"))
				if err := e.runTask(context.Background(), task); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(dest, "file")); err != nil {
					t.Fatal("deletion propagated")
				}
			}
		})
	}
}
func TestNoDiskScanOnIdleMonitor(t *testing.T) {
	e := fixture(t)
	task := addTask(t, e, "upload")
	var calls atomic.Int32
	e.Local = func(s string) (string, error) { calls.Add(1); return s, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.monitor(ctx); close(done) }()
	time.Sleep(2300 * time.Millisecond)
	cancel()
	<-done
	if calls.Load() != 1 {
		t.Fatal("rescanned idle tree", calls.Load())
	}
	_ = task
}

func TestPausedTaskNeverStarts(t *testing.T) {
	e := fixture(t)
	task := addTask(t, e, "upload")
	e.DB.Exec("UPDATE tasks SET paused=1")
	e.Runner = func(context.Context, Account, []string, func() error) ([]byte, error) {
		t.Fatal("paused task started")
		return nil, nil
	}
	e.cycle(context.Background(), map[string]time.Time{}, map[string]int{}, func(bool) {})
	tasks, _ := e.tasks()
	if tasks[0].ID != task.ID || tasks[0].Paused != 1 {
		t.Fatal(tasks)
	}
}
func TestOverlapPreventsCloudAccess(t *testing.T) {
	e := fixture(t)
	task := addTask(t, e, "upload")
	nested := filepath.Join(task.Local, "nested")
	os.Mkdir(nested, 0700)
	e.Runner = func(context.Context, Account, []string, func() error) ([]byte, error) {
		t.Fatal("cloud called for overlapping task")
		return nil, nil
	}
	_, err := e.Action(context.Background(), map[string]any{"action": "task.create", "account": accountID, "local": nested, "remote": "other", "direction": "upload"})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatal(err)
	}
}
func TestGoDriveConfigNeverFallsBackToLegacyCredentials(t *testing.T) {
	e := fixture(t)
	a := Account{ID: accountID, Provider: "drive", Owner: "owner"}
	e.Runner = func(context.Context, Account, []string, func() error) ([]byte, error) {
		t.Fatal("legacy Drive credentials used")
		return nil, nil
	}
	if _, err := e.token(context.Background(), a); err == nil {
		t.Fatal("ungranted account accepted")
	}
}

func TestFolderCreationRejectsTraversalAndLinks(t *testing.T) {
	e := fixture(t)
	parent := t.TempDir()
	for _, name := range []string{"..", "../outside", "nested/child", "", " bad", "bad\x00name"} {
		if _, err := e.createLocalFolder(parent, name); err == nil {
			t.Fatalf("accepted invalid name %q", name)
		}
	}
	if _, err := e.createLocalFolder(parent, "Documents"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.createLocalFolder(parent, "Documents"); err == nil {
		t.Fatal("accepted existing folder")
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(filepath.Join(parent, "Documents"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := e.createLocalFolder(link, "Escape"); err == nil {
		t.Fatal("followed symlink parent")
	}
	if _, err := os.Stat(filepath.Join(parent, "Documents", "Escape")); !os.IsNotExist(err) {
		t.Fatal("created through symlink")
	}
}
func TestLocalBrowserRejectsSystemAndTraversal(t *testing.T) {
	for _, path := range []string{"/etc", "/", "/srv/../etc", "relative"} {
		if _, err := browseLocal(path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}

func TestVolumeRootsUseMountsAndDeduplicateAliases(t *testing.T) {
	raw := "1 0 8:1 / / rw - ext4 /dev/mmcblk0p2 rw\n" +
		"2 1 9:0 / /home rw - ext4 /dev/md127 rw\n" +
		"3 1 9:0 / /srv rw - ext4 /dev/md127 rw\n" +
		"4 1 0:1 / /proc rw - proc proc rw\n"
	roots := volumeRoots(raw)
	if len(roots) != 2 {
		t.Fatalf("expected system and one data volume, got %+v", roots)
	}
	if roots[0].Path != "/" || roots[0].Reason == "" {
		t.Fatalf("system root must not be a sync destination: %+v", roots[0])
	}
	if roots[1].Path != "/srv" || roots[1].Kind != "volume" || roots[1].Reason != "" {
		t.Fatalf("wrong data mount: %+v", roots[1])
	}
}

func TestBlockMountNamesMatchFileManager(t *testing.T) {
	blocks := []namedBlock{{Name: "md127", Path: "/dev/md127", Type: "raid5", Children: []namedBlock{{Name: "md127p1", Type: "part", Label: "data", Mountpoints: []string{"/srv/data"}}}}, {Name: "sda", Model: "USB Disk", Children: []namedBlock{{Name: "sda1", Label: "Photos", Mountpoints: []string{"/media/photos"}}, {Name: "sda2", Label: "Backup", Mountpoints: []string{"/media/backup"}}}}}
	names := blockMountNames(blocks, map[string]string{"/dev/md127": "Storage"})
	if names["/srv/data"] != "Storage" || names["/media/photos"] != "USB Disk · Photos" {
		t.Fatalf("wrong mount labels: %+v", names)
	}
}
