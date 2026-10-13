package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// authenticator runs git operations against one source. It tries them
// anonymously first. When the server asks for credentials, it looks them up
// once through git's credential helpers (the same ones the git CLI uses, e.g.
// the macOS keychain) and uses them for this and every later operation.
type authenticator struct {
	ctx    context.Context
	src    string
	auth   transport.AuthMethod
	looked bool
}

func newAuthenticator(ctx context.Context, src string) *authenticator {
	return &authenticator{ctx: ctx, src: src}
}

func (a *authenticator) run(op func(transport.AuthMethod) error) error {
	err := op(a.auth)
	if !a.looked && isAuthErr(err) {
		a.looked = true
		if a.auth = credentials(a.ctx, a.src); a.auth != nil {
			err = op(a.auth)
		}
	}
	if !isAuthErr(err) {
		return err
	}
	if a.auth != nil {
		return hintedErr{err, "the credentials from git's credential helper were rejected; update the stored token or password"}
	}
	return hintedErr{err, "store credentials with a git credential helper, e.g. by cloning the repository once with git, or use an SSH URL"}
}

func isAuthErr(err error) bool {
	return errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed)
}

type hintedErr struct {
	error
	hint string
}

func (e hintedErr) Error() string { return e.error.Error() + " (" + e.hint + ")" }

func (e hintedErr) Unwrap() error { return e.error }

// credentials asks `git credential fill` for the username and password of
// an HTTPS source. It never prompts and returns nil when git is missing or
// no helper knows the host.
func credentials(ctx context.Context, src string) transport.AuthMethod {
	u, err := url.Parse(src)
	if err != nil {
		return nil
	}
	if secure := u.Scheme == "https" || (u.Scheme == "http" && isLoopback(u.Hostname())); !secure {
		return nil
	}
	addr := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
	name := u.User.Username()
	// A decoded control character (e.g. %0A in the URL's user) would add
	// lines to the request, such as a host= line asking for another host's
	// credentials. git refuses such values too.
	if strings.ContainsFunc(addr+name, unicode.IsControl) {
		return nil
	}
	req := "url=" + addr + "\n"
	if name != "" {
		req += "username=" + name + "\n"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "credential", "fill")
	// Empty askpass variables keep git from opening a password dialog, e.g.
	// the one of an editor's integrated terminal.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=", "SSH_ASKPASS=")
	cmd.Stdin = strings.NewReader(req + "\n")
	// Stop waiting for a helper that keeps the output open after git is killed.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	auth := &http.BasicAuth{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		key, value, _ := strings.Cut(sc.Text(), "=")
		switch key {
		case "username":
			auth.Username = value
		case "password":
			auth.Password = value
		}
	}
	if auth.Password == "" {
		return nil
	}
	return auth
}

// isLoopback reports whether host is the local machine. Credentials are only
// sent over plain HTTP to it (as in tests); Normalize rejects http:// sources.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
