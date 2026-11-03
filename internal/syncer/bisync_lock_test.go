package syncer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func deadPID(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(cmd.Process.Pid)
}

func TestBisyncLocks(t *testing.T) {
	for _, tt := range []struct{ name, content, message string }{
		{"dead", deadPID(t), ""},
		{"live", strconv.Itoa(os.Getpid()), activeLockMessage},
		{"empty", "", unknownLockMessage},
		{"negative", "-1", unknownLockMessage},
		{"zero", "0", unknownLockMessage},
		{"overflow", "4294967296", unknownLockMessage},
		{"oversize", strings.Repeat("1", 65), unknownLockMessage},
		{"unknown-format", `{ "pid": 123 }`, unknownLockMessage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			work := t.TempDir()
			path := filepath.Join(work, "run.lck")
			if err := os.WriteFile(path, []byte(tt.content), 0600); err != nil {
				t.Fatal(err)
			}
			stale, err := bisyncLocks(work)
			if tt.message == "" {
				if !stale || err != nil {
					t.Fatal(stale, err)
				}
			} else if stale || safeError(err) != tt.message {
				t.Fatal(stale, err)
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != tt.content {
				t.Fatal("lock was modified", err)
			}
		})
	}
	work := t.TempDir()
	if err := os.Symlink("/does/not/exist", filepath.Join(work, "link.lck")); err != nil {
		t.Fatal(err)
	}
	if _, err := bisyncLocks(work); safeError(err) != unknownLockMessage {
		t.Fatal(err)
	}
}

func TestActiveBisyncLockBlocksRecovery(t *testing.T) {
	e := fixture(t)
	task := addTask(t, e, "both")
	task.Recovery = "history"
	work := filepath.Join(e.Root, "tasks", task.ID)
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "live.lck"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	e.Runner = func(context.Context, Account, []string, func() error) ([]byte, error) {
		t.Fatal("active lock allowed cloud or file operations")
		return nil, nil
	}
	err := e.runTask(context.Background(), task)
	if safeError(err) != activeLockMessage || recoveryKind(err) != "retry" {
		t.Fatal(err)
	}
}
