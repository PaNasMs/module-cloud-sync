package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
