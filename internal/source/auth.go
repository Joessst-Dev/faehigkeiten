package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// withAuth runs op anonymously first. When the server asks for credentials,
// it looks them up through git's credential helpers (the same ones the git
// CLI uses, e.g. the macOS keychain) and runs op again with them.
func withAuth(ctx context.Context, src string, op func(transport.AuthMethod) error) error {
	err := op(nil)
	if !isAuthErr(err) {
		return err
	}
	auth := credentials(ctx, src)
	if auth == nil {
		return authHint(err)
	}
	if err := op(auth); err != nil {
		return authHint(err)
	}
	return nil
}

func isAuthErr(err error) bool {
	return errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed)
}

type hintedErr struct{ error }

func (e hintedErr) Error() string {
	return e.error.Error() + " (store credentials with a git credential helper, e.g. by cloning the repository once with git, or use an SSH URL)"
}

func (e hintedErr) Unwrap() error { return e.error }

func authHint(err error) error {
	if !isAuthErr(err) {
		return err
	}
	return hintedErr{err}
}

// credentials asks `git credential fill` for the username and password of
// an HTTPS source. It never prompts and returns nil when git is missing or
// no helper knows the host.
func credentials(ctx context.Context, src string) transport.AuthMethod {
	u, err := url.Parse(src)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil
	}
	u.User = nil
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "credential", "fill")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	cmd.Stdin = strings.NewReader("url=" + u.String() + "\n\n")
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
