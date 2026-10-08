package registry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

type fakeFetcher map[string]string

func (f fakeFetcher) Fetch(_ context.Context, src, _ string, _ bool) (*source.Checkout, error) {
	dir, ok := f[src]
	if !ok {
		return nil, errors.New("unreachable")
	}
	return &source.Checkout{URL: src, Dir: dir}, nil
}

var _ = Describe("Repos", func() {
	It("ships a non-empty builtin list sorted by popularity", func() {
		b := registry.Builtin()
		Expect(len(b)).To(BeNumerically(">=", 10))
		for i := 1; i < len(b); i++ {
			Expect(b[i-1].Stars).To(BeNumerically(">=", b[i].Stars))
		}
		for _, r := range b {
			Expect(source.Normalize(r.URL)).To(Equal(r.URL))
		}
	})

	It("puts user repos first and deduplicates", func() {
		cfg := &config.Config{Repos: []config.Repo{
			{URL: "https://github.com/me/skills"},
			{URL: "https://github.com/anthropics/skills", Name: "mine"},
		}}
		repos := registry.Repos(cfg)
		Expect(repos[0].Name).To(Equal("me/skills"))
		Expect(repos[0].Custom).To(BeTrue())
		n := 0
		for _, r := range repos {
			if r.URL == "https://github.com/anthropics/skills" {
				n++
			}
		}
		Expect(n).To(Equal(1))
	})
})

var _ = Describe("Index", func() {
	var (
		f     fakeFetcher
		repos []registry.Repo
	)

	BeforeEach(func() {
		a, b := GinkgoT().TempDir(), GinkgoT().TempDir()
		Expect(testutil.WriteFiles(a, map[string]string{
			"skills/pdf/SKILL.md":  testutil.SkillMD("pdf", "Read and write PDF documents", "1.0.0"),
			"skills/docx/SKILL.md": testutil.SkillMD("docx", "Word documents", ""),
		})).To(Succeed())
		Expect(testutil.WriteFiles(b, map[string]string{
			"golang-testing/SKILL.md": testutil.SkillMD("golang-testing", "Write Go tests with pdf fixtures", ""),
		})).To(Succeed())
		f = fakeFetcher{"https://x/a": a, "https://x/b": b}
		repos = []registry.Repo{{Name: "a", URL: "https://x/a"}, {Name: "b", URL: "https://x/b"}, {Name: "down", URL: "https://x/down"}}
	})

	It("indexes all reachable repos and records failures", func() {
		var calls int
		idx := registry.Build(context.Background(), f, repos, false, func(done, total int, _ registry.Repo, _ error) {
			calls++
			Expect(total).To(Equal(3))
		})
		Expect(calls).To(Equal(3))
		Expect(idx.Entries).To(HaveLen(3))
		Expect(idx.Errors).To(HaveKey("https://x/down"))
		Expect(idx.Entries[0].Repo.Name).To(Equal("a"))
	})

	It("searches name before description and requires all terms", func() {
		idx := registry.Build(context.Background(), f, repos, false, nil)
		hits := idx.Search("pdf")
		Expect(hits).To(HaveLen(2))
		Expect(hits[0].Name).To(Equal("pdf"))
		Expect(idx.Search("word docx")).To(HaveLen(1))
		Expect(idx.Search("nothing-matches")).To(BeEmpty())
		Expect(idx.Search("")).To(HaveLen(3))
	})

	It("caches the index with a TTL", func() {
		p := registry.IndexPath(GinkgoT().TempDir())
		idx := registry.Build(context.Background(), f, repos, false, nil)
		Expect(idx.Save(p)).To(Succeed())

		loaded, err := registry.LoadIndex(p, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Entries).To(HaveLen(3))

		idx.BuiltAt = time.Now().Add(-2 * time.Hour)
		Expect(idx.Save(p)).To(Succeed())
		stale, err := registry.LoadIndex(p, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(stale).To(BeNil())

		missing, err := registry.LoadIndex(filepath.Join(GinkgoT().TempDir(), "none.json"), time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(missing).To(BeNil())
	})
})

var _ = Describe("RemoteSearch", func() {
	It("queries the skills.sh search API", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/api/search"))
			Expect(r.URL.Query().Get("q")).To(Equal("pdf"))
			w.Write([]byte(`{"skills":[{"id":"anthropics/skills/pdf","name":"pdf","installs":42,"source":"anthropics/skills"}]}`))
		}))
		DeferCleanup(srv.Close)
		rs := &registry.RemoteSearch{BaseURL: srv.URL, Client: srv.Client()}
		hits, err := rs.Search(context.Background(), "pdf", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(hits).To(ConsistOf(registry.RemoteSkill{ID: "anthropics/skills/pdf", Name: "pdf", Installs: 42, Source: "anthropics/skills"}))
	})

	It("reports HTTP errors", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		DeferCleanup(srv.Close)
		_, err := (&registry.RemoteSearch{BaseURL: srv.URL, Client: srv.Client()}).Search(context.Background(), "x", 1)
		Expect(err).To(MatchError(ContainSubstring("403")))
	})
})
