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

	"github.com/PaNasMs/module-sdk/external"
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

	for _, action := range []string{"account.save", "account.remove"} {
		if _, err := e.Action(ctx, map[string]any{"action": action, "id": accountID}); err == nil || !strings.Contains(err.Error(), "This account is busy") {
			t.Fatalf("active account mutation was not blocked: %s %v", action, err)
		}
	}
	if _, err := e.Action(ctx, map[string]any{"action": "account.save", "id": id, "provider": "drive", "grantId": strings.Repeat("b", 32)}); err != nil {
		t.Fatal("unrelated reconnect was blocked", err)
	}
	extra := newID()
	if _, err := e.DB.Exec("INSERT INTO accounts(id,provider,label,identity) VALUES(?,?,?,?)", extra, "local", "Unused", "unused"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Action(ctx, map[string]any{"action": "account.remove", "id": extra}); err != nil {
		t.Fatal("unrelated removal was blocked", err)
	}
	if transfers.Load() != 1 {
		t.Fatal("unrelated reconnect interrupted the transfer")
	}
	close(release)
	<-done
	e.cycle(ctx, next, map[string]int{}, func(bool) {})
	if transfers.Load() != 2 {
		t.Fatal("queued task did not run next", transfers.Load())
	}
}

func TestPollUsesReconnectedAccountAndSkipsOtherTasks(t *testing.T) {
	e := fixture(t)
	stale, err := e.account(accountID)
	if err != nil {
		t.Fatal(err)
	}
	grant := strings.Repeat("b", 32)
	if _, err := e.DB.Exec("UPDATE accounts SET provider='drive',grant_id=?,owner=?,cursor='fresh' WHERE id=?", grant, e.Owner, accountID); err != nil {
		t.Fatal(err)
	}
	originalBroker := e.Broker
	e.Broker = func(ctx context.Context, got string) (external.Access, error) {
		if got != grant {
			t.Errorf("worker used stale grant: %s", got)
		}
		return originalBroker(ctx, got)
	}
	task := addTask(t, e, "download")
	other := Task{ID: newID(), Account: newID()}
	e.HTTP = doFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.RawQuery, "fresh") {
			t.Errorf("stale cursor: %s", r.URL.RawQuery)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"newStartPageToken":"next","changes":[{"fileId":"changed"}]}`))}, nil
	})
	listed := false
	e.Runner = func(context.Context, Account, []string, func() error) ([]byte, error) {
		listed = true
		return []byte(`[]`), nil
	}
	if blocked := e.pollAccount(context.Background(), stale, []Task{other, task}, map[string]time.Time{}, map[string]int{}); blocked {
		t.Fatal("reconnected account blocked")
	}
	if !listed {
		t.Fatal("other account's task prevented listing this task")
	}
	current, _ := e.account(accountID)
	if current.Cursor != "next" {
		t.Fatal("fresh cursor not persisted", current.Cursor)
	}
}

func TestWorkerSkipsAccountBeingReconnected(t *testing.T) {
	e := fixture(t)
	held := addTask(t, e, "download")
	otherID := newID()
	if _, err := e.DB.Exec("INSERT INTO accounts(id,provider,label,identity) VALUES(?,?,?,?)", otherID, "local", "Other", "other"); err != nil {
		t.Fatal(err)
	}
	other := addTask(t, e, "download")
	if _, err := e.DB.Exec("UPDATE tasks SET account=? WHERE id=?", otherID, other.ID); err != nil {
		t.Fatal(err)
	}
	lock := e.accountLock(accountID)
	lock.Lock()
	defer lock.Unlock()
	e.Runner = func(_ context.Context, a Account, args []string, _ func() error) ([]byte, error) {
		if a.ID != otherID {
			t.Error("worker entered account being reconnected")
		}
		if args[0] == "lsjson" {
			return []byte(`[]`), nil
		}
		return nil, nil
	}
	next := map[string]time.Time{accountID: time.Now().Add(time.Hour), otherID: time.Now().Add(time.Hour)}
	e.cycle(context.Background(), next, map[string]int{}, func(bool) {})
	tasks, _ := e.tasks()
	for _, task := range tasks {
		if task.ID == held.ID && task.Status != "queued" {
			t.Fatal("held account task started", task)
		}
		if task.ID == other.ID && task.Status != "idle" {
			t.Fatal("unrelated task did not finish", task)
		}
	}
}
