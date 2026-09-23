package syncer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func (e *Engine) Action(ctx context.Context, p map[string]any) (any, error) {
	kind := str(p, "action")
	if kind == "local.folders" {
		return e.localFolders(str(p, "path"))
	}
	if kind == "local.mkdir" {
		return e.createLocalFolder(str(p, "path"), str(p, "name"))
	}
	if strings.HasPrefix(kind, "task.") && kind != "task.create" {
		return e.taskAction(kind, str(p, "id"))
	}
	if !e.cloudMu.TryLock() {
		return nil, problem("A transfer is running. Pause it before changing connections or browsing cloud folders.")
	}
	defer e.cloudMu.Unlock()
	switch kind {
	case "account.save":
		return e.saveAccount(ctx, p)
	case "account.remove":
		id := str(p, "id")
		if !validID(id) {
			return nil, problem("Invalid account")
		}
		var count int
		if err := e.DB.QueryRow("SELECT count(*) FROM tasks WHERE account=?", id).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, problem("Remove tasks before disconnecting this account")
		}
		if _, err := e.DB.Exec("DELETE FROM accounts WHERE id=?", id); err != nil {
			return nil, err
		}
		delete(e.tokens, id)
		for _, root := range []string{e.Root, e.Runtime} {
			if err := os.Remove(filepath.Join(root, id+".conf")); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
		return map[string]any{}, nil
	case "folders":
		a, err := e.account(str(p, "account"))
		if err != nil {
			return nil, err
		}
		path, err := remoteFolder(str(p, "path"))
		if err != nil {
			return nil, err
		}
		if _, err = e.ensure(ctx, a, true); err != nil {
			return nil, err
		}
		raw, err := e.Runner(ctx, a, []string{"lsjson", "cloud:" + path, "--dirs-only"}, nil)
		if err != nil {
			return nil, err
		}
		var entries []Entry
		if err = json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
		folders := []map[string]string{}
		for _, v := range entries {
			folders = append(folders, map[string]string{"name": v.Name, "path": strings.Trim(path+"/"+v.Name, "/")})
		}
		return map[string]any{"folders": folders}, nil
	case "task.create":
		a, err := e.account(str(p, "account"))
		if err != nil {
			return nil, err
		}
		local, err := e.Local(str(p, "local"))
		if err != nil {
			return nil, err
		}
		mount, err := e.Mount(local)
		if err != nil {
			return nil, err
		}
		remote, err := remoteFolder(str(p, "remote"))
		if err != nil {
			return nil, err
		}
		direction := str(p, "direction")
		if direction != "upload" && direction != "download" && direction != "both" {
			return nil, problem("Invalid sync direction")
		}
		tasks, err := e.tasks()
		if err != nil {
			return nil, err
		}
		if len(tasks) >= 32 {
			return nil, problem("Prototype supports up to 32 tasks per user")
		}
		for _, t := range tasks {
			if overlap(local, t.Local) {
				return nil, problem("Local sync folders must not overlap")
			}
			if a.ID == t.Account && overlap(remote, t.Remote) {
				return nil, problem("Cloud sync folders must not overlap")
			}
		}
		if _, err = e.ensure(ctx, a, true); err != nil {
			return nil, err
		}
		if a.Provider == "drive" && direction != "download" && e.tokens[a.ID].Access.Scope != "https://www.googleapis.com/auth/drive" {
			return nil, problem("This permission is read-only. Authorize full Drive access before uploading.")
		}
		if _, _, err = e.listing(ctx, a, remote); err != nil {
			return nil, err
		}
		name := strings.TrimSpace(str(p, "name"))
		if name == "" {
			name = filepath.Base(local)
		}
		if len([]rune(name)) > 100 {
			return nil, problem("Task name is too long")
		}
		id := newID()
		if _, err = e.DB.Exec("INSERT INTO tasks(id,account,name,local,remote,direction,mount) VALUES(?,?,?,?,?,?,?)", id, a.ID, name, local, remote, direction, mount); err != nil {
			return nil, err
		}
		if err = e.event(id, "created", "Task created"); err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, nil
	default:
		return nil, problem("Unknown action")
	}
}
func (e *Engine) taskAction(kind, id string) (any, error) {
	if !validID(id) {
		return nil, problem("Task not found")
	}
	var result int64
	switch kind {
	case "task.pause":
		r, err := e.DB.Exec("UPDATE tasks SET paused=1 WHERE id=?", id)
		if err != nil {
			return nil, err
		}
		result, _ = r.RowsAffected()
	case "task.remove":
		r, err := e.DB.Exec("DELETE FROM tasks WHERE id=? AND status!='running'", id)
		if err != nil {
			return nil, err
		}
		result, _ = r.RowsAffected()
		if result > 0 {
			_ = e.event(id, "removed", "Task removed; local and cloud files preserved")
		}
	case "task.resume", "task.run":
		r, err := e.DB.Exec("UPDATE tasks SET paused=0,dirty=1,status='queued',error='' WHERE id=? AND status!='running'", id)
		if err != nil {
			return nil, err
		}
		result, _ = r.RowsAffected()
	default:
		return nil, problem("Unknown action")
	}
	if result == 0 {
		return nil, problem("Task is unavailable or still running. Pause it and wait for the transfer to stop.")
	}
	return map[string]any{}, nil
}
