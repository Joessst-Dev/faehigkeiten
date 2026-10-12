package registry_test

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

type fakeFetcher map[string]string

func (f fakeFetcher) Fetch(ctx context.Context, src, _ string, _ bool) (*source.Checkout, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f["block"] != "" && src == f["block"] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
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

	It("returns a builtin list callers may change", func() {
		b := registry.Builtin()
		url := b[0].URL
		b[0].URL = "changed"
		Expect(registry.Builtin()[0].URL).To(Equal(url))
		Expect(registry.Repos(nil)[0].URL).To(Equal(url))
	})

	It("puts user repos first and deduplicates", func() {
		cfg := &config.Config{}
		cfg.AddRepo(config.Repo{URL: "https://github.com/me/skills"})
		cfg.AddRepo(config.Repo{URL: "https://github.com/anthropics/skills", Name: "mine"})
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
		var calls, found int
		idx := registry.Build(context.Background(), f, repos, false, func(done, total int, _ registry.Repo, entries []registry.Entry, _ error) {
			calls++
			found += len(entries)
			Expect(total).To(Equal(3))
		})
		Expect(calls).To(Equal(3))
		Expect(found).To(Equal(3))
		Expect(idx.Entries).To(HaveLen(3))
		Expect(idx.Errors).To(HaveKey("https://x/down"))
		Expect(idx.Entries[0].Repo.Name).To(Equal("a"))
	})

	It("finishes and records errors when cancelled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		idx := registry.Build(ctx, f, repos, false, nil)
		Expect(idx.Entries).To(BeEmpty())
		Expect(idx.Errors).To(HaveLen(3))
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

		loaded, fresh, err := registry.LoadIndexAny(p, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh).To(BeTrue())
		Expect(loaded.Entries).To(HaveLen(3))

		idx.BuiltAt = time.Now().Add(-2 * time.Hour)
		Expect(idx.Save(p)).To(Succeed())
		stale, fresh, err := registry.LoadIndexAny(p, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh).To(BeFalse())
		Expect(stale.Entries).To(HaveLen(3), "stale indexes are still returned")

		missing, _, err := registry.LoadIndexAny(filepath.Join(GinkgoT().TempDir(), "none.json"), time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(missing).To(BeNil())
	})

	It("merges per-repository results without touching the original", func() {
		idx := registry.Build(context.Background(), f, repos, false, nil)
		merged := idx.Merge(repos[0], []registry.Entry{{Name: "new", Path: "new", Repo: repos[0]}}, nil)
		Expect(idx.Entries).To(HaveLen(3))
		Expect(merged.Search("")).To(HaveLen(2))
		Expect(merged.Search("new")).To(HaveLen(1))

		failed := merged.Merge(repos[1], nil, errors.New("boom"))
		Expect(failed.Entries).To(HaveLen(2), "keeps the last good entries")
		Expect(failed.Errors).To(HaveKeyWithValue("https://x/b", "boom"))

		fixed := failed.Merge(repos[2], nil, nil)
		Expect(fixed.Errors).NotTo(HaveKey("https://x/down"))
	})

	It("marks repositories as scanned without touching the original", func() {
		idx := registry.Build(context.Background(), f, repos[:1], false, nil)
		extra := registry.Repo{Name: "c", URL: "https://x/c", Ref: "v1"}
		marked := idx.MarkScanned(extra)
		Expect(idx.Scanned).NotTo(HaveKey("https://x/c#v1"))
		Expect(marked.Scanned).To(HaveKeyWithValue("https://x/c#v1", idx.BuiltAt))
		Expect(marked.Scanned).To(HaveKeyWithValue("https://x/a", idx.Scanned["https://x/a"]))
		Expect(marked.Missing([]registry.Repo{repos[0], extra})).To(BeEmpty())
	})

	It("reports repositories that were never indexed or failed long ago", func() {
		idx := registry.Build(context.Background(), f, repos[:2], false, nil)
		extra := registry.Repo{Name: "c", URL: "https://x/c"}
		Expect(idx.Missing(append(repos, extra))).To(Equal([]registry.Repo{repos[2], extra}))

		idx = registry.Build(context.Background(), f, repos, false, nil)
		Expect(idx.Missing(repos)).To(BeEmpty(), "recent failures are not retried")
		idx.Scanned["https://x/down"] = time.Now().Add(-2 * registry.ErrorRetry)
		Expect(idx.Missing(repos)).To(Equal([]registry.Repo{repos[2]}))

		Expect(idx.Retain(repos[:1]).Entries).To(HaveLen(2))
		Expect(idx.Retain(repos[:1]).Errors).To(BeEmpty())
	})

	It("re-indexes a repository whose ref changed", func() {
		idx := registry.Build(context.Background(), f, repos[:2], false, nil)
		moved := registry.Repo{Name: "a", URL: "https://x/a", Ref: "v2"}
		Expect(idx.Missing([]registry.Repo{moved, repos[1]})).To(Equal([]registry.Repo{moved}))
		Expect(idx.Retain([]registry.Repo{moved}).Entries).To(HaveLen(2), "old entries stay until re-indexed")
	})

	It("loads caches written before scan times were recorded", func() {
		p := filepath.Join(GinkgoT().TempDir(), "index.json")
		Expect(os.WriteFile(p, []byte(`{"built_at":"2020-01-01T00:00:00Z","entries":[{"name":"pdf","path":"pdf","repo":{"name":"a","url":"https://x/a"}}],"errors":{"https://x/down":"x"}}`), 0o644)).To(Succeed())
		idx, fresh, err := registry.LoadIndexAny(p, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh).To(BeFalse())
		Expect(idx.Missing(repos)).To(Equal([]registry.Repo{repos[1], repos[2]}))
	})

	It("saves atomically without leaving temp files", func() {
		dir := GinkgoT().TempDir()
		p := registry.IndexPath(dir)
		idx := registry.Build(context.Background(), f, repos, false, nil)
		Expect(idx.Save(p)).To(Succeed())
		Expect(idx.Save(p)).To(Succeed())
		files, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(HaveLen(1))
	})
})

var _ = Describe("Indexer", func() {
	var (
		f     countingFetcher
		repos []registry.Repo
		x     *registry.Indexer
	)

	BeforeEach(func() {
		a := GinkgoT().TempDir()
		Expect(testutil.WriteFiles(a, map[string]string{"pdf/SKILL.md": testutil.SkillMD("pdf", "PDF", "")})).To(Succeed())
		b := GinkgoT().TempDir()
		Expect(testutil.WriteFiles(b, map[string]string{"docx/SKILL.md": testutil.SkillMD("docx", "Word", "")})).To(Succeed())
		f = countingFetcher{dirs: fakeFetcher{"https://x/a": a, "https://x/b": b}, calls: map[string]int{}}
		repos = []registry.Repo{{Name: "a", URL: "https://x/a"}, {Name: "b", URL: "https://x/b"}}
		x = &registry.Indexer{Fetcher: &f, Path: registry.IndexPath(GinkgoT().TempDir()), Repos: func() []registry.Repo { return repos }, TTL: time.Hour}
	})

	It("builds once even when started concurrently and saves the result", func() {
		d1 := x.Start(context.Background(), false, nil)
		d2 := x.Start(context.Background(), false, nil)
		Eventually(d1).Should(BeClosed())
		Eventually(d2).Should(BeClosed())
		Expect(f.count()).To(Equal(map[string]int{"https://x/a": 1, "https://x/b": 1}))
		idx, st := x.Snapshot()
		Expect(st.Building).To(BeFalse())
		Expect(st.Done).To(Equal(2))
		Expect(idx.Entries).To(HaveLen(2))
		cached, fresh, err := registry.LoadIndexAny(x.Path, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh).To(BeTrue())
		Expect(cached.Entries).To(HaveLen(2))
	})

	It("uses a fresh cache and indexes only new repositories", func() {
		_, err := x.Wait(context.Background(), false, nil)
		Expect(err).NotTo(HaveOccurred())

		y := &registry.Indexer{Fetcher: &f, Path: x.Path, Repos: x.Repos, TTL: time.Hour}
		Eventually(y.Start(context.Background(), false, nil)).Should(BeClosed())
		Expect(f.count()).To(Equal(map[string]int{"https://x/a": 1, "https://x/b": 1}))

		c := GinkgoT().TempDir()
		Expect(testutil.WriteFiles(c, map[string]string{"go/SKILL.md": testutil.SkillMD("go", "Go", "")})).To(Succeed())
		f.dirs["https://x/c"] = c
		repos = append(repos, registry.Repo{Name: "c", URL: "https://x/c"})
		idx, err := y.Wait(context.Background(), false, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(idx.Entries).To(HaveLen(3))
		Expect(f.count()).To(Equal(map[string]int{"https://x/a": 1, "https://x/b": 1, "https://x/c": 1}))
	})

	It("does not re-index repositories from a cache written before scan times were recorded", func() {
		pinned := registry.Repo{Name: "c", URL: "https://x/c", Ref: "v1"}
		repos = append(repos, pinned)
		built := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
		old := `{"built_at":"` + built + `","entries":[{"name":"pdf","path":"pdf","repo":{"name":"a","url":"https://x/a"}}],"errors":{"https://x/c":"boom"}}`
		Expect(os.WriteFile(x.Path, []byte(old), 0o644)).To(Succeed())

		idx, err := x.Wait(context.Background(), false, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.count()).To(BeEmpty(), "b had no skills and c failed at its pinned ref")
		Expect(idx.Errors).To(HaveKey("https://x/c"))

		cached, _, err := registry.LoadIndexAny(x.Path, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(cached.Scanned).To(HaveKey("https://x/a"))
		Expect(cached.Scanned).To(HaveKey("https://x/b"))
		Expect(cached.Scanned).To(HaveKey("https://x/c#v1"), "the converted cache is saved")
	})

	It("serves requests made during a build before completing it", func() {
		f.gate, f.gateURL = make(chan struct{}), "https://x/b"
		done := x.Start(context.Background(), false, nil)
		Eventually(func() int { _, st := x.Snapshot(); return st.Done }).Should(Equal(1))

		c := GinkgoT().TempDir()
		Expect(testutil.WriteFiles(c, map[string]string{"go/SKILL.md": testutil.SkillMD("go", "Go", "")})).To(Succeed())
		f.dirs["https://x/c"] = c
		repos = append(repos, registry.Repo{Name: "c", URL: "https://x/c"})
		Expect(x.Start(context.Background(), true, nil)).To(Equal(done), "joins the running build")
		x.Start(context.Background(), false, nil)

		close(f.gate)
		Eventually(done).Should(BeClosed())
		idx, st := x.Snapshot()
		Expect(st.Building).To(BeFalse())
		Expect(idx.Entries).To(HaveLen(3))
		Expect(f.refreshed).To(Equal(3), "the queued refresh re-fetched every repository once")
	})

	It("reports progress to every queued caller and survives a cancelled one", func() {
		f.gate, f.gateURL = make(chan struct{}), "https://x/b"
		done := x.Start(context.Background(), false, nil)
		Eventually(func() int { _, st := x.Snapshot(); return st.Done }).Should(Equal(1))

		var mu sync.Mutex
		calls := map[string]int{}
		counter := func(name string) registry.Progress {
			return func(int, int, registry.Repo, []registry.Entry, error) {
				mu.Lock()
				defer mu.Unlock()
				calls[name]++
			}
		}
		x.Start(context.Background(), true, counter("live"))
		dead, cancel := context.WithCancel(context.Background())
		cancel()
		x.Start(dead, false, counter("cancelled"))

		close(f.gate)
		Eventually(done).Should(BeClosed())
		_, st := x.Snapshot()
		Expect(st.Err).NotTo(HaveOccurred(), "the follow-up build ran with the live context")
		mu.Lock()
		defer mu.Unlock()
		Expect(calls).To(Equal(map[string]int{"live": 2, "cancelled": 2}))
	})

	It("retries when the joined build timed out through another caller", func() {
		f.gate, f.gateURL = make(chan struct{}), "https://x/b"
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		done := x.Start(ctx, false, nil)
		Eventually(done).Should(BeClosed())
		_, st := x.Snapshot()
		Expect(st.Err).To(MatchError(context.DeadlineExceeded))

		close(f.gate)
		idx, err := x.Wait(context.Background(), false, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(idx.Entries).To(HaveLen(2))
	})

	It("drops removed repositories from a fresh index", func() {
		_, err := x.Wait(context.Background(), false, nil)
		Expect(err).NotTo(HaveOccurred())
		repos = repos[:1]
		idx, err := x.Wait(context.Background(), false, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(idx.Entries).To(HaveLen(1))
		Expect(idx.Scanned).NotTo(HaveKey("https://x/b"))

		cached, _, err := registry.LoadIndexAny(x.Path, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(cached.Entries).To(HaveLen(1), "the trimmed index is saved")
		Expect(cached.Scanned).NotTo(HaveKey("https://x/b"))
	})

	It("saves an index trimmed by a request queued during a build", func() {
		_, err := x.Wait(context.Background(), false, nil)
		Expect(err).NotTo(HaveOccurred())

		c := GinkgoT().TempDir()
		Expect(testutil.WriteFiles(c, map[string]string{"go/SKILL.md": testutil.SkillMD("go", "Go", "")})).To(Succeed())
		f.dirs["https://x/c"] = c
		f.gate, f.gateURL = make(chan struct{}), "https://x/c"
		repos = append(repos, registry.Repo{Name: "c", URL: "https://x/c"})
		done := x.Start(context.Background(), false, nil)

		repos = repos[:1]
		x.Start(context.Background(), false, nil)
		close(f.gate)
		Eventually(done).Should(BeClosed())

		cached, _, err := registry.LoadIndexAny(x.Path, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(cached.Entries).To(HaveLen(1))
		Expect(cached.Scanned).To(HaveLen(1))
	})

	It("rebuilds for a waiter whose build was cancelled by another caller", func() {
		f.gate, f.gateURL = make(chan struct{}), "https://x/b"
		ctx, cancel := context.WithCancel(context.Background())
		x.Start(ctx, false, nil)

		got := make(chan *registry.Index)
		go func() {
			defer GinkgoRecover()
			idx, err := x.Wait(context.Background(), false, nil)
			Expect(err).NotTo(HaveOccurred())
			got <- idx
		}()
		Eventually(func() int { _, st := x.Snapshot(); return st.Done }).Should(Equal(1))
		cancel()
		close(f.gate)
		var idx *registry.Index
		Eventually(got).Should(Receive(&idx))
		Expect(idx.Entries).To(HaveLen(2))
	})

	It("completes a cancelled first build on the next start", func() {
		f.gate, f.gateURL = make(chan struct{}), "https://x/b"
		ctx, cancel := context.WithCancel(context.Background())
		done := x.Start(ctx, false, nil)
		Eventually(func() int { _, st := x.Snapshot(); return st.Done }).Should(Equal(1))
		cancel()
		Eventually(done).Should(BeClosed())

		close(f.gate)
		idx, err := x.Wait(context.Background(), false, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(idx.Entries).To(HaveLen(2))
		Expect(f.count()).To(Equal(map[string]int{"https://x/a": 2, "https://x/b": 2}), "rebuilt fully")
	})

	It("serves a stale cache and partial results while rebuilding", func() {
		stale := registry.Build(context.Background(), f.dirs, repos, false, nil)
		stale.BuiltAt = time.Now().Add(-2 * time.Hour)
		Expect(stale.Save(x.Path)).To(Succeed())
		f.dirs["block"] = "https://x/b"

		ctx, cancel := context.WithCancel(context.Background())
		done := x.Start(ctx, false, nil)
		Eventually(func() int { _, st := x.Snapshot(); return st.Done }).Should(Equal(1))
		idx, st := x.Snapshot()
		Expect(st.Building).To(BeTrue())
		Expect(st.Total).To(Equal(2))
		Expect(idx.Entries).To(HaveLen(2), "stale entries stay searchable")

		cancel()
		Eventually(done).Should(BeClosed())
		_, st = x.Snapshot()
		Expect(st.Err).To(MatchError(context.Canceled))
		cached, _, err := registry.LoadIndexAny(x.Path, time.Hour)
		Expect(err).NotTo(HaveOccurred())
		Expect(cached.BuiltAt).To(BeTemporally("~", stale.BuiltAt, time.Second), "cancelled builds are not saved")
	})
})

type countingFetcher struct {
	dirs fakeFetcher
	// gate, if set, holds fetches of gateURL until it is closed.
	gate    chan struct{}
	gateURL string

	mu        sync.Mutex
	calls     map[string]int
	refreshed int
}

func (c *countingFetcher) Fetch(ctx context.Context, src, ref string, refresh bool) (*source.Checkout, error) {
	c.mu.Lock()
	c.calls[src]++
	if refresh {
		c.refreshed++
	}
	gate := c.gate
	c.mu.Unlock()
	if gate != nil && src == c.gateURL {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.dirs.Fetch(ctx, src, ref, refresh)
}

func (c *countingFetcher) count() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.calls)
}

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

	It("strips control characters from results", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"skills":[{"id":"a/b/x","name":"x\u001b[2J","source":"a/b\u202e"}]}`))
		}))
		DeferCleanup(srv.Close)
		hits, err := (&registry.RemoteSearch{BaseURL: srv.URL, Client: srv.Client()}).Search(context.Background(), "x", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(hits).To(ConsistOf(registry.RemoteSkill{ID: "a/b/x", Name: "x [2J", Source: "a/b"}))
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
