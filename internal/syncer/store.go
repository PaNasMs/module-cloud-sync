package syncer

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Account struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Identity string `json:"identity"`
	Cursor   string `json:"-"`
	Error    string `json:"error"`
	Grant    string `json:"-"`
	Owner    string `json:"-"`
}
type Task struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	Name        string `json:"name"`
	Local       string `json:"local"`
	Remote      string `json:"remote"`
	Direction   string `json:"direction"`
	Mount       string `json:"mount"`
	Paused      int    `json:"paused"`
	Status      string `json:"status"`
	Dirty       int    `json:"dirty"`
	Initialized int    `json:"initialized"`
	Snapshot    string `json:"snapshot"`
	Error       string `json:"error"`
	LastSync    int64  `json:"last_sync"`
}
type History struct {
	ID      int64  `json:"id"`
	Task    string `json:"task"`
	At      int64  `json:"at"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}
type State struct {
	Accounts []Account `json:"accounts"`
	Tasks    []Task    `json:"tasks"`
	History  []History `json:"history"`
}
type Failure struct {
	Message   string
	Reconnect bool
}

func (e *Failure) Error() string { return e.Message }
func problem(s string) error     { return &Failure{Message: s} }
func safeError(err error) string {
	var e *Failure
	if errors.As(err, &e) {
		return e.Message
	}
	return "Cloud Sync operation failed. Check the connection and try again."
}
func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}
func validGrant(s string) bool {
	if len(s) < 16 || len(s) > 128 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

type Engine struct {
	DB                   *sql.DB
	Root, Runtime, Owner string
	Broker               Broker
	cloudMu              sync.Mutex
	tokens               map[string]lease
	HTTP                 HTTPClient
	Runner               Runner
	Mount                func(string) (string, error)
	Local                func(string) (string, error)
}

func Open(root, runtime, owner string, broker Broker) (*Engine, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(runtime, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(root, "state.db")+"?_busy_timeout=15000&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	schema := `CREATE TABLE IF NOT EXISTS accounts(id TEXT PRIMARY KEY,provider TEXT NOT NULL,label TEXT NOT NULL,identity TEXT NOT NULL,cursor TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',grant_id TEXT NOT NULL DEFAULT '',owner TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS tasks(id TEXT PRIMARY KEY,account TEXT NOT NULL REFERENCES accounts(id),name TEXT NOT NULL,local TEXT NOT NULL,remote TEXT NOT NULL,direction TEXT NOT NULL,mount TEXT NOT NULL,paused INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL DEFAULT 'queued',dirty INTEGER NOT NULL DEFAULT 1,initialized INTEGER NOT NULL DEFAULT 0,snapshot TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',last_sync INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS history(id INTEGER PRIMARY KEY,task TEXT,at INTEGER NOT NULL,kind TEXT NOT NULL,message TEXT NOT NULL);`
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	rows, err := db.Query("PRAGMA table_info(accounts)")
	if err != nil {
		db.Close()
		return nil, err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &nn, &def, &pk); err != nil {
			break
		}
		columns[name] = true
	}
	rows.Close()
	for _, name := range []string{"grant_id", "owner"} {
		if !columns[name] {
			_, err = db.Exec("ALTER TABLE accounts ADD COLUMN " + name + " TEXT NOT NULL DEFAULT ''")
			if err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	e := &Engine{DB: db, Root: root, Runtime: runtime, Owner: owner, Broker: broker, tokens: map[string]lease{}, HTTP: defaultHTTP(), Mount: mountIdentity, Local: localFolder}
	e.Runner = e.runProcess
	return e, nil
}
func (e *Engine) Close() error { return e.DB.Close() }
func (e *Engine) accounts() ([]Account, error) {
	rows, err := e.DB.Query("SELECT id,provider,label,identity,cursor,error,grant_id,owner FROM accounts")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		if err = rows.Scan(&a.ID, &a.Provider, &a.Label, &a.Identity, &a.Cursor, &a.Error, &a.Grant, &a.Owner); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (e *Engine) account(id string) (Account, error) {
	all, err := e.accounts()
	if err != nil {
		return Account{}, err
	}
	for _, a := range all {
		if a.ID == id {
			return a, nil
		}
	}
	return Account{}, problem("Account not found")
}
func (e *Engine) tasks() ([]Task, error) {
	rows, err := e.DB.Query("SELECT id,account,name,local,remote,direction,mount,paused,status,dirty,initialized,snapshot,error,last_sync FROM tasks ORDER BY last_sync")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		if err = rows.Scan(&t.ID, &t.Account, &t.Name, &t.Local, &t.Remote, &t.Direction, &t.Mount, &t.Paused, &t.Status, &t.Dirty, &t.Initialized, &t.Snapshot, &t.Error, &t.LastSync); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (e *Engine) State() (State, error) {
	s := State{History: []History{}}
	var err error
	s.Accounts, err = e.accounts()
	if err != nil {
		return s, err
	}
	s.Tasks, err = e.tasks()
	if err != nil {
		return s, err
	}
	rows, err := e.DB.Query("SELECT id,COALESCE(task,''),at,kind,message FROM history ORDER BY id DESC LIMIT 100")
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var h History
		if err = rows.Scan(&h.ID, &h.Task, &h.At, &h.Kind, &h.Message); err != nil {
			return s, err
		}
		s.History = append(s.History, h)
	}
	return s, rows.Err()
}
func (e *Engine) event(task, kind, message string) error {
	tx, err := e.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO history(task,at,kind,message) VALUES(?,?,?,?)", task, time.Now().Unix(), kind, message); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM history WHERE id NOT IN (SELECT id FROM history ORDER BY id DESC LIMIT 500)"); err != nil {
		return err
	}
	return tx.Commit()
}
func atomicFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".config-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func marshal(v any) string { b, _ := json.Marshal(v); return string(b) }
