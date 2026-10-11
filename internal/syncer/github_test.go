package syncer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/PaNasMs/module-sdk/external"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, b, err)
	}
	return strings.TrimSpace(string(b))
}

func TestRepositoryAllRefsAndLocalProtection(t *testing.T) {
	e := fixture(t)
	tgt := t.TempDir()
	src := t.TempDir()
	fixtureGit(t, src, "init", "-b", "main")
	os.WriteFile(filepath.Join(src, "data"), []byte("first"), 0664)
	fixtureGit(t, src, "add", ".")
	fixtureGit(t, src, "commit", "-m", "first")
	first := fixtureGit(t, src, "rev-parse", "HEAD")
	fixtureGit(t, src, "branch", "other")
	fixtureGit(t, src, "tag", "v1")
	execPath := fixtureGit(t, src, "--exec-path")
	server := httptest.NewServer(&cgi.Handler{Path: filepath.Join(execPath, "git-http-backend"), Env: []string{"GIT_PROJECT_ROOT=" + src, "GIT_HTTP_EXPORT_ALL=1"}})
	defer server.Close()
	task := Task{ID: newID(), Local: tgt}
	remote := server.URL + "/.git"
	update := func() error { return e.updateRepository(context.Background(), task, remote, "main", "test-secret") }
	if err := update(); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"HEAD", "refs/remotes/origin/other", "refs/tags/v1"} {
		if got := fixtureGit(t, tgt, "rev-parse", ref); got != first {
			t.Fatal(ref, got)
		}
	}
	config, _ := os.ReadFile(filepath.Join(tgt, ".git", "config"))
	if strings.Contains(string(config), "test-secret") {
		t.Fatal("credential persisted")
	}
	os.WriteFile(filepath.Join(src, "data"), []byte("second"), 0664)
	fixtureGit(t, src, "commit", "-am", "second")
	if err := update(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(tgt, "data"))
	if string(data) != "second" {
		t.Fatal(string(data))
	}
	history := fixtureGit(t, tgt, "for-each-ref", "--format=%(objectname)", "refs/panasms/history/")
	if !strings.Contains(history, first) {
		t.Fatal("old refs not retained")
	}
	os.WriteFile(filepath.Join(tgt, "data"), []byte("local edit"), 0664)
	if err := update(); err == nil {
		t.Fatal("local edits accepted")
	}
	data, _ = os.ReadFile(filepath.Join(tgt, "data"))
	if string(data) != "local edit" {
		t.Fatal("local edit lost")
	}
	fixtureGit(t, tgt, "checkout", "--", "data")
	fixtureGit(t, src, "reset", "--hard", first)
	if err := update(); err == nil {
		t.Fatal("remote rewind must not discard local history")
	}
	if got := fixtureGit(t, tgt, "rev-parse", "HEAD"); got == first {
		t.Fatal("remote rewind discarded tip")
	}

	fixtureGit(t, tgt, "checkout", "other")
	if err := update(); err != nil {
		t.Fatal(err)
	}
	if branch := fixtureGit(t, tgt, "branch", "--show-current"); branch != "other" {
		t.Fatal("changed branch", branch)
	}
	fixtureGit(t, src, "branch", "-D", "other")
	if err := update(); err == nil {
		t.Fatal("deleted branch must request attention")
	}
	if got := fixtureGit(t, tgt, "rev-parse", "HEAD"); got != first {
		t.Fatal("deleted branch lost", got)
	}
}

func TestRepositoryRefusesExistingFiles(t *testing.T) {
	e := fixture(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "keep"), []byte("safe"), 0664)
	if err := e.updateRepository(context.Background(), Task{ID: newID(), Local: dir}, "https://github.com/test/test.git", "main", ""); err == nil {
		t.Fatal("nonempty folder accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "keep")); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryUnchangedDoesNotReadArchive(t *testing.T) {
	e := fixture(t)
	e.Broker = func(context.Context, string) (external.Access, error) {
		return external.Access{AccessToken: "test-token", TokenType: "Bearer", Scope: "repo", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	_, err := e.DB.Exec("UPDATE accounts SET provider='github',grant_id=?,owner=? WHERE id=?", newID(), e.Owner, accountID)
	if err != nil {
		t.Fatal(err)
	}
	e.HTTP = doFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":123,"full_name":"owner/repo","default_branch":"main"}`))}, nil
	})
	dir := t.TempDir()
	fake := filepath.Join(dir, "git")
	if err = os.WriteFile(fake, []byte("#!/bin/sh\nprintf 'abc refs/heads/main\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	hash := sha256.Sum256([]byte("abc refs/heads/main\n"))
	task := addTask(t, e, "github")
	task.Remote = "owner/repo"
	task.Initialized = 1
	task.Snapshot = hex.EncodeToString(hash[:])
	if _, err = e.DB.Exec("INSERT INTO git_tasks(task,repository_id) VALUES(?,123)", task.ID); err != nil {
		t.Fatal(err)
	}
	e.Mount = func(string) (string, error) { t.Fatal("unchanged backup probed archive mount"); return "", nil }
	e.Local = func(string) (string, error) { t.Fatal("unchanged backup read local folder"); return "", nil }
	if err = e.runRepositoryTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPauseInterruptsRemoteProbe(t *testing.T) {
	e := fixture(t)
	e.Broker = func(context.Context, string) (external.Access, error) {
		return external.Access{AccessToken: "test-token", TokenType: "Bearer", Scope: "repo", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	_, err := e.DB.Exec("UPDATE accounts SET provider='github',grant_id=?,owner=? WHERE id=?", newID(), e.Owner, accountID)
	if err != nil {
		t.Fatal(err)
	}
	e.HTTP = doFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":123,"full_name":"owner/repo","default_branch":"main"}`))}, nil
	})
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nsleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	task := addTask(t, e, "github")
	task.Remote = "owner/repo"
	if _, err = e.DB.Exec("INSERT INTO git_tasks(task,repository_id) VALUES(?,123)", task.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.runRepositoryTask(context.Background(), task) }()
	time.Sleep(100 * time.Millisecond)
	if _, err = e.taskAction("task.pause", task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != errPaused {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pause did not cancel remote probe")
	}
}

func TestRepositoryListLoadsTwoPages(t *testing.T) {
	e := fixture(t)
	e.Broker = func(context.Context, string) (external.Access, error) {
		return external.Access{AccessToken: "test", TokenType: "Bearer", Scope: "repo", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	if _, err := e.DB.Exec("UPDATE accounts SET provider='github',grant_id=?,owner=? WHERE id=?", newID(), e.Owner, accountID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.HTTP = doFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/user/repos" || r.URL.Query().Get("page") != fmt.Sprint(calls) || r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("unexpected request: %s", r.URL)
		}
		batch := make([]repository, 100)
		for i := range batch {
			batch[i] = repository{ID: int64((calls-1)*100 + i + 1), Name: fmt.Sprintf("org/repo-%d-%d", calls, i)}
		}
		data, _ := json.Marshal(batch)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	result, err := e.repositories(context.Background(), map[string]any{"account": accountID})
	if err != nil {
		t.Fatal(err)
	}
	repos := result.(map[string]any)["repositories"].([]repository)
	if calls != 2 || len(repos) != 200 || repos[199].ID != 200 {
		t.Fatalf("calls=%d repos=%d", calls, len(repos))
	}
}

func TestRepositorySharedScheduleAndRestart(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{"2026-10-11", "2026-10-25", "2026-03-29"} {
		t.Run(day, func(t *testing.T) {
			e := fixture(t)
			morning, err := time.ParseInLocation("2006-01-02 15:04", day+" 03:00", loc)
			if err != nil {
				t.Fatal(err)
			}
			a := addTask(t, e, "github")
			b := addTask(t, e, "github")
			paused := addTask(t, e, "github")
			cloud := addTask(t, e, "download")
			for _, id := range []string{a.ID, b.ID, paused.ID, cloud.ID} {
				if _, err = e.DB.Exec("UPDATE tasks SET initialized=1,status='idle',dirty=0,last_sync=? WHERE id=?", morning.Add(-time.Hour).Unix(), id); err != nil {
					t.Fatal(err)
				}
			}
			e.DB.Exec("UPDATE tasks SET paused=1 WHERE id=?", paused.ID)
			check := func(id string, want int) {
				t.Helper()
				var got int
				if err := e.DB.QueryRow("SELECT dirty FROM tasks WHERE id=?", id).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("dirty=%d want=%d", got, want)
				}
			}
			if err = e.scheduleRepositories(morning.Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			check(a.ID, 0)
			if err = e.scheduleRepositories(morning); err != nil {
				t.Fatal(err)
			}
			check(a.ID, 1)
			check(b.ID, 1)
			check(paused.ID, 0)
			check(cloud.ID, 0)
			e.DB.Exec("UPDATE tasks SET dirty=0,last_sync=? WHERE id IN (?,?)", morning.Unix(), a.ID, b.ID)
			if err = e.initialize(); err != nil {
				t.Fatal(err)
			}
			check(a.ID, 0)
			check(b.ID, 0)
			fresh := addTask(t, e, "github")
			check(fresh.ID, 1)
			afternoon := time.Date(morning.Year(), morning.Month(), morning.Day(), 15, 0, 0, 0, loc)
			if err = e.scheduleRepositories(afternoon.Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			check(a.ID, 0)
			if err = e.scheduleRepositories(afternoon.Add(2 * time.Hour)); err != nil {
				t.Fatal(err)
			}
			check(a.ID, 1)
			check(b.ID, 1)
			check(paused.ID, 0)
			e.DB.Exec("UPDATE tasks SET dirty=0,last_sync=? WHERE id IN (?,?)", afternoon.Add(2*time.Hour).Unix(), a.ID, b.ID)
			if err = e.scheduleRepositories(afternoon.Add(3 * time.Hour)); err != nil {
				t.Fatal(err)
			}
			check(a.ID, 0)
		})
	}
}
