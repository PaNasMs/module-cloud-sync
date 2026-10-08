package syncer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PaNasMs/module-sdk/external"
)

// Opt-in acceptance uses a short-lived runtime token, never refresh credentials.
func TestDropboxLive(t *testing.T) {
	config := os.Getenv("PANASMS_TEST_DROPBOX_CONFIG")
	if config == "" {
		t.Skip("requires explicit Dropbox test consent and runtime token")
	}
	remote := "PaNasMs-acceptance-" + time.Now().UTC().Format("20060102T150405")
	e := fixture(t)
	e.Broker = func(context.Context, string) (external.Access, error) {
		raw, err := os.ReadFile(config)
		if err != nil {
			return external.Access{}, err
		}
		var token struct {
			Access string    `json:"access_token"`
			Expiry time.Time `json:"expiry"`
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "token = ") {
				err = json.Unmarshal([]byte(strings.TrimPrefix(line, "token = ")), &token)
				break
			}
		}
		return external.Access{AccessToken: token.Access, TokenType: "Bearer", ExpiresAt: token.Expiry, Scope: "account_info.read files.metadata.read files.content.read files.content.write"}, err
	}
	e.DB.Exec("UPDATE accounts SET provider='dropbox',grant_id=?,owner='owner'", strings.Repeat("f", 32))
	a, _ := e.account(accountID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := e.ensure(ctx, a, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(ctx, a, []string{"mkdir", "cloud:" + remote}, nil); err != nil {
		t.Fatal(err)
	}
	task := addTask(t, e, "both")
	task.Remote = remote
	e.DB.Exec("UPDATE tasks SET remote=? WHERE id=?", remote, task.ID)
	content := []byte("PaNasMs Dropbox upload acceptance — UTF-8 — Україна\n")
	os.WriteFile(filepath.Join(task.Local, "from-nas.txt"), content, 0600)
	if err := e.runTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	raw, err := e.run(ctx, a, []string{"cat", "cloud:" + remote + "/from-nas.txt"}, nil)
	if err != nil || string(raw) != string(content) {
		t.Fatal("upload content mismatch", err)
	}
	t.Log("PASS NAS to Dropbox", remote)
	fromCloud := filepath.Join(t.TempDir(), "from-cloud.txt")
	os.WriteFile(fromCloud, []byte("Dropbox download acceptance\n"), 0600)
	if _, err = e.run(ctx, a, []string{"copyto", fromCloud, "cloud:" + remote + "/from-cloud.txt"}, nil); err != nil {
		t.Fatal(err)
	}
	tasks, _ := e.tasks()
	task = tasks[0]
	if err = e.runTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(task.Local, "from-cloud.txt"))
	if err != nil || string(raw) != "Dropbox download acceptance\n" {
		t.Fatal("download content mismatch", err)
	}
	t.Log("PASS Dropbox to NAS through bisync; remote test folder retained", remote)
}
