package syncer

import (
	"context"
	"encoding/json"
	"github.com/PaNasMs/module-sdk/external"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRefreshRelayRotatesWithoutExposingProviderCredentials(t *testing.T) {
	e := fixture(t)
	a := Account{ID: accountID, Provider: "drive", Owner: "owner", Grant: strings.Repeat("a", 32)}
	access := external.Access{AccessToken: "first", TokenType: "Bearer", Scope: "https://www.googleapis.com/auth/drive", ExpiresAt: time.Now().Add(time.Hour)}
	e.tokens[a.ID] = lease{Access: access}
	e.Broker = func(context.Context, string) (external.Access, error) {
		access.AccessToken = "rotated"
		return access, nil
	}
	path, closeRelay, err := e.refreshConfig(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRelay()
	raw, _ := os.ReadFile(path)
	var address string
	var token map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "token_url = ") {
			address = strings.TrimPrefix(line, "token_url = ")
		}
		if strings.HasPrefix(line, "token = ") {
			json.Unmarshal([]byte(strings.TrimPrefix(line, "token = ")), &token)
		}
	}
	for _, secret := range []string{"wrong", token["refresh_token"].(string)} {
		res, err := http.PostForm(address, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {secret}})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if secret == "wrong" {
			if res.StatusCode != 403 {
				t.Fatal(res.StatusCode)
			}
			continue
		}
		if res.StatusCode != 200 || !strings.Contains(string(body), "rotated") {
			t.Fatalf("refresh failed: %d %s", res.StatusCode, body)
		}
	}
	closeRelay()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("run credential file retained")
	}
}
