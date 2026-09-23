package syncer

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var errRotated = errors.New("token rotated")
var errPaused = errors.New("task paused")

const recoveryMessage = "Two-way sync history requires recovery. Preserve both folders and create a new task with an empty destination; automatic reset is disabled."

type Runner func(context.Context, Account, []string, func() error) ([]byte, error)
type limitedBuffer struct {
	sync.Mutex
	b        bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := len(p)
	left := b.limit - b.b.Len()
	if len(p) > left {
		p = p[:left]
		b.overflow = true
	}
	_, _ = b.b.Write(p)
	return n, nil
}
func (b *limitedBuffer) Exceeded() bool { b.Lock(); defer b.Unlock(); return b.overflow }
func (e *Engine) runProcess(ctx context.Context, a Account, args []string, check func() error) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()
	argv := []string{"--config", e.config(a), "--cache-dir", filepath.Join(e.Root, "cache"), "--contimeout", "15s", "--timeout", "60s", "--retries", "1", "--low-level-retries", "2", "--drive-skip-gdocs", "--drive-skip-shortcuts"}
	argv = append(argv, args...)
	cmd := exec.Command("rclone", argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	out := &limitedBuffer{limit: 32 << 20}
	logs := &limitedBuffer{limit: 4 << 20}
	cmd.Stdout = out
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		return nil, problem("Cannot start rclone. Check that the module dependencies are installed.")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stop := func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	nextCheck := time.Now()
	for {
		select {
		case err := <-done:
			if out.Exceeded() || logs.Exceeded() {
				return nil, problem("Cloud output exceeds the supported limit")
			}
			if err != nil {
				text := strings.ToLower(logs.b.String())
				if strings.Contains(text, "max-delete") || strings.Contains(text, "all files were changed") || strings.Contains(text, "too many deletes") {
					return nil, problem("Safety check stopped the task: too many files changed or were deleted. Review both folders before retrying.")
				}
				if len(args) > 0 && args[0] == "bisync" {
					return nil, problem(recoveryMessage)
				}
				return nil, problem("Cloud operation failed. Check account access, folder availability and quota; reconnect if necessary.")
			}
			return out.b.Bytes(), nil
		case <-ctx.Done():
			stop()
			return nil, ctx.Err()
		case <-ticker.C:
			if out.Exceeded() || logs.Exceeded() {
				stop()
				return nil, problem("Cloud output exceeds the supported limit")
			}
			if time.Now().Before(nextCheck) {
				continue
			}
			nextCheck = time.Now().Add(time.Second)
			if check != nil {
				if err := check(); err != nil {
					stop()
					return nil, err
				}
			}
			rotated, err := e.ensure(ctx, a, false)
			if err != nil {
				stop()
				return nil, err
			}
			if rotated {
				stop()
				return nil, errRotated
			}
		}
	}
}
func (e *Engine) runTask(ctx context.Context, t Task) error {
	mount, err := e.Mount(t.Local)
	if err != nil || !sameMount(mount, t.Mount) {
		return problem("Local volume changed or is unavailable; reconnect the original volume")
	}
	if _, err = e.Local(t.Local); err != nil {
		return err
	}
	a, err := e.account(t.Account)
	if err != nil {
		return err
	}
	if _, err = e.ensure(ctx, a, true); err != nil {
		return err
	}
	listingCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	snapshot, entries, err := e.listing(listingCtx, a, t.Remote)
	cancel()
	if err != nil {
		return err
	}
	target := "cloud:" + t.Remote
	work := filepath.Join(e.Root, "tasks", t.ID)
	if err = os.MkdirAll(work, 0700); err != nil {
		return err
	}
	args := []string{}
	if t.Direction == "both" {
		if t.Initialized < 0 {
			return problem(recoveryMessage)
		}
		if t.Initialized == 0 {
			localFiles := false
			err = filepath.WalkDir(t.Local, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if excluded(d.Name()) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if !d.IsDir() {
					localFiles = true
					return fs.SkipAll
				}
				return nil
			})
			if err != nil {
				return err
			}
			remoteFiles := false
			for _, entry := range entries {
				remoteFiles = remoteFiles || !entry.IsDir
			}
			if localFiles && remoteFiles {
				return problem("For the first two-way sync, one folder must be empty. Use a new folder to avoid initial conflicts.")
			}
		}
		args = []string{"bisync", t.Local, target, "--workdir", work, "--max-delete", "10"}
		if t.Initialized == 0 {
			args = append(args, "--resync")
			if _, err = e.DB.Exec("UPDATE tasks SET initialized=-1 WHERE id=?", t.ID); err != nil {
				return err
			}
		}
	} else {
		source, dest := t.Local, target
		if t.Direction == "download" {
			source, dest = target, t.Local
		}
		backup := strings.TrimRight(dest, "/") + "/.panasms-cloud-versions/" + time.Now().UTC().Format("20060102T150405") + "-" + newID()[:8]
		args = []string{"copy", source, dest, "--create-empty-src-dirs", "--backup-dir", backup}
	}
	args = append(args, "--exclude", ".panasms-cloud-versions/**", "--exclude", ".panasms-cloud-conflicts/**", "--transfers", "2", "--checkers", "2")
	check := func() error {
		var paused int
		if err := e.DB.QueryRow("SELECT paused FROM tasks WHERE id=?", t.ID).Scan(&paused); err != nil {
			return err
		}
		if paused != 0 {
			return errPaused
		}
		m, err := e.Mount(t.Local)
		if err != nil || !sameMount(m, t.Mount) {
			return problem("Local volume changed or is unavailable; reconnect the original volume")
		}
		return nil
	}
	ctx, cancel = context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()
	for {
		if err = check(); err != nil {
			return err
		}
		_, err = e.Runner(ctx, a, args, check)
		if !errors.Is(err, errRotated) {
			break
		}
		if t.Direction == "both" {
			return problem(recoveryMessage)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	if _, err = e.DB.Exec("UPDATE tasks SET status='idle',initialized=1,snapshot=?,last_sync=?,error='' WHERE id=?", snapshot, time.Now().Unix(), t.ID); err != nil {
		return err
	}
	return e.event(t.ID, "success", "Synchronization completed")
}
