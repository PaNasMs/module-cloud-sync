package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var repositoryName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$`)

type repository struct {
	ID      int64  `json:"id"`
	Name    string `json:"full_name"`
	Branch  string `json:"default_branch"`
	Private bool   `json:"private"`
}

func (e *Engine) github(ctx context.Context, a Account, path string, result any) error {
	token, err := e.token(ctx, a)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := e.HTTP.Do(req)
	if err != nil {
		return problem("Cloud is unreachable; retry later.")
	}
	defer res.Body.Close()
	if res.StatusCode == 401 {
		return &Failure{"Account authorization expired or access was denied. Reconnect the account.", true}
	}
	if res.StatusCode == 403 || res.StatusCode == 429 {
		return problem("GitHub denied access or rate-limited this request. Check repository access and try again later.")
	}
	if res.StatusCode == 404 {
		return problem("GitHub repository is unavailable. The local copy has been preserved.")
	}
	if res.StatusCode != 200 {
		return problem("Cloud is unreachable; retry later.")
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(result)
}

func (e *Engine) repositories(ctx context.Context, p map[string]any) (any, error) {
	a, err := e.account(str(p, "account"))
	if err != nil || a.Provider != "github" {
		return nil, problem("Invalid account")
	}
	query := strings.TrimSpace(str(p, "query"))
	if len(query) > 128 {
		return nil, problem("Select a GitHub repository")
	}
	repos := []repository{}
	if query == "" {
		err = e.github(ctx, a, "user/repos?per_page=50&sort=full_name", &repos)
		return map[string]any{"repositories": repos}, err
	}
	// GitHub search includes visible public repositories and authorized private ones.
	// Qualify owner/name input so it also finds repositories outside the initial 50.
	if owner, name, ok := strings.Cut(query, "/"); ok && regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`).MatchString(owner) {
		query = "user:" + owner + " " + name
	}
	var result struct {
		Items      []repository `json:"items"`
		Total      int          `json:"total_count"`
		Incomplete bool         `json:"incomplete_results"`
	}
	err = e.github(ctx, a, "search/repositories?per_page=50&q="+url.QueryEscape(query+" in:name fork:true"), &result)
	if result.Items == nil {
		result.Items = repos
	}
	return map[string]any{"repositories": result.Items, "more": result.Total > len(result.Items) || result.Incomplete}, err
}

func (e *Engine) createRepositoryTask(ctx context.Context, a Account, p map[string]any) (any, error) {
	remote := str(p, "remote")
	if !repositoryName.MatchString(remote) || strings.HasSuffix(remote, "/.") || strings.HasSuffix(remote, "/..") {
		return nil, problem("Select a GitHub repository")
	}
	local, err := e.Local(str(p, "local"))
	if err != nil {
		return nil, err
	}
	mount, err := e.Mount(local)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(local)
	if err != nil {
		return nil, err
	}
	if len(entries) != 0 {
		return nil, problem("Choose an empty local folder for the repository")
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
		if t.Account == a.ID && strings.EqualFold(t.Remote, remote) {
			return nil, problem("This repository already has a synchronization task")
		}
	}
	var repo repository
	if err = e.github(ctx, a, "repos/"+remote, &repo); err != nil {
		return nil, err
	}
	if repo.ID <= 0 || !strings.EqualFold(repo.Name, remote) {
		return nil, problem("Select a GitHub repository")
	}
	name := strings.TrimSpace(str(p, "name"))
	if name == "" {
		name = repo.Name
	}
	if len([]rune(name)) > 100 {
		return nil, problem("Task name is too long")
	}
	id := newID()
	tx, err := e.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO tasks(id,account,name,local,remote,direction,mount) VALUES(?,?,?,?,?,'github',?)", id, a.ID, name, local, repo.Name, stableMount(mount))
	if err == nil {
		_, err = tx.Exec("INSERT INTO git_tasks(task,repository_id) VALUES(?,?)", id, repo.ID)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	_ = e.event(id, "created", "Repository task created")
	return map[string]string{"id": id}, nil
}

// No shell, credential persistence, hooks, global configuration or automatic GC.
func gitCommand(ctx context.Context, dir, token string, args ...string) ([]byte, error) {
	options := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "submodule.recurse=false", "-c", "credential.helper=", "-c", "http.followRedirects=false", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.http.allow=always"}
	cmd := exec.CommandContext(ctx, "git", append(options, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "LC_ALL=C", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	out := &limitedBuffer{limit: 8 << 20}
	cmd.Stdout = out
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, problem("Git operation failed. Check connectivity, repository access, local changes and free space, then retry.")
	}
	if out.Exceeded() {
		return nil, problem("Repository reference list is too large")
	}
	return out.b.Bytes(), nil
}

func (e *Engine) runRepositoryTask(parent context.Context, t Task) (result error) {
	ctx, cancel := context.WithTimeout(parent, 24*time.Hour)
	defer cancel()
	pauseWatch := make(chan struct{})
	defer close(pauseWatch)
	defer func() {
		if ctx.Err() != nil {
			var paused int
			if e.DB.QueryRow("SELECT paused FROM tasks WHERE id=?", t.ID).Scan(&paused) == nil && paused != 0 {
				result = errPaused
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-pauseWatch:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				var paused int
				if e.DB.QueryRow("SELECT paused FROM tasks WHERE id=?", t.ID).Scan(&paused) != nil || paused != 0 {
					cancel()
					return
				}
			}
		}
	}()
	a, err := e.account(t.Account)
	if err != nil {
		return err
	}
	var expected int64
	if err = e.DB.QueryRow("SELECT repository_id FROM git_tasks WHERE task=?", t.ID).Scan(&expected); err != nil {
		return err
	}
	var repo repository
	if err = e.github(ctx, a, "repos/"+t.Remote, &repo); err != nil {
		return err
	}
	if repo.ID != expected {
		return problem("GitHub repository identity changed. The local copy has been preserved.")
	}
	token, err := e.token(ctx, a)
	if err != nil {
		return err
	}
	remote := "https://github.com/" + t.Remote + ".git"
	// The working directory is on the runtime filesystem, never the sleeping archive.
	probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer probeCancel()
	refs, err := gitCommand(probeCtx, e.Runtime, token, "ls-remote", "--symref", remote, "HEAD", "refs/heads/*", "refs/tags/*")
	if err != nil {
		return err
	}
	hash := sha256.Sum256(refs)
	snapshot := hex.EncodeToString(hash[:])
	if t.Initialized == 0 || snapshot != t.Snapshot {
		if !e.originalVolume(t) {
			return problem("Local volume changed or is unavailable; reconnect the original volume")
		}
		if _, err = e.Local(t.Local); err != nil {
			return err
		}
		done := make(chan struct{})
		defer close(done)
		go func() {
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					return
				case <-tick.C:
					var paused int
					if e.DB.QueryRow("SELECT paused FROM tasks WHERE id=?", t.ID).Scan(&paused) != nil || paused != 0 || !e.originalVolume(t) {
						cancel()
						return
					}
				}
			}
		}()
		if err = e.updateRepository(ctx, t, remote, repo.Branch, token); err != nil {
			var paused int
			_ = e.DB.QueryRow("SELECT paused FROM tasks WHERE id=?", t.ID).Scan(&paused)
			if paused != 0 {
				return errPaused
			}
			return err
		}
		current, branchErr := gitCommand(ctx, t.Local, token, "symbolic-ref", "--quiet", "--short", "HEAD")
		if branchErr == nil {
			if _, err = e.DB.Exec("UPDATE git_tasks SET branch=? WHERE task=?", strings.TrimSpace(string(current)), t.ID); err != nil {
				return err
			}
		}
	}
	_, err = e.DB.Exec("UPDATE tasks SET status='idle',initialized=1,snapshot=?,last_sync=?,error='',recovery='',retry_at=0,retry_count=0 WHERE id=?", snapshot, time.Now().Unix(), t.ID)
	if err == nil && (t.Initialized == 0 || snapshot != t.Snapshot) {
		err = e.event(t.ID, "success", "Repository synchronized; all remote branches and tags fetched")
	}
	return err
}

func (e *Engine) updateRepository(ctx context.Context, t Task, remote, branch, token string) error {
	folder, err := os.Open(t.Local)
	if err != nil {
		return err
	}
	defer folder.Close()
	if err = syscall.Flock(int(folder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return problem("Repository is busy; retry after the file manager finishes")
	}

	run := func(args ...string) ([]byte, error) { return gitCommand(ctx, t.Local, token, args...) }
	meta := filepath.Join(t.Local, ".git")
	info, err := os.Lstat(meta)
	if os.IsNotExist(err) {
		entries, e := os.ReadDir(t.Local)
		if e != nil {
			return e
		}
		if len(entries) != 0 {
			return problem("Choose an empty local folder for the repository")
		}
		if _, err = run("init"); err != nil {
			return err
		}
		if _, err = run("config", "panasms.task", t.ID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return problem("The repository metadata is not a local directory")
	}
	marker, err := run("config", "--get", "panasms.task")
	if err != nil || strings.TrimSpace(string(marker)) != t.ID {
		return problem("This folder is not the repository created by this task")
	}
	dirty, err := run("status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if len(dirty) > 0 {
		return problem("Local repository changes need attention. Commit or move them, then resume synchronization.")
	}
	old, err := run("for-each-ref", "--format=%(objectname) %(refname)", "refs/remotes/origin/", "refs/tags/")
	if err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405") + "-" + newID()[:8]
	for _, line := range strings.Split(strings.TrimSpace(string(old)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if _, err = run("update-ref", "refs/panasms/history/"+stamp+"/"+strings.TrimPrefix(fields[1], "refs/"), fields[0]); err != nil {
			return err
		}
	}
	if _, err = run("config", "remote.origin.url", remote); err != nil {
		return err
	}
	if _, err = run("config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return err
	}
	if _, err = run("fetch", "--prune", "--no-recurse-submodules", "--no-write-fetch-head", remote, "+refs/heads/*:refs/remotes/origin/*", "+refs/tags/*:refs/tags/*"); err != nil {
		return err
	}
	_, headErr := run("rev-parse", "--verify", "HEAD")
	if headErr != nil {
		available, refsErr := run("for-each-ref", "--format=%(refname)", "refs/remotes/origin/", "refs/tags/")
		if refsErr != nil {
			return refsErr
		}
		if len(available) == 0 {
			return nil
		}
		if branch == "" {
			return nil
		}
		if _, err = run("check-ref-format", "--branch", branch); err != nil {
			return err
		}
		if _, err = run("checkout", "-b", branch, "--track", "origin/"+branch); err != nil {
			return err
		}
	} else {
		current, err := run("symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil {
			return problem("The repository has a detached HEAD. Select a branch and resume synchronization.")
		}
		ref := "refs/remotes/origin/" + strings.TrimSpace(string(current))
		if _, err = run("rev-parse", "--verify", ref); err != nil {
			return problem("The checked-out branch no longer exists on GitHub. Local files and history were preserved.")
		}
		if _, err = run("merge-base", "--is-ancestor", "HEAD", ref); err != nil {
			return problem("The local branch cannot fast-forward. Local files and previous remote history were preserved.")
		}
		if _, err = run("merge", "--ff-only", "--no-edit", ref); err != nil {
			return problem("The local branch cannot fast-forward. Local files and previous remote history were preserved.")
		}
	}
	return nil
}
