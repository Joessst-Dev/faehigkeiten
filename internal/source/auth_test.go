package source_test

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/source"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

var _ = Describe("Authentication", func() {
	var (
		ctx    context.Context
		srcURL string
		tmp    string
	)

	BeforeEach(func() {
		gitBin, err := exec.LookPath("git")
		if err != nil {
			Skip("git is not installed")
		}
		ctx = context.Background()
		root := GinkgoT().TempDir()
		repo, err := testutil.InitRepo(filepath.Join(root, "skills"), map[string]string{"skills/a/SKILL.md": testutil.SkillMD("a", "A", "")})
		Expect(err).NotTo(HaveOccurred())
		h, err := repo.Head()
		Expect(err).NotTo(HaveOccurred())
		_, err = repo.CreateTag("v1.0.0", h.Hash(), nil)
		Expect(err).NotTo(HaveOccurred())
		tmp = GinkgoT().TempDir()

		backend := &cgi.Handler{
			Path: gitBin,
			Args: []string{"http-backend"},
			Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if user, pass, ok := r.BasicAuth(); !ok || user != "me" || pass != "secret" {
				w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			backend.ServeHTTP(w, r)
		}))
		DeferCleanup(srv.Close)
		srcURL = srv.URL + "/skills/.git"

		// Isolate git from the developer's own configuration and helpers.
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())
		GinkgoT().Setenv("XDG_CONFIG_HOME", GinkgoT().TempDir())
		GinkgoT().Setenv("GIT_CONFIG_NOSYSTEM", "1")
	})

	useHelper := func(helper string) {
		GinkgoT().Setenv("GIT_CONFIG_COUNT", "1")
		GinkgoT().Setenv("GIT_CONFIG_KEY_0", "credential.helper")
		GinkgoT().Setenv("GIT_CONFIG_VALUE_0", helper)
	}

	It("clones and pulls with credentials from git's credential helper", func() {
		useHelper("!f() { echo username=me; echo password=secret; }; f")
		dir := filepath.Join(GinkgoT().TempDir(), "clone")
		Expect(source.Clone(ctx, srcURL, "", dir)).To(Succeed())
		Expect(filepath.Join(dir, "skills", "a", "SKILL.md")).To(BeAnExistingFile())
		Expect(source.Pull(ctx, dir)).To(Succeed())
	})

	It("reports missing credentials instead of a missing ref", func() {
		dir := filepath.Join(GinkgoT().TempDir(), "clone")
		err := source.Clone(ctx, srcURL, "main", dir)
		Expect(err).To(MatchError(transport.ErrAuthenticationRequired))
		Expect(err).NotTo(MatchError(ContainSubstring("not found")))
		Expect(err).To(MatchError(ContainSubstring("credential helper")))
	})

	It("looks up credentials once for the branch and tag attempts", func() {
		calls := filepath.Join(tmp, "calls")
		useHelper("!f() { echo call >> '" + calls + "'; echo username=me; echo password=secret; }; f")
		dir := filepath.Join(GinkgoT().TempDir(), "clone")
		Expect(source.Clone(ctx, srcURL, "v1.0.0", dir)).To(Succeed())
		Expect(os.ReadFile(calls)).To(Equal([]byte("call\n")))
	})

	It("asks the helper for the user named in the URL", func() {
		useHelper(`!f() { while read l; do [ "$l" = username=me ] && echo password=secret; done; }; f`)
		dir := filepath.Join(GinkgoT().TempDir(), "clone")
		Expect(source.Clone(ctx, strings.Replace(srcURL, "://", "://me@", 1), "", dir)).To(Succeed())
	})

	It("never opens a password dialog", func() {
		marker := filepath.Join(tmp, "asked")
		askpass := filepath.Join(tmp, "askpass.sh")
		Expect(os.WriteFile(askpass, []byte("#!/bin/sh\ntouch '"+marker+"'\necho secret\n"), 0o755)).To(Succeed())
		GinkgoT().Setenv("GIT_ASKPASS", askpass)
		GinkgoT().Setenv("SSH_ASKPASS", askpass)
		dir := filepath.Join(GinkgoT().TempDir(), "clone")
		Expect(source.Clone(ctx, srcURL, "", dir)).To(MatchError(transport.ErrAuthenticationRequired))
		Expect(marker).NotTo(BeAnExistingFile())
	})

	It("reports credentials the server rejects", func() {
		useHelper("!f() { echo username=me; echo password=wrong; }; f")
		dir := filepath.Join(GinkgoT().TempDir(), "clone")
		Expect(source.Clone(ctx, srcURL, "", dir)).To(MatchError(transport.ErrAuthenticationRequired))
	})
})
