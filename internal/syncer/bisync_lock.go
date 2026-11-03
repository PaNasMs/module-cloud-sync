package syncer

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const activeLockMessage = "Another synchronization process still holds this task's lock. The task will retry automatically after it exits."
const unknownLockMessage = "The synchronization lock could not be verified. The task is paused to protect both folders; check the lock before retrying."

// A dead PID proves interruption, not that the old bisync history is usable.
// Keep the lock with that history until recoverHistory archives the directory.
func bisyncLocks(work string) (bool, error) {
	paths, err := filepath.Glob(filepath.Join(work, "*.lck"))
	if err != nil {
		return false, err
	}
	stale := false
	for _, path := range paths {
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return false, problem(unknownLockMessage)
		}
		info, statErr := file.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(file, 65))
		file.Close()
		if statErr != nil || !info.Mode().IsRegular() || readErr != nil || len(raw) > 64 {
			return false, problem(unknownLockMessage)
		}
		pid, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 32)
		if err != nil || pid <= 0 {
			return false, problem(unknownLockMessage)
		}
		err = syscall.Kill(int(pid), 0)
		if err == nil || errors.Is(err, syscall.EPERM) {
			return false, problem(activeLockMessage)
		}
		if !errors.Is(err, syscall.ESRCH) {
			return false, problem(unknownLockMessage)
		}
		stale = true
	}
	return stale, nil
}
