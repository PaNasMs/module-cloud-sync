package syncer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/PaNasMs/module-sdk/external"
)

type Broker func(context.Context, string) (external.Access, error)
type lease struct {
	Access  external.Access
	Checked time.Time
}
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

func defaultHTTP() *http.Client {
	return &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (e *Engine) config(a Account) string {
	if a.Grant != "" {
		return filepath.Join(e.Runtime, a.ID+".conf")
	}
	return filepath.Join(e.Root, a.ID+".conf")
}
func (e *Engine) ensure(ctx context.Context, a Account, force bool) (bool, error) {
	if a.Provider != "drive" {
		return false, nil
	}
	if a.Grant == "" || !validGrant(a.Grant) || a.Owner != e.Owner {
		return false, &Failure{"Reconnect this account to grant Drive access", true}
	}
	old, ok := e.tokens[a.ID]
	if !force && ok && time.Since(old.Checked) < 30*time.Second && old.Access.ExpiresAt.After(time.Now().Add(10*time.Second)) {
		return false, nil
	}
	access, err := e.Broker(ctx, a.Grant)
	if err != nil {
		delete(e.tokens, a.ID)
		_ = os.Remove(e.config(a))
		var failure *external.Error
		if errors.As(err, &failure) && (failure.Status == 403 || failure.Status == 409) {
			return false, &Failure{"Drive access is unavailable. Reconnect this account or restore its permission.", true}
		}
		return false, problem("Cloud access service is unavailable; retrying later.")
	}
	if access.AccessToken == "" || access.TokenType != "Bearer" || !access.ExpiresAt.After(time.Now().Add(10*time.Second)) {
		return false, problem("Cloud access service returned an invalid token.")
	}
	changed := !ok || old.Access.AccessToken != access.AccessToken
	if changed {
		v := map[string]any{"access_token": access.AccessToken, "token_type": "Bearer", "expiry": access.ExpiresAt}
		if err = atomicFile(e.config(a), []byte("[cloud]\ntype = drive\ntoken = "+marshal(v)+"\n")); err != nil {
			return false, err
		}
	}
	e.tokens[a.ID] = lease{access, time.Now()}
	return ok && changed, nil
}
func readToken(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(io.LimitReader(f, 128<<10))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "token = ") {
			var v map[string]any
			err = json.Unmarshal([]byte(strings.TrimPrefix(line, "token = ")), &v)
			return v, err
		}
	}
	return nil, problem("Account authorization is unavailable. Reconnect the account.")
}
func (e *Engine) token(ctx context.Context, a Account) (string, error) {
	if a.Provider == "drive" {
		if _, err := e.ensure(ctx, a, false); err != nil {
			return "", err
		}
		return e.tokens[a.ID].Access.AccessToken, nil
	}
	v, err := readToken(e.config(a))
	if err != nil {
		return "", err
	}
	expiry, _ := time.Parse(time.RFC3339, str(v, "expiry"))
	if !expiry.After(time.Now().Add(120 * time.Second)) {
		if _, err = e.Runner(ctx, a, []string{"lsd", "cloud:", "--max-depth", "1"}, nil); err != nil {
			return "", err
		}
		v, err = readToken(e.config(a))
		if err != nil {
			return "", err
		}
	}
	t := str(v, "access_token")
	if t == "" {
		return "", problem("Account authorization is unavailable. Reconnect the account.")
	}
	return t, nil
}
func (e *Engine) api(ctx context.Context, a Account, address string, body any) (map[string]any, error) {
	token, err := e.token(ctx, a)
	if err != nil {
		return nil, err
	}
	var data io.Reader
	method := "GET"
	if body != nil || a.Provider == "dropbox" {
		data = strings.NewReader(marshal(body))
		method = "POST"
	}
	req, err := http.NewRequestWithContext(ctx, method, address, data)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := e.HTTP.Do(req)
	if err != nil {
		return nil, problem("Cloud is unreachable; retry later.")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		switch res.StatusCode {
		case 401, 403:
			body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
			if a.Provider == "drive" && (bytes.Contains(body, []byte("accessNotConfigured")) || bytes.Contains(body, []byte("SERVICE_DISABLED"))) {
				return nil, problem("Enable Google Drive API in the NAS Google Cloud project, then reconnect this account.")
			}
			return nil, problem("Account authorization expired or access was denied. Reconnect the account.")
		case 409, 410:
			return nil, problem("Cloud change cursor expired. Reconnect the account to rebuild it.")
		case 429:
			return nil, problem("Cloud rate limit reached. Checks will resume with backoff.")
		}
		return nil, problem("Cloud request failed; retry later.")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 4<<20 {
		return nil, problem("Cloud response is too large")
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		return nil, problem("Cloud returned an invalid response.")
	}
	return result, nil
}
func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func (e *Engine) initialCursor(ctx context.Context, a Account) (string, error) {
	address := "https://www.googleapis.com/drive/v3/changes/startPageToken"
	key := "startPageToken"
	var body any
	if a.Provider == "dropbox" {
		address = "https://api.dropboxapi.com/2/files/list_folder/get_latest_cursor"
		key = "cursor"
		body = map[string]any{"path": "", "recursive": true, "include_deleted": true}
	}
	v, err := e.api(ctx, a, address, body)
	if err != nil {
		return "", err
	}
	if str(v, key) == "" {
		return "", problem("Cloud returned an invalid change cursor.")
	}
	return str(v, key), nil
}
func (e *Engine) poll(ctx context.Context, a Account) (string, bool, error) {
	cursor := a.Cursor
	changed := false
	if cursor == "" {
		c, err := e.initialCursor(ctx, a)
		return c, true, err
	}
	for range 100 {
		var v map[string]any
		var err error
		more := false
		if a.Provider == "drive" {
			v, err = e.api(ctx, a, "https://www.googleapis.com/drive/v3/changes?"+url.Values{"pageToken": {cursor}, "pageSize": {"1000"}, "fields": {"nextPageToken,newStartPageToken,changes(fileId,removed)"}}.Encode(), nil)
			if err != nil {
				return "", false, err
			}
			changes, _ := v["changes"].([]any)
			changed = changed || len(changes) > 0
			cursor = str(v, "nextPageToken")
			more = cursor != ""
			if !more {
				cursor = str(v, "newStartPageToken")
			}
		} else {
			v, err = e.api(ctx, a, "https://api.dropboxapi.com/2/files/list_folder/continue", map[string]string{"cursor": cursor})
			if err != nil {
				return "", false, err
			}
			entries, _ := v["entries"].([]any)
			changed = changed || len(entries) > 0
			cursor = str(v, "cursor")
			more, _ = v["has_more"].(bool)
		}
		if cursor == "" {
			return "", false, problem("Cloud returned an invalid change cursor.")
		}
		if !more {
			return cursor, changed, nil
		}
	}
	return "", false, problem("Too many cloud changes; manual reconciliation is required.")
}

type Entry struct {
	Path, Name, ModTime string
	Size                int64
	IsDir               bool
	Hashes              map[string]string
}

func (e *Engine) listing(ctx context.Context, a Account, remote string) (string, []Entry, error) {
	raw, err := e.Runner(ctx, a, []string{"lsjson", "cloud:" + remote, "-R", "--hash"}, nil)
	if err != nil {
		return "", nil, err
	}
	var entries []Entry
	if json.Unmarshal(raw, &entries) != nil {
		return "", nil, problem("Cloud returned an invalid folder listing.")
	}
	if len(entries) > 100000 {
		return "", nil, problem("Prototype supports up to 100000 entries per task")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	var stable []any
	for i, en := range entries {
		if i > 0 && entries[i-1].Path == en.Path {
			return "", nil, problem("Duplicate cloud filenames must be resolved first")
		}
		stable = append(stable, []any{en.Path, en.Size, en.ModTime, en.IsDir, en.Hashes})
	}
	sum := sha256.Sum256([]byte(marshal(stable)))
	return hex.EncodeToString(sum[:]), entries, nil
}
func (e *Engine) saveAccount(ctx context.Context, p map[string]any) (any, error) {
	provider := str(p, "provider")
	if provider != "drive" && provider != "dropbox" {
		return nil, problem("Unsupported provider")
	}
	id := str(p, "id")
	var previous Account
	var err error
	if id != "" {
		if !validID(id) {
			return nil, problem("Invalid account")
		}
		previous, err = e.account(id)
		if err != nil || previous.Provider != provider {
			return nil, problem("Account not found")
		}
	} else {
		id = newID()
	}
	label := strings.TrimSpace(str(p, "label"))
	if len([]rune(label)) > 100 {
		return nil, problem("Connection name is too long")
	}
	a := Account{ID: id, Provider: provider, Label: label}
	if provider == "drive" {
		a.Grant = str(p, "grantId")
		a.Owner = e.Owner
		if previous.Owner != "" && previous.Owner != a.Owner {
			return nil, problem("Reconnect Drive using your own account")
		}
	}
	oldToken, hadToken := e.tokens[id]
	oldConfig, oldErr := os.ReadFile(e.config(a))
	committed := false
	defer func() {
		if !committed {
			if hadToken {
				e.tokens[id] = oldToken
			} else {
				delete(e.tokens, id)
			}
			if oldErr == nil {
				_ = atomicFile(e.config(a), oldConfig)
			} else {
				_ = os.Remove(e.config(a))
			}
		}
	}()
	if provider == "drive" {
		delete(e.tokens, id)
		if _, err = e.ensure(ctx, a, true); err != nil {
			return nil, err
		}
	} else {
		auth, _ := p["authorization"].(map[string]any)
		if str(auth, "provider") != "dropbox" {
			return nil, problem("Authorization file belongs to another provider")
		}
		tok, _ := auth["token"].(map[string]any)
		if str(tok, "access_token") == "" || str(tok, "refresh_token") == "" {
			return nil, problem("Authorization file must include access and refresh tokens")
		}
		var content bytes.Buffer
		fmt.Fprintf(&content, "[cloud]\ntype = dropbox\ntoken = %s\n", marshal(tok))
		for _, key := range []string{"client_id", "client_secret"} {
			if value, exists := auth[key]; exists {
				s, ok := value.(string)
				if !ok || strings.ContainsAny(s, "\r\n") {
					return nil, problem("Invalid OAuth client setting")
				}
				fmt.Fprintf(&content, "%s = %s\n", key, s)
			}
		}
		if err = atomicFile(e.config(a), content.Bytes()); err != nil {
			return nil, err
		}
	}
	address := "https://www.googleapis.com/drive/v3/about?fields=user"
	if provider == "dropbox" {
		address = "https://api.dropboxapi.com/2/users/get_current_account"
	}
	data, err := e.api(ctx, a, address, nil)
	if err != nil {
		return nil, err
	}
	if provider == "drive" {
		u, _ := data["user"].(map[string]any)
		a.Identity = str(u, "permissionId")
	} else {
		a.Identity = str(data, "account_id")
	}
	if a.Identity == "" {
		return nil, problem("Cloud returned an invalid account identity.")
	}
	if previous.ID != "" && previous.Identity != a.Identity {
		return nil, problem("Reconnect using the same cloud account")
	}
	all, err := e.accounts()
	if err != nil {
		return nil, err
	}
	for _, other := range all {
		if other.ID != id && other.Provider == provider && other.Identity == a.Identity {
			return nil, problem("This cloud account is already connected. Reconnect the existing connection.")
		}
	}
	a.Cursor, err = e.initialCursor(ctx, a)
	if err != nil {
		return nil, err
	}
	if a.Label == "" {
		a.Label = a.Identity
	}
	tx, err := e.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO accounts(id,provider,label,identity,cursor,grant_id,owner) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET label=excluded.label,cursor=excluded.cursor,grant_id=excluded.grant_id,owner=excluded.owner,error=''`, id, provider, a.Label, a.Identity, a.Cursor, a.Grant, a.Owner)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec("UPDATE tasks SET dirty=1,error='',status='queued' WHERE account=? AND status!='running'", id); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	// Imported Drive refresh credentials are retired only after the replacement grant works.
	if provider == "drive" {
		_ = os.Remove(filepath.Join(e.Root, id+".conf"))
	}
	return map[string]string{"id": id}, nil
}
