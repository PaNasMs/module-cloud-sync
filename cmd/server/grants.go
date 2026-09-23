package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/PaNasMs/module-sdk/auth"
	"github.com/PaNasMs/module-sdk/external"
	"golang.org/x/sys/unix"
)

type grantReply struct {
	Access external.Access `json:"access"`
	Status int             `json:"status"`
	Code   string          `json:"code"`
}

func grantPair(owner string, allowed map[string]bool) (*os.File, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	parent := os.NewFile(uintptr(fds[0]), "grant-parent")
	child := os.NewFile(uintptr(fds[1]), "grant-child")
	return parent, child, nil
}

// The unprivileged worker inherits this private channel. Its owner is fixed by
// the parent; workers cannot choose a different NAS owner or open the core socket.
func relayGrants(file *os.File, owner string, allowed map[string]bool) {
	defer file.Close()
	conn, err := net.FileConn(file)
	if err != nil {
		return
	}
	defer conn.Close()
	broker := external.New()
	reader := bufio.NewReader(conn)
	for {
		raw, err := reader.ReadSlice('\n')
		if err != nil {
			return
		}
		var req struct {
			Grant string `json:"grantId"`
		}
		if json.Unmarshal(raw, &req) != nil || len(req.Grant) > 128 {
			return
		}
		reply := grantReply{Status: 403, Code: "external.accountUnavailable"}
		identity, err := auth.Lookup(owner, allowed)
		if err == nil && identity.Role == "admin" {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			reply.Access, err = broker.Token(ctx, req.Grant, owner)
			cancel()
			if err == nil {
				reply.Status = 200
				reply.Code = ""
			} else {
				reply.Status = 503
				reply.Code = "external.unavailable"
				var typed *external.Error
				if errors.As(err, &typed) {
					reply.Status = typed.Status
					reply.Code = typed.Code
				}
			}
		}
		if json.NewEncoder(conn).Encode(reply) != nil {
			return
		}
	}
}
func workerBroker(file *os.File) (func(context.Context, string) (external.Access, error), error) {
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	reader := bufio.NewReader(io.LimitReader(conn, 1<<62))
	return func(ctx context.Context, grant string) (external.Access, error) {
		mu.Lock()
		defer mu.Unlock()
		deadline := time.Now().Add(45 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetDeadline(deadline)
		if err := json.NewEncoder(conn).Encode(map[string]string{"grantId": grant}); err != nil {
			return external.Access{}, err
		}
		raw, err := reader.ReadBytes('\n')
		if err != nil {
			return external.Access{}, err
		}
		if len(raw) > 32768 {
			return external.Access{}, errors.New("invalid broker response")
		}
		var reply grantReply
		if err = json.Unmarshal(raw, &reply); err != nil {
			return external.Access{}, err
		}
		if reply.Status != 200 {
			return external.Access{}, &external.Error{Status: reply.Status, Code: reply.Code}
		}
		return reply.Access, nil
	}, nil
}
