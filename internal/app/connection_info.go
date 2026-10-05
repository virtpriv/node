package app

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/virtpriv/node/internal/helper"
	"github.com/virtpriv/node/internal/paths"
)

// ConnectionTarget selects one of the existing operator connection displays.
type ConnectionTarget int

const (
	LNDRESTConnection ConnectionTarget = iota
	SyncthingWebConnection
)

// ConnectionCredential holds raw staged bytes. String formatting deliberately
// redacts them; callers must explicitly convert for a credential display.
type ConnectionCredential string

func (ConnectionCredential) String() string     { return "(credential redacted)" }
func (c ConnectionCredential) GoString() string { return c.String() }

// ConnectionInfo is one read of an endpoint and its deliberately staged
// credential. It does not prove remote reachability or successful authentication.
// The credential is explicit; default string formatting redacts it.
type ConnectionInfo struct {
	Address       string
	AddressErr    error
	CredentialErr error
	Credential    ConnectionCredential
	Target        ConnectionTarget
}

func (i ConnectionInfo) HasCredential() bool { return i.CredentialErr == nil && len(i.Credential) > 0 }

// CredentialText is for an explicit credential display, never diagnostics.
func (i ConnectionInfo) CredentialText() string {
	if !i.HasCredential() {
		return ""
	}
	if i.Target == LNDRESTConnection {
		return hex.EncodeToString([]byte(i.Credential))
	}
	return string(i.Credential)
}

// URL formats the existing REST connection contract. The caller supplies a
// freshly observed public IP for the optional clearnet endpoint.
func (i ConnectionInfo) URL(host string) string {
	if host == "" {
		return ""
	}
	if i.Target == SyncthingWebConnection {
		return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, "8384")}).String()
	}
	if !i.HasCredential() {
		return ""
	}
	return (&url.URL{Scheme: "lndconnect", Host: net.JoinHostPort(host, "8080"),
		RawQuery: url.Values{"macaroon": {base64.RawURLEncoding.EncodeToString([]byte(i.Credential))}}.Encode()}).String()
}

func (i ConnectionInfo) String() string   { return "connection information (credential redacted)" }
func (i ConnectionInfo) GoString() string { return i.String() }

// ConnectionInfoReader owns read cancellation and joining for one terminal.
// Helper reads have a 30-second local deadline including queue time. This does
// not interrupt root work. Ordinary staged-file reads finish before Close returns.
type ConnectionInfoReader struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	calls    sync.WaitGroup
	call     func(context.Context, string, any, any) error
	readFile func(string) ([]byte, error)
}

func NewConnectionInfoReader() *ConnectionInfoReader {
	ctx, cancel := context.WithCancel(context.Background())
	return &ConnectionInfoReader{ctx: ctx, cancel: cancel, readFile: os.ReadFile,
		call: func(ctx context.Context, verb string, params, result any) error {
			session, err := helper.StartContext(ctx, verb, params)
			if err != nil {
				return err
			}
			return session.Wait(result)
		},
	}
}

func (r *ConnectionInfoReader) Close() {
	r.mu.Lock()
	r.cancel()
	r.mu.Unlock()
	r.calls.Wait()
}

func (r *ConnectionInfoReader) Read(target ConnectionTarget) ConnectionInfo {
	result := ConnectionInfo{Target: target}
	fail := func(err error) ConnectionInfo {
		result.Address, result.Credential = "", ""
		result.AddressErr, result.CredentialErr = err, err
		return result
	}
	r.mu.Lock()
	if err := r.ctx.Err(); err != nil {
		r.mu.Unlock()
		return fail(err)
	}
	r.calls.Add(1)
	r.mu.Unlock()
	defer r.calls.Done()
	if target != LNDRESTConnection && target != SyncthingWebConnection {
		return fail(errors.New("unsupported connection display"))
	}
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	var addresses helper.NodeAddressesResult
	result.AddressErr = r.call(ctx, helper.VerbReadNodeAddresses, nil, &addresses)
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	path := paths.StateLNDMacaroon
	result.Address = addresses.LNDRESTOnion
	if target == SyncthingWebConnection {
		path = paths.StateSyncthingWebPassword
		result.Address = addresses.SyncthingOnion
	}
	if result.AddressErr != nil {
		result.Address = ""
	} else if result.Address == "" {
		result.AddressErr = errors.New("onion address unavailable")
	}
	credential, err := r.readFile(path)
	if err == nil && target == SyncthingWebConnection {
		credential = []byte(strings.TrimSpace(string(credential)))
	}
	if err != nil {
		result.CredentialErr = errors.New("staged connection credential unavailable")
	} else if len(credential) == 0 {
		result.CredentialErr = errors.New("staged connection credential is empty")
	} else {
		result.Credential = ConnectionCredential(credential)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return result
}
