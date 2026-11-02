package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"
)

type Progress struct {
	Phase          string   `json:"phase"`
	ObservedAt     int64    `json:"observedAt"`
	Measured       bool     `json:"measured"`
	Bytes          int64    `json:"bytes"`
	TotalBytes     int64    `json:"totalBytes"`
	Checks         int64    `json:"checks"`
	TotalChecks    int64    `json:"totalChecks"`
	Transfers      int64    `json:"transfers"`
	TotalTransfers int64    `json:"totalTransfers"`
	Speed          float64  `json:"speed"`
	ETA            *float64 `json:"eta"`
}

type taskProgress struct {
	sync.Mutex
	id    string
	value Progress
}

type progressKey struct{}

func progressFrom(ctx context.Context) *taskProgress {
	p, _ := ctx.Value(progressKey{}).(*taskProgress)
	return p
}

func (e *Engine) startProgress(ctx context.Context, id string) (context.Context, func()) {
	p := &taskProgress{id: id, value: Progress{Phase: "preparing", ObservedAt: time.Now().Unix()}}
	e.progressMu.Lock()
	e.progress = p
	e.progressMu.Unlock()
	return context.WithValue(ctx, progressKey{}, p), func() {
		e.progressMu.Lock()
		if e.progress == p {
			e.progress = nil
		}
		e.progressMu.Unlock()
	}
}

func (e *Engine) attachProgress(tasks []Task) {
	e.progressMu.Lock()
	defer e.progressMu.Unlock()
	if e.progress == nil {
		return
	}
	p := e.progress
	p.Lock()
	defer p.Unlock()
	for i := range tasks {
		if tasks[i].ID == p.id && tasks[i].Status == "running" {
			value := p.value
			tasks[i].Progress = &value
		}
	}
}

func setProgressPhase(ctx context.Context, phase string) {
	if p := progressFrom(ctx); p != nil {
		p.Lock()
		p.value = Progress{Phase: phase, ObservedAt: time.Now().Unix()}
		p.Unlock()
	}
}

func commandPhase(args []string) string {
	if len(args) > 0 {
		switch args[0] {
		case "lsjson", "cat":
			return "scanning"
		case "check":
			return "checking"
		case "copy", "copyto", "sync":
			return "transferring"
		case "bisync":
			return "comparing"
		}
	}
	return "preparing"
}

// Stats are consumed as a stream, not accumulated in the bounded error log.
// Only numeric counters cross the API; filenames, URLs and log messages do not.
type progressLog struct {
	progress *taskProgress
	logs     *limitedBuffer
	line     []byte
	overlong bool
}

func (w *progressLog) Write(data []byte) (int, error) {
	n := len(data)
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		part := data
		if end >= 0 {
			part = data[:end+1]
		}
		if w.overlong || len(w.line)+len(part) > 64<<10 {
			w.logs.Write(w.line)
			w.line = nil
			w.logs.Write(part)
			w.overlong = end < 0
		} else {
			w.line = append(w.line, part...)
			if end >= 0 {
				w.flush()
			}
		}
		data = data[len(part):]
	}
	return n, nil
}

func (w *progressLog) flush() {
	if len(w.line) == 0 {
		return
	}
	var entry struct {
		Stats *Progress `json:"stats"`
	}
	if json.Unmarshal(w.line, &entry) == nil && entry.Stats != nil {
		v := entry.Stats
		w.progress.Lock()
		v.Phase = w.progress.value.Phase
		if v.Phase == "comparing" && v.TotalBytes > 0 {
			v.Phase = "transferring"
		}
		v.ObservedAt = time.Now().Unix()
		v.Measured = true
		w.progress.value = *v
		w.progress.Unlock()
	} else {
		w.logs.Write(w.line)
	}
	w.line = w.line[:0]
}
