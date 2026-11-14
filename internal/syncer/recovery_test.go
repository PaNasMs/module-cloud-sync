package syncer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryRetryAndManualPause(t *testing.T) {
	e := fixture(t)
	task := addTask(t, e, "both")
	e.pauseError(task.ID, problem(recoveryMessage))
	tasks, _ := e.tasks()
	if tasks[0].Recovery != "history" || tasks[0].RetryAt < time.Now().Unix()+50 || tasks[0].RetryCount != 1 {
		t.Fatal(tasks)
	}
	if _, err := e.taskAction("task.pause", task.ID); err != nil {
		t.Fatal(err)
	}
	e.DB.Exec("UPDATE tasks SET status='running' WHERE id=?", task.ID)
	e.pauseError(task.ID, problem(recoveryMessage))
	tasks, _ = e.tasks()
	if tasks[0].Status != "idle" {
		t.Fatal("completed transfer stuck running after concurrent manual pause", tasks)
	}
	if err := e.initialize(); err != nil {
		t.Fatal(err)
	}
	tasks, _ = e.tasks()
	if tasks[0].Recovery != "" || tasks[0].RetryAt != 0 || tasks[0].Paused != 1 {
		t.Fatal("manual pause overwritten", tasks)
	}
}

func TestTransferSizeRetryBackoffAndManualPause(t *testing.T) {
	e := fixture(t)
	task := addTask(t, e, "download")
	for attempt := 0; attempt < 8; attempt++ {
		before := time.Now().Unix()
		e.pauseError(task.ID, transferFailure("corrupted on transfer: sizes differ 278 vs 265"))
		tasks, err := e.tasks()
		if err != nil {
			t.Fatal(err)
		}
		got := tasks[0]
		delay := int64(min(60*(1<<min(attempt, 5)), 1800))
		if got.Recovery != "retry" || got.Paused != 1 || got.Status != "error" || got.RetryCount != attempt+1 || got.RetryAt < before+delay || got.RetryAt > time.Now().Unix()+delay {
			t.Fatalf("attempt %d: %+v", attempt, got)
		}
	}
	if _, err := e.taskAction("task.pause", task.ID); err != nil {
		t.Fatal(err)
	}
	e.pauseError(task.ID, transferFailure("corrupted on transfer: sizes differ 278 vs 265"))
	if err := e.initialize(); err != nil {
		t.Fatal(err)
	}
	tasks, err := e.tasks()
	if err != nil || tasks[0].Paused != 1 || tasks[0].Recovery != "" || tasks[0].RetryAt != 0 {
		t.Fatal("manual pause overwritten", tasks, err)
	}
}

func TestRealRcloneSizeFailureRetriesHistoryRecovery(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone unavailable")
	}
	e := fixture(t)
	task := addTask(t, e, "both")
	write := func(dir, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(task.Remote, "cloud report")
	if err := e.runTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	write(task.Local, "unsent local report")
	if _, err := e.DB.Exec("UPDATE tasks SET recovery='history',dirty=1 WHERE id=?", task.ID); err != nil {
		t.Fatal(err)
	}
	syncs := 0
	e.Runner = func(ctx context.Context, a Account, args []string, check func() error) ([]byte, error) {
		if args[0] == "sync" {
			syncs++
			if syncs == 1 {
				return nil, transferFailure(`{"level":"error","msg":"corrupted on transfer: sizes differ 1479 vs 1874"}`)
			}
		}
		return e.runProcess(ctx, a, args, check)
	}
	next := map[string]time.Time{accountID: time.Now().Add(time.Hour)}
	cycle := func() { e.cycle(context.Background(), next, map[string]int{}, func(bool) {}) }
	cycle()
	tasks, err := e.tasks()
	if err != nil || tasks[0].Initialized != -1 || tasks[0].Recovery != "retry" || tasks[0].RetryAt == 0 || tasks[0].Error != transferSizeMessage {
		t.Fatal("size failure did not schedule recovery", tasks, err)
	}
	if err := e.initialize(); err != nil {
		t.Fatal(err)
	}
	cycle()
	if syncs != 1 {
		t.Fatal("retry ran before deadline", syncs)
	}
	if _, err := e.DB.Exec("UPDATE tasks SET retry_at=1 WHERE id=?", task.ID); err != nil {
		t.Fatal(err)
	}
	cycle()
	tasks, err = e.tasks()
	if err != nil || syncs != 2 || tasks[0].Status != "idle" || tasks[0].Initialized != 1 || tasks[0].Recovery != "" || tasks[0].Paused != 0 || tasks[0].RetryAt != 0 || tasks[0].RetryCount != 0 {
		t.Fatal("scheduled recovery failed", tasks, syncs, err)
	}
	for _, dir := range []string{task.Local, task.Remote} {
		raw, err := os.ReadFile(filepath.Join(dir, "report.md"))
		if err != nil || string(raw) != "cloud report" {
			t.Fatal("recovery did not preserve cloud contents", string(raw), err)
		}
	}
	backups, err := filepath.Glob(filepath.Join(task.Local, ".panasms-cloud-versions", "recovery-*", "report.md"))
	if err != nil || len(backups) != 1 {
		t.Fatal("local report backup missing", backups, err)
	}
	raw, err := os.ReadFile(backups[0])
	if err != nil || string(raw) != "unsent local report" {
		t.Fatal("local report was not preserved", string(raw), err)
	}
}

func TestRealRcloneCloudAuthoritativeRecovery(t *testing.T) {
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
	write(task.Remote, "changed", "cloud version")
	if err := e.runTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	write(task.Local, "changed", "unsent local version")
	write(task.Local, "local-only", "unsent new file")
	work := filepath.Join(e.Root, "tasks", task.ID)
	files, _ := filepath.Glob(filepath.Join(work, "*.lst"))
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
	}
	write(work, "interrupted.lck", deadPID(t))

	next := map[string]time.Time{accountID: time.Now().Add(time.Hour)}
	e.cycle(context.Background(), next, map[string]int{}, func(bool) {})
	tasks, err := e.tasks()
	if err != nil || tasks[0].Recovery != "history" || tasks[0].RetryAt == 0 || tasks[0].Paused != 1 {
		t.Fatal("missing automatic recovery after stale lock", tasks, err)
	}
	// Advance the persisted retry deadline instead of waiting for the backoff.
	if _, err := e.DB.Exec("UPDATE tasks SET retry_at=1 WHERE id=?", task.ID); err != nil {
		t.Fatal(err)
	}
	e.cycle(context.Background(), next, map[string]int{}, func(bool) {})
	tasks, err = e.tasks()
	if err != nil || tasks[0].Status != "idle" || tasks[0].Recovery != "" || tasks[0].Paused != 0 {
		log, _ := os.ReadFile(filepath.Join(e.Root, accountID+".last-error.log"))
		t.Fatalf("automatic recovery failed: %+v %v %s", tasks, err, log)
	}
	task.Initialized = 1
	archived, _ := filepath.Glob(work + ".before-recovery-*/interrupted.lck")
	if len(archived) != 1 {
		t.Fatal("old lock/history were not retained", archived)
	}
	if _, err := os.Stat(filepath.Join(work, "interrupted.lck")); !os.IsNotExist(err) {
		t.Fatal("recovered task still locked", err)
	}
	read(task.Local, "changed", "cloud version")
	read(task.Remote, "changed", "cloud version")
	for _, dir := range []string{task.Local, task.Remote} {
		if _, err := os.Stat(filepath.Join(dir, "local-only")); !os.IsNotExist(err) {
			t.Fatal("local-only file was uploaded or retained in live folder", err)
		}
	}
	backups, _ := filepath.Glob(filepath.Join(task.Local, ".panasms-cloud-versions", "recovery-*"))
	if len(backups) != 1 {
		t.Fatal(backups)
	}
	read(backups[0], "changed", "unsent local version")
	read(backups[0], "local-only", "unsent new file")
	task.Recovery = ""
	if err := e.runTask(context.Background(), task); err != nil {
		t.Fatal("normal sync after recovery", err)
	}
	write(task.Remote, "after-recovery", "new cloud file")
	if err := e.runTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	read(task.Local, "after-recovery", "new cloud file")
	for _, scenario := range []string{"missing-marker", "symlink-backup"} {
		t.Run(scenario, func(t *testing.T) {
			task.Recovery = "history"
			write(task.Local, "changed", "protect this version")
			if scenario == "missing-marker" {
				os.Remove(filepath.Join(task.Remote, ".panasms-cloud-access-"+task.ID))
			} else {
				write(task.Remote, ".panasms-cloud-access-"+task.ID, task.ID+"\n")
				os.Rename(filepath.Join(task.Local, ".panasms-cloud-versions"), filepath.Join(task.Local, ".panasms-cloud-versions-saved"))
				os.Symlink(t.TempDir(), filepath.Join(task.Local, ".panasms-cloud-versions"))
			}
			if err := e.runTask(context.Background(), task); err == nil || (!strings.Contains(err.Error(), "marker") && !strings.Contains(err.Error(), "symlink")) {
				t.Fatal(err)
			}
			read(task.Local, "changed", "protect this version")
		})
	}
}

func TestRealRcloneLegacyRecoveryAndPause(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone unavailable")
	}
	e := fixture(t)
	task := addTask(t, e, "both")
	task.Initialized = 1
	task.Recovery = "history"
	os.WriteFile(filepath.Join(task.Remote, "cloud-file"), []byte("cloud"), 0600)
	os.WriteFile(filepath.Join(task.Local, "local-file"), []byte("local"), 0600)
	if err := e.runTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(task.Local, "cloud-file")); err != nil || string(raw) != "cloud" {
		t.Fatal(string(raw), err)
	}
	if _, err := os.Stat(filepath.Join(task.Remote, "local-file")); !os.IsNotExist(err) {
		t.Fatal("legacy local-only file uploaded", err)
	}
	runner := e.runProcess
	e.Runner = func(ctx context.Context, a Account, args []string, check func() error) ([]byte, error) {
		if args[0] == "sync" {
			if _, err := e.taskAction("task.pause", task.ID); err != nil {
				t.Fatal(err)
			}
			return nil, check()
		}
		return runner(ctx, a, args, check)
	}
	if err := e.runTask(context.Background(), task); err != errPaused {
		t.Fatal("manual pause did not stop recovery", err)
	}
	tasks, _ := e.tasks()
	if tasks[0].Paused != 1 || tasks[0].Recovery != "" {
		t.Fatal(tasks)
	}
}
