package syncer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeDevice publishes a filesystem UUID for a stand-in device node.
func fakeDevice(t *testing.T, name, uuid string) string {
	t.Helper()
	dir := t.TempDir()
	device := filepath.Join(dir, name)
	if err := os.WriteFile(device, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(device, filepath.Join(dir, uuid)); err != nil {
		t.Fatal(err)
	}
	old := uuidLinks
	uuidLinks = dir
	t.Cleanup(func() { uuidLinks = old })
	return device
}

func TestMountIdentityUsesFilesystemUUID(t *testing.T) {
	device := fakeDevice(t, "sdb1", "1bfefdfb-34c9")
	current := withUUID(marshal([]string{"8:17", "/", "/", "ext4", device}))
	if current != marshal([]string{"uuid:1bfefdfb-34c9", "/", "/", "ext4", "8:17", device}) {
		t.Fatal(current)
	}
	if stableMount(current) != `["uuid:1bfefdfb-34c9","/","/","ext4"]` {
		t.Fatal(stableMount(current))
	}
	unpublished := marshal([]string{"8:17", "/", "/", "ext4", filepath.Join(t.TempDir(), "missing")})
	if withUUID(unpublished) != unpublished || stableMount(unpublished) != unpublished {
		t.Fatal("identity without a published UUID must stay device-based")
	}
}

func TestDeviceRenameKeepsOriginalVolume(t *testing.T) {
	e := fixture(t)
	device := fakeDevice(t, "sdb1", "uuid-a")
	current := func(uuid, numbers string) {
		e.Mount = func(string) (string, error) {
			return marshal([]string{"uuid:" + uuid, "/", "/srv/test", "ext4", numbers, device}), nil
		}
	}
	stored := func(task Task) string {
		var value string
		if err := e.DB.QueryRow("SELECT mount FROM tasks WHERE id=?", task.ID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	stable := `["uuid:uuid-a","/","/srv/test","ext4"]`

	// Recorded by device and still on that device: upgraded in place.
	task := addTask(t, e, "upload")
	task.Mount = marshal([]string{"8:17", "/", "/srv/test", "ext4", device})
	e.DB.Exec("UPDATE tasks SET mount=? WHERE id=?", task.Mount, task.ID)
	current("uuid-a", "8:17")
	if !e.originalVolume(task) || stored(task) != stable {
		t.Fatal("unchanged device was not upgraded", stored(task))
	}

	// Recorded as /dev/sda1 before a reboot renamed the disk to /dev/sdb1.
	renamed := addTask(t, e, "both")
	renamed.Mount = `["8:1","/","/srv/test","ext4","/dev/sda1"]`
	e.DB.Exec("UPDATE tasks SET mount=? WHERE id=?", renamed.Mount, renamed.ID)
	if e.originalVolume(renamed) || stored(renamed) != renamed.Mount {
		t.Fatal("renamed device accepted without the task's access marker")
	}
	marker := filepath.Join(renamed.Local, ".panasms-cloud-access-"+renamed.ID)
	os.WriteFile(marker, []byte("another task\n"), 0600)
	if e.originalVolume(renamed) {
		t.Fatal("foreign access marker accepted")
	}
	os.WriteFile(marker, []byte(renamed.ID+"\n"), 0600)
	other := renamed
	other.Mount = `["8:1","/","/srv/other","ext4","/dev/sda1"]`
	if e.originalVolume(other) {
		t.Fatal("different mount point accepted")
	}
	if !e.originalVolume(renamed) || stored(renamed) != stable {
		t.Fatal("task with its access marker did not follow the renamed device", stored(renamed))
	}

	// Once recorded by UUID, only the filesystem matters.
	renamed.Mount = stable
	current("uuid-a", "8:33")
	if !e.originalVolume(renamed) {
		t.Fatal("same filesystem rejected after another rename")
	}
	current("uuid-b", "8:17")
	if e.originalVolume(renamed) {
		t.Fatal("another filesystem at the same mount point accepted")
	}
	e.Mount = func(string) (string, error) { return marshal([]string{"8:17", "/", "/srv/test", "ext4", device}), nil }
	if e.originalVolume(renamed) {
		t.Fatal("volume without a readable UUID accepted for a UUID identity")
	}
}

func TestRealRcloneInterruptedFirstSyncCompletes(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone unavailable")
	}
	e := fixture(t)
	task := addTask(t, e, "both")
	write := func(dir, name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(dir, name, want string) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(raw) != want {
			t.Fatalf("%s/%s: %q %v; want %q", dir, name, raw, err, want)
		}
	}
	// The first run stopped mid-transfer: one download is truncated, one cloud
	// file never arrived, one local file was never sent and neither was the marker.
	write(task.Remote, "partial", "complete cloud content")
	write(task.Local, "partial", "compl")
	write(task.Remote, "cloud-only", "cloud")
	write(task.Local, "local-only", "local")
	e.DB.Exec("UPDATE tasks SET initialized=-1 WHERE id=?", task.ID)
	task.Initialized = -1
	if err := e.runTask(context.Background(), task); err != nil {
		log, _ := os.ReadFile(filepath.Join(e.Root, accountID+".last-error.log"))
		t.Fatalf("%v\n%s", err, log)
	}
	for _, dir := range []string{task.Local, task.Remote} {
		read(dir, "partial", "complete cloud content")
		read(dir, "cloud-only", "cloud")
		read(dir, "local-only", "local")
		read(dir, ".panasms-cloud-access-"+task.ID, task.ID+"\n")
	}
	backups, _ := filepath.Glob(filepath.Join(task.Local, ".panasms-cloud-versions", "recovery-*"))
	if len(backups) != 1 {
		t.Fatal(backups)
	}
	read(backups[0], "partial", "compl")
	tasks, _ := e.tasks()
	if tasks[0].Initialized != 1 || tasks[0].LastSync == 0 || tasks[0].Recovery != "" {
		t.Fatal(tasks)
	}
	write(task.Remote, "later", "next pass")
	if err := e.runTask(context.Background(), tasks[0]); err != nil {
		t.Fatal("normal sync after completing the first one", err)
	}
	read(task.Local, "later", "next pass")
}

func TestListingSnapshotIgnoresFolderTimes(t *testing.T) {
	e := fixture(t)
	listed := 0
	e.Runner = func(context.Context, Account, []string, func() error) ([]byte, error) {
		listed++
		// Dropbox reports the listing time for folders; file times are real.
		return []byte(fmt.Sprintf(`[{"Path":"docs","Name":"docs","IsDir":true,"Size":-1,"ModTime":"2026-10-02T22:0%d:00Z"},
{"Path":"docs/a.txt","Name":"a.txt","IsDir":false,"Size":3,"ModTime":"2026-10-01T10:00:00Z"}]`, listed)), nil
	}
	first, _, err := e.listing(context.Background(), Account{}, "folder")
	if err != nil {
		t.Fatal(err)
	}
	second, _, _ := e.listing(context.Background(), Account{}, "folder")
	if first != second {
		t.Fatal("unchanged cloud content produced different snapshots")
	}
	e.Runner = func(context.Context, Account, []string, func() error) ([]byte, error) {
		return []byte(`[{"Path":"docs","Name":"docs","IsDir":true,"Size":-1,"ModTime":"2026-10-02T22:09:00Z"},
{"Path":"docs/a.txt","Name":"a.txt","IsDir":false,"Size":3,"ModTime":"2026-10-02T11:00:00Z"}]`), nil
	}
	if changed, _, _ := e.listing(context.Background(), Account{}, "folder"); changed == first {
		t.Fatal("changed file time not detected")
	}
}
