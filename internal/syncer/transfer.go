package syncer

import (
	"bytes"
	"context"
	"errors"
	"github.com/PaNasMs/module-sdk/maintenance"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

var errRotated = errors.New("token rotated")
var errPaused = errors.New("task paused")

const legacyRecoveryMessage = "Two-way sync history requires recovery. Preserve both folders and create a new task with an empty destination; automatic reset is disabled."

const recoveryMessage = "Two-way sync history will be rebuilt automatically from the cloud; local changes will be backed up."

// maxDelete bounds how many deletions bisync will propagate in one pass. High
// enough to let deliberate bulk reorganizations sync through, low enough to
// still stop a run where a side has effectively vanished.
const maxDelete = "5000"

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
	config, cleanup, err := e.refreshConfig(ctx, a)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	argv := []string{"--config", config, "--cache-dir", filepath.Join(e.Root, "cache"), "--contimeout", "15s", "--timeout", "60s", "--retries", "1", "--low-level-retries", "2", "--drive-skip-gdocs", "--drive-skip-shortcuts"}
	argv = append(argv, args...)
	cmd := exec.Command("rclone", argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
				diagnostic := redactTransferLog(logs.b.String())
				if token, ok := e.tokens[a.ID]; ok && token.Access.AccessToken != "" {
					diagnostic = strings.ReplaceAll(diagnostic, token.Access.AccessToken, "[redacted]")
				}
				_ = atomicFile(filepath.Join(e.Root, a.ID+".last-error.log"), []byte(diagnostic))
				return nil, transferFailure(logs.b.String())
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
			_, err := e.ensure(ctx, a, false)
			if err != nil {
				stop()
				return nil, err
			}
		}
	}
}

var transferSecrets = regexp.MustCompile(`(?i)(bearer\s+|(?:access_token|refresh_token|client_secret)["']?\s*[:=]\s*["']?)[^\s"'&,}]+`)
var transferURLs = regexp.MustCompile(`https?://[^\s"'<>]+`)

func redactTransferLog(text string) string {
	text = transferSecrets.ReplaceAllString(text, "${1}[redacted]")
	return transferURLs.ReplaceAllString(text, "[endpoint]")
}

func transferFailure(log string) error {
	text := strings.ToLower(log)
	switch {
	case strings.Contains(text, "max-delete"), strings.Contains(text, "all files were changed"), strings.Contains(text, "too many deletes"):
		return problem("Safety check stopped the task: too many files changed or were deleted. Review both folders before retrying.")
	case strings.Contains(text, "must run --resync"), strings.Contains(text, "cannot find prior"), strings.Contains(text, "run --resync to recover"):
		return problem(recoveryMessage)
	case strings.Contains(text, "invalid_grant"), strings.Contains(text, "unauthorized"), strings.Contains(text, "invalid_access_token"):
		return problem("Account authorization expired or access was denied. Reconnect the account.")
	case strings.Contains(text, "429"), strings.Contains(text, "503"), strings.Contains(text, "502"), strings.Contains(text, "timeout"), strings.Contains(text, "connection refused"), strings.Contains(text, "no such host"):
		return problem("Cloud is unreachable; retry later.")
	default:
		return problem("Cloud operation failed. Check account access, folder availability and quota; reconnect if necessary.")
	}
}
func (e *Engine) runTask(ctx context.Context, t Task) error {
	release, lockErr := maintenance.Acquire()
	if lockErr != nil {
		return lockErr
	}
	defer release()
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
	if t.Direction == "both" && (t.Initialized < 0 || t.Recovery == "history") {
		return e.recoverHistory(ctx, a, t, work, check)
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
		args = []string{"bisync", t.Local, target, "--workdir", work, "--max-delete", maxDelete}
		if t.Initialized == 0 {
			// A stable access marker also keeps empty and single-file histories valid
			// in rclone 1.60. Never recreate it after initialization: loss must stop sync.
			marker := ".panasms-cloud-access-" + t.ID
			if err = atomicFile(filepath.Join(t.Local, marker), []byte(t.ID+"\n")); err != nil {
				return err
			}
			if err = atomicFile(filepath.Join(work, "access-marker"), []byte(marker)); err != nil {
				return err
			}
			args = append(args, "--resync")
		} else if marker, readErr := os.ReadFile(filepath.Join(work, "access-marker")); readErr == nil {
			if string(marker) != ".panasms-cloud-access-"+t.ID {
				return problem(recoveryMessage)
			}
			args = append(args, "--check-access", "--check-filename", string(marker))
		} else if !os.IsNotExist(readErr) {
			return readErr
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

	ctx, cancel = context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()
	for {
		if err = check(); err != nil {
			return err
		}
		if t.Direction == "both" && t.Initialized == 0 {
			if _, err = e.DB.Exec("UPDATE tasks SET initialized=-1 WHERE id=?", t.ID); err != nil {
				return err
			}
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
	if _, err = e.DB.Exec("UPDATE tasks SET status='idle',initialized=1,snapshot=?,last_sync=?,error='',recovery='',retry_at=0,retry_count=0 WHERE id=?", snapshot, time.Now().Unix(), t.ID); err != nil {
		return err
	}
	return e.event(t.ID, "success", "Synchronization completed")
}
