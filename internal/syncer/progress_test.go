package syncer

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProgressStreamBoundedAndPrivate(t *testing.T) {
	e := &Engine{}
	ctx, finish := e.startProgress(context.Background(), "task")
	defer finish()
	setProgressPhase(ctx, "transferring")
	p := progressFrom(ctx)
	logs := &limitedBuffer{limit: 1024}
	w := &progressLog{progress: p, logs: logs}
	line := `{"stats":{"bytes":123,"totalBytes":456,"checks":2,"totalChecks":3,"transfers":1,"totalTransfers":2,"speed":12,"eta":27,"transferring":[{"name":"private-file"}],"lastError":"secret"}}` + "\n"
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 10000 {
			tasks := []Task{{ID: "task", Status: "running"}, {ID: "other", Status: "running"}}
			e.attachProgress(tasks)
			if tasks[1].Progress != nil {
				t.Error("progress leaked to another task")
			}
		}
	}()
	for range 10000 {
		w.Write([]byte(line[:21]))
		w.Write([]byte(line[21:]))
	}
	wg.Wait()
	if logs.Exceeded() || logs.b.Len() != 0 {
		t.Fatal("stats accumulated in error log")
	}
	raw, _ := json.Marshal(p.value)
	if strings.Contains(string(raw), "private-file") || strings.Contains(string(raw), "secret") {
		t.Fatal("raw stats leaked")
	}
	if p.value.Bytes != 123 || p.value.ETA == nil || *p.value.ETA != 27 {
		t.Fatal(p.value)
	}
	w.Write([]byte("{\"msg\":\"connection refused\"}"))
	w.flush()
	if safeError(transferFailure(logs.b.String())) != "Cloud is unreachable; retry later." {
		t.Fatal("lost error classification")
	}
	setProgressPhase(ctx, "checking")
	if p.value.Measured || p.value.Bytes != 0 || p.value.ETA != nil {
		t.Fatal("previous stage retained")
	}
	finish()
	tasks := []Task{{ID: "task", Status: "running"}}
	e.attachProgress(tasks)
	if tasks[0].Progress != nil {
		t.Fatal("finished progress retained")
	}
}

func TestProgressTerminalStatesAndOversizedLog(t *testing.T) {
	e := &Engine{}
	ctx, finish := e.startProgress(context.Background(), "task")
	defer finish()
	for _, status := range []string{"idle", "queued", "error"} {
		tasks := []Task{{ID: "task", Status: status}}
		e.attachProgress(tasks)
		if tasks[0].Progress != nil {
			t.Fatal(status)
		}
	}
	logs := &limitedBuffer{limit: 128}
	w := &progressLog{progress: progressFrom(ctx), logs: logs}
	w.Write([]byte(strings.Repeat("x", 128<<10) + "\n"))
	if len(w.line) > 64<<10 || !logs.Exceeded() {
		t.Fatal("unbounded malformed log")
	}
}

func TestRealRcloneProgress(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone unavailable")
	}
	e := fixture(t)
	source := t.TempDir()
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "payload"), make([]byte, 256<<10), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, finish := e.startProgress(context.Background(), "task")
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := e.run(ctx, Account{ID: accountID, Provider: "local"}, []string{"copy", source, dest, "--bwlimit", "64k"}, nil)
		done <- err
	}()
	saw := false
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if !saw {
				t.Fatal("no live stats before process exit")
			}
			if data, err := os.ReadFile(filepath.Join(dest, "payload")); err != nil || len(data) != 256<<10 {
				t.Fatal("transfer changed")
			}
			return
		case <-time.After(100 * time.Millisecond):
			tasks := []Task{{ID: "task", Status: "running"}}
			e.attachProgress(tasks)
			p := tasks[0].Progress
			saw = saw || p != nil && p.Measured && p.Bytes > 0 && p.TotalBytes > 0
		}
	}
}

func TestJSONDiagnosticsRedactEscapedSecretsAndKeepFailures(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"msg":     "server returned {\"access_token\":\"hidden-access\",\"refresh_token\":\"hidden-refresh\"}; Bearer hidden-bearer",
		"error":   "connection refused",
		"context": map[string]any{"client_secret": "hidden-client"},
	})
	text := redactTransferLog(string(raw))
	for _, secret := range []string{"hidden-access", "hidden-refresh", "hidden-bearer", "hidden-client"} {
		if strings.Contains(text, secret) {
			t.Fatal("secret retained", secret)
		}
	}
	if !json.Valid([]byte(text)) || safeError(transferFailure(text)) != "Cloud is unreachable; retry later." {
		t.Fatal(text)
	}
	for _, message := range []string{"must run --resync", "invalid_grant", "429", "max-delete", "failed to check: differences found"} {
		raw, _ = json.Marshal(map[string]string{"msg": "operation failed", "error": message})
		if safeError(transferFailure(string(raw))) != safeError(transferFailure(redactTransferLog(string(raw)))) {
			t.Fatal(message)
		}
	}
}

func TestProgressAgeUsesServerElapsedTime(t *testing.T) {
	e := &Engine{}
	ctx, finish := e.startProgress(context.Background(), "task")
	defer finish()
	p := progressFrom(ctx)
	p.value.ObservedAt = time.Now().Add(time.Hour).Unix()
	p.updated = time.Now().Add(-15 * time.Second)
	tasks := []Task{{ID: "task", Status: "running"}}
	e.attachProgress(tasks)
	if tasks[0].Progress.AgeSeconds < 15 || tasks[0].Progress.AgeSeconds > 16 {
		t.Fatal(tasks[0].Progress)
	}
}

func TestHistoryDryRunDoesNotReportTransfers(t *testing.T) {
	phase := commandPhase([]string{"bisync", "local", "cloud:", "--resync", "--dry-run"})
	if phase != "rebuilding" {
		t.Fatal(phase)
	}
	e := &Engine{}
	ctx, finish := e.startProgress(context.Background(), "task")
	defer finish()
	setProgressPhase(ctx, phase)
	w := &progressLog{progress: progressFrom(ctx), logs: &limitedBuffer{limit: 1024}}
	w.Write([]byte("{\"stats\":{\"bytes\":0,\"totalBytes\":100}}\n"))
	if w.progress.value.Phase != "rebuilding" {
		t.Fatal(w.progress.value)
	}
}
