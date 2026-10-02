package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const recoveryAccessMessage = "Recovery stopped: the cloud access marker is missing or invalid. Check the selected cloud folder."

func recoveryKind(err error) string {
	var f *Failure
	if errors.As(err, &f) && f.Reconnect {
		return "authorization"
	}
	switch safeError(err) {
	case recoveryMessage, legacyRecoveryMessage:
		return "history"
	case "Account authorization expired or access was denied. Reconnect the account.":
		return "authorization"
	case "Cloud is unreachable; retry later.", "Cloud access service is unavailable; retrying later.":
		return "retry"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errRotated) {
		return "retry"
	}
	return ""
}

// Never run a real resync against divergent folders: older rclone versions can
// write local changes back into the cloud even when recovery must favor it.
func (e *Engine) recoverHistory(ctx context.Context, a Account, t Task, work string, check func() error) error {
	if err := check(); err != nil {
		return err
	}
	marker := ".panasms-cloud-access-" + t.ID
	remote := "cloud:" + t.Remote
	markerRemote := strings.TrimRight(remote, "/") + "/" + marker
	if _, err := os.Stat(filepath.Join(work, "access-marker")); os.IsNotExist(err) && t.Initialized > 0 {
		// Pre-marker tasks already have an established cloud account and folder.
		// Add only the access sentinel; never upload their unsent local files.
		if err = atomicFile(filepath.Join(work, "access-marker-source"), []byte(t.ID+"\n")); err != nil {
			return err
		}
		if _, err = e.Runner(ctx, a, []string{"copyto", filepath.Join(work, "access-marker-source"), markerRemote, "--ignore-existing"}, check); err != nil {
			return err
		}
		if err = atomicFile(filepath.Join(work, "access-marker"), []byte(marker)); err != nil {
			return err
		}
	}
	raw, err := e.Runner(ctx, a, []string{"cat", markerRemote}, check)
	if err != nil {
		if recoveryKind(err) == "retry" || recoveryKind(err) == "authorization" {
			return err
		}
		return problem(recoveryAccessMessage)
	}
	if string(raw) != t.ID+"\n" {
		return problem(recoveryAccessMessage)
	}
	versions := filepath.Join(t.Local, ".panasms-cloud-versions")
	if err := os.Mkdir(versions, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(versions)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return problem("Recovery backup folder must be a local directory, not a symlink.")
	}
	backup := filepath.Join(versions, "recovery-"+time.Now().UTC().Format("20060102T150405")+"-"+newID()[:8])
	if _, err = e.DB.Exec("UPDATE tasks SET initialized=-1,recovery='history' WHERE id=? AND paused=0", t.ID); err != nil {
		return err
	}
	filters := []string{"--exclude", ".panasms-cloud-versions/**", "--exclude", ".panasms-cloud-conflicts/**", "--transfers", "2", "--checkers", "2"}
	run := func(args ...string) error {
		if err := check(); err != nil {
			return err
		}
		_, err := e.Runner(ctx, a, append(args, filters...), check)
		return err
	}
	if err = run("sync", remote, t.Local, "--backup-dir", backup, "--checksum", "--max-delete", maxDelete, "--create-empty-src-dirs"); err != nil {
		return err
	}
	if err = run("check", t.Local, remote, "--download"); err != nil {
		return err
	}
	snapshot, _, err := e.listing(ctx, a, t.Remote)
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Dir(work), ".recovery-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err = run("bisync", t.Local, remote, "--resync", "--dry-run", "--workdir", temp); err != nil {
		return err
	}
	if err = run("check", t.Local, remote, "--download"); err != nil {
		return err
	}
	after, _, err := e.listing(ctx, a, t.Remote)
	if err != nil {
		return err
	}
	if snapshot != after {
		return problem(recoveryMessage)
	}
	listings, err := filepath.Glob(filepath.Join(temp, "*.path1.lst-dry"))
	if err != nil || len(listings) != 1 {
		return problem("Recovery could not rebuild synchronization history. Local backups have been preserved.")
	}
	prefix := strings.TrimSuffix(listings[0], ".path1.lst-dry")
	for _, side := range []string{".path1", ".path2"} {
		if err = os.Rename(prefix+side+".lst-dry", prefix+side+".lst"); err != nil {
			return err
		}
	}
	if err = atomicFile(filepath.Join(temp, "access-marker"), []byte(marker)); err != nil {
		return err
	}
	if err = check(); err != nil {
		return err
	}
	if err = os.Rename(work, work+".before-recovery-"+newID()[:8]); err != nil {
		return err
	}
	if err = os.Rename(temp, work); err != nil {
		return err
	}
	if _, err = e.DB.Exec("UPDATE tasks SET status='idle',initialized=1,snapshot=?,last_sync=?,error='',recovery='',retry_at=0,retry_count=0 WHERE id=?", snapshot, time.Now().Unix(), t.ID); err != nil {
		return err
	}
	return e.event(t.ID, "success", "Synchronization history restored from the cloud. Replaced and local-only files were saved in .panasms-cloud-versions.")
}
