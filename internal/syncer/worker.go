package syncer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func (e *Engine) initialize() error {
	if _, err := e.DB.Exec("UPDATE tasks SET status='error',error='Transfer interrupted; review before resuming.',paused=1 WHERE status='running'"); err != nil {
		return err
	}
	_, err := e.DB.Exec("UPDATE tasks SET dirty=1 WHERE paused=0 AND status!='error'")
	return err
}
func (e *Engine) pauseError(id string, err error) {
	message := safeError(err)
	_, _ = e.DB.Exec("UPDATE tasks SET paused=1,status='error',error=? WHERE id=?", message, id)
	_ = e.event(id, "error", message)
}
func (e *Engine) monitor(ctx context.Context) {
	w, err := newWatcher()
	if err != nil {
		_, _ = e.DB.Exec("UPDATE tasks SET paused=1,status='error',error='Cannot monitor local files'")
		return
	}
	defer w.Close()
	watched := map[string]bool{}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			tasks, err := e.tasks()
			if err != nil {
				continue
			}
			active := map[string]bool{}
			for _, t := range tasks {
				if t.Paused != 0 {
					continue
				}
				active[t.ID] = true
				if !watched[t.ID] {
					m, err := e.Mount(t.Local)
					if err != nil || !sameMount(m, t.Mount) {
						e.pauseError(t.ID, problem("Local volume changed or is unavailable"))
						continue
					}
					if _, err = e.Local(t.Local); err == nil {
						err = w.Add(t.Local, t.ID)
					}
					if err != nil {
						w.Remove(t.ID)
						e.pauseError(t.ID, err)
						continue
					}
					watched[t.ID] = true
				}
			}
			for id := range watched {
				if !active[id] {
					w.Remove(id)
					delete(watched, id)
				}
			}
			affected, err := w.Drain()
			if err != nil {
				for id := range watched {
					e.pauseError(id, err)
					w.Remove(id)
					delete(watched, id)
				}
				continue
			}
			for id := range w.Rebuild {
				w.Remove(id)
				delete(watched, id)
				delete(w.Rebuild, id)
			}
			for id := range affected {
				_, _ = e.DB.Exec("UPDATE tasks SET dirty=1 WHERE id=?", id)
			}
		}
	}
}
func (e *Engine) cycle(ctx context.Context, next map[string]time.Time, failures map[string]int, busy func(bool)) {
	if !e.cloudMu.TryLock() {
		return
	}
	defer e.cloudMu.Unlock()
	accounts, err := e.accounts()
	if err != nil {
		return
	}
	tasks, err := e.tasks()
	if err != nil {
		return
	}
	blocked := map[string]bool{}
	for _, a := range accounts {
		needed := false
		for _, t := range tasks {
			needed = needed || (t.Account == a.ID && t.Paused == 0)
		}
		if !needed {
			continue
		}
		if time.Now().Before(next[a.ID]) {
			blocked[a.ID] = a.Error != ""
			continue
		}
		pollCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_, err = e.ensure(pollCtx, a, true)
		cursor := ""
		changed := false
		if err == nil {
			cursor, changed, err = e.poll(pollCtx, a)
		}
		dirty := []string{}
		if err == nil && changed {
			for _, t := range tasks {
				if t.Account != a.ID || t.Paused != 0 {
					continue
				}
				var snapshot string
				snapshot, _, err = e.listing(pollCtx, a, t.Remote)
				if err != nil {
					break
				}
				if snapshot != t.Snapshot {
					dirty = append(dirty, t.ID)
				}
			}
		}
		cancel()
		if err != nil {
			blocked[a.ID] = true
			failures[a.ID]++
			delay := 60 * time.Second * time.Duration(1<<min(failures[a.ID], 5))
			next[a.ID] = time.Now().Add(min(delay, 30*time.Minute))
			_, _ = e.DB.Exec("UPDATE accounts SET error=? WHERE id=?", safeError(err), a.ID)
			var f *Failure
			if errors.As(err, &f) && f.Reconnect {
				for _, t := range tasks {
					if t.Account == a.ID {
						e.pauseError(t.ID, err)
					}
				}
			}
			continue
		}
		tx, err := e.DB.Begin()
		if err != nil {
			continue
		}
		if _, err = tx.Exec("UPDATE accounts SET cursor=?,error='' WHERE id=?", cursor, a.ID); err == nil {
			for _, id := range dirty {
				if _, err = tx.Exec("UPDATE tasks SET dirty=1 WHERE id=?", id); err != nil {
					break
				}
			}
		}
		if err != nil {
			_ = tx.Rollback()
			blocked[a.ID] = true
			continue
		}
		if err = tx.Commit(); err != nil {
			blocked[a.ID] = true
			continue
		}
		failures[a.ID] = 0
		next[a.ID] = time.Now().Add(time.Minute)
	}
	tasks, err = e.tasks()
	if err != nil {
		return
	}
	for _, t := range tasks {
		if t.Paused != 0 || t.Dirty == 0 || t.Status == "error" || blocked[t.Account] {
			continue
		}
		r, err := e.DB.Exec("UPDATE tasks SET status='running',dirty=0,error='' WHERE id=? AND paused=0 AND dirty=1 AND status!='error'", t.ID)
		if err != nil {
			return
		}
		n, _ := r.RowsAffected()
		if n == 0 {
			continue
		}
		busy(true)
		err = e.runTask(ctx, t)
		busy(false)
		if err != nil {
			if errors.Is(err, errPaused) {
				_, _ = e.DB.Exec("UPDATE tasks SET status='idle',dirty=1,error='' WHERE id=?", t.ID)
			} else {
				e.pauseError(t.ID, err)
			}
		}
		break
	}
}
func (e *Engine) Serve(ctx context.Context, busy func(bool)) error {
	lock, err := os.OpenFile(filepath.Join(e.Root, ".worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	if err = e.initialize(); err != nil {
		return err
	}
	path := filepath.Join(e.Root, "worker.sock")
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(path)
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e.monitor(ctx) }()
	go func() {
		defer wg.Done()
		next := map[string]time.Time{}
		failures := map[string]int{}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.cycle(ctx, next, failures, busy)
			}
		}
	}()
	go func() { <-ctx.Done(); listener.Close() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			cancel()
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(150 * time.Second))
			raw, err := bufio.NewReader(io.LimitReader(conn, 128<<10+1)).ReadBytes('\n')
			if err != nil || len(raw) > 128<<10 {
				return
			}
			var req struct {
				Mode   string         `json:"mode"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(raw, &req) != nil {
				return
			}
			var result any
			requestCtx, requestCancel := context.WithTimeout(ctx, 140*time.Second)
			defer requestCancel()
			switch req.Mode {
			case "state":
				result, err = e.State()
			case "action":
				result, err = e.Action(requestCtx, req.Params)
			default:
				err = problem("Invalid request")
			}
			if err != nil {
				result = map[string]string{"error": safeError(err)}
			}
			_ = json.NewEncoder(conn).Encode(result)
		}()
	}
	cancel()
	wg.Wait()
	return nil
}
