package syncer

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"time"
)

// The child receives a per-run capability, never the provider's refresh token.
func (e *Engine) refreshConfig(ctx context.Context, a Account) (string, func(), error) {
	if a.Provider != "drive" && a.Provider != "dropbox" {
		return e.config(a), func() {}, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	secretBytes := make([]byte, 32)
	if _, err = rand.Read(secretBytes); err != nil {
		l.Close()
		return "", nil, err
	}
	secret := hex.EncodeToString(secretBytes)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("grant_type") != "refresh_token" || subtle.ConstantTimeCompare([]byte(r.Form.Get("refresh_token")), []byte(secret)) != 1 {
			http.Error(w, "Invalid refresh request", http.StatusForbidden)
			return
		}
		callCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		access, err := e.Broker(callCtx, a.Grant)
		if err != nil || access.AccessToken == "" || access.TokenType != "Bearer" || time.Until(access.ExpiresAt) < 15*time.Second || !scopeAllowed(a.Provider, access.Scope) {
			http.Error(w, "Cloud authorization unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": access.AccessToken, "token_type": "Bearer", "expires_in": int(time.Until(access.ExpiresAt).Seconds()), "refresh_token": secret})
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 5 * time.Second}
	file, err := os.CreateTemp(e.Runtime, "rclone-*.conf")
	if err != nil {
		l.Close()
		return "", nil, err
	}
	cleanup := func() { server.Close(); os.Remove(file.Name()) }
	access := e.tokens[a.ID].Access
	token := map[string]any{"access_token": access.AccessToken, "token_type": "Bearer", "expiry": access.ExpiresAt, "refresh_token": secret}
	_, err = file.WriteString("[cloud]\ntype = " + a.Provider + "\ntoken_url = http://" + l.Addr().String() + "/\ntoken = " + marshal(token) + "\n")
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		l.Close()
		cleanup()
		if err == nil {
			err = closeErr
		}
		return "", nil, err
	}
	go server.Serve(l)
	return file.Name(), cleanup, nil
}

func scopeAllowed(provider, scope string) bool {
	return (provider == "drive" && (scope == "https://www.googleapis.com/auth/drive" || scope == "https://www.googleapis.com/auth/drive.readonly")) ||
		(provider == "dropbox" && scope == "account_info.read files.metadata.read files.content.read files.content.write")
}
