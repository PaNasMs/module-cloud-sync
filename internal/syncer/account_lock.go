package syncer

import "sync"

func (e *Engine) accountLock(id string) *sync.Mutex {
	e.accountsMu.Lock()
	defer e.accountsMu.Unlock()
	if e.accountLocks == nil {
		e.accountLocks = make(map[string]*sync.Mutex)
	}
	if e.accountLocks[id] == nil {
		e.accountLocks[id] = &sync.Mutex{}
	}
	return e.accountLocks[id]
}

func (e *Engine) cachedToken(id string) (lease, bool) {
	e.tokensMu.Lock()
	defer e.tokensMu.Unlock()
	token, ok := e.tokens[id]
	return token, ok
}

func (e *Engine) cacheToken(id string, token lease) {
	e.tokensMu.Lock()
	defer e.tokensMu.Unlock()
	e.tokens[id] = token
}

func (e *Engine) deleteToken(id string) {
	e.tokensMu.Lock()
	defer e.tokensMu.Unlock()
	delete(e.tokens, id)
}
