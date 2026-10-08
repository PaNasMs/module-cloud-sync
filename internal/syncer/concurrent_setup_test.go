package syncer

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSetupDuringTransfer(t *testing.T) {
	e := fixture(t)
	first := addTask(t, e, "download")
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var transfers atomic.Int32
	e.Runner = func(ctx context.Context, a Account, args []string, check func() error) ([]byte, error) {
		if args[0] == "lsjson" {
			return []byte(`[]`), nil
		}
		if transfers.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, nil
	}
	e.HTTP = doFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"startPageToken":"cursor"}`
		if strings.Contains(r.URL.Path, "about") {
			body = `{"user":{"permissionId":"new-user"}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	next := map[string]time.Time{accountID: time.Now().Add(time.Hour)}
	go func() { defer close(done); e.cycle(ctx, next, map[string]int{}, func(bool) {}) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("transfer never started")
	}
	saved, err := e.Action(ctx, map[string]any{"action": "account.save", "provider": "drive", "grantId": strings.Repeat("a", 32), "label": "Second account"})
	if err != nil {
		t.Fatal("cannot connect while transferring", err)
	}
	id := saved.(map[string]string)["id"]
	for _, account := range []string{accountID, id} {
		if _, err = e.Action(ctx, map[string]any{"action": "folders", "account": account}); err != nil {
			t.Fatal("cannot browse while transferring", err)
		}
		if _, err = e.Action(ctx, map[string]any{"action": "task.create", "account": account, "local": t.TempDir(), "remote": "new-folder", "direction": "download"}); err != nil {
			t.Fatal("cannot queue task", err)
		}
	}
	tasks, _ := e.tasks()
	if len(tasks) != 3 {
		t.Fatal(tasks)
	}
	for _, task := range tasks {
		if task.ID != first.ID && task.Status != "queued" {
			t.Fatal("new task not queued", task)
		}
	}
	if transfers.Load() != 1 {
		t.Fatal("parallel transfer started")
	}
	entries, _ := os.ReadDir(e.Runtime)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "setup-") {
			t.Fatal("setup credentials leaked", filepath.Join(e.Runtime, entry.Name()))
		}
	}
	close(release)
	<-done
	e.cycle(ctx, next, map[string]int{}, func(bool) {})
	if transfers.Load() != 2 {
		t.Fatal("queued task did not run next", transfers.Load())
	}
}
