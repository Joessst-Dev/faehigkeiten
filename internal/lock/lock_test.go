package lock_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
)

var _ = Describe("Lockfile", func() {
	var p string

	BeforeEach(func() {
		p = filepath.Join(GinkgoT().TempDir(), lock.FileName)
	})

	It("resolves the path per scope", func() {
		Expect(lock.PathFor(agent.ScopeProject, "/repo", "/cfg")).To(Equal(filepath.Join("/repo", lock.FileName)))
		Expect(lock.PathFor(agent.ScopeGlobal, "", "/cfg")).To(Equal(filepath.Join("/cfg", lock.GlobalFileName)))
		_, err := lock.PathFor(agent.ScopeProject, "", "/cfg")
		Expect(err).To(HaveOccurred())
	})

	It("loads an empty lockfile when none exists", func() {
		f, err := lock.Load(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Skills).To(BeEmpty())
		Expect(f.Path()).To(Equal(p))
	})

	It("round-trips entries sorted by name", func() {
		f, _ := lock.Load(p)
		now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
		f.Upsert(lock.Entry{Name: "zeta", Source: "s", Path: "zeta", Agents: []string{"codex"}, Tracking: lock.TrackHash, Hash: "sha256:abc", InstalledAt: now})
		f.Upsert(lock.Entry{Name: "alpha", Source: "s", Path: "alpha", Agents: []string{"claude-code"}, Tracking: lock.TrackVersion, Version: "1.0.0", InstalledAt: now})
		Expect(f.Save()).To(Succeed())

		loaded, err := lock.Load(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.Skills).To(HaveLen(2))
		Expect(loaded.Skills[0].Name).To(Equal("alpha"))
		Expect(loaded.Skills[1].Hash).To(Equal("sha256:abc"))
		Expect(loaded.Skills[1].InstalledAt.Equal(now)).To(BeTrue())
	})

	It("replaces entries with the same name", func() {
		f, _ := lock.Load(p)
		f.Upsert(lock.Entry{Name: "a", Version: "1"})
		f.Upsert(lock.Entry{Name: "a", Version: "2"})
		Expect(f.Skills).To(HaveLen(1))
		e, ok := f.Get("a")
		Expect(ok).To(BeTrue())
		Expect(e.Version).To(Equal("2"))
	})

	It("deletes the file once the last entry is removed", func() {
		f, _ := lock.Load(p)
		f.Upsert(lock.Entry{Name: "a"})
		Expect(f.Save()).To(Succeed())
		Expect(f.Remove("a")).To(BeTrue())
		Expect(f.Save()).To(Succeed())
		_, err := os.Stat(p)
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("refuses lockfiles from newer releases", func() {
		Expect(os.WriteFile(p, []byte("version: 99\nskills: []\n"), 0o644)).To(Succeed())
		_, err := lock.Load(p)
		Expect(err).To(MatchError(ContainSubstring("upgrade")))
	})

	DescribeTable("rejects entries that escape the skill directories",
		func(name, path string) {
			Expect(os.WriteFile(p, []byte("version: 1\nskills:\n  - name: "+name+"\n    path: "+path+"\n"), 0o644)).To(Succeed())
			_, err := lock.Load(p)
			Expect(err).To(MatchError(ContainSubstring("invalid")))
		},
		Entry("parent name", "'../../victim'", "skills/x"),
		Entry("nested name", "'a/b'", "skills/x"),
		Entry("windows separator", `'a\b'`, "skills/x"),
		Entry("dot dot", "'..'", "skills/x"),
		Entry("empty name", "''", "skills/x"),
		Entry("parent path", "x", "'../../home/u/.ssh'"),
		Entry("absolute path", "x", "/etc"),
		Entry("hidden name", "'.ssh'", "skills/x"),
		Entry("drive or stream separator", "'c:x'", "skills/x"),
		Entry("escape sequence in name", `"x\e[2J"`, "skills/x"),
		Entry("escape sequence in path", "x", `"skills/\e[2J"`),
	)

	It("rejects control characters in other fields", func() {
		Expect(os.WriteFile(p, []byte("version: 1\nskills:\n  - name: x\n    source: \"https://e.com/\\e]8;;x\"\n"), 0o644)).To(Succeed())
		_, err := lock.Load(p)
		Expect(err).To(MatchError(ContainSubstring("invalid value")))
	})

	It("accepts root-level skills", func() {
		Expect(os.WriteFile(p, []byte("version: 1\nskills:\n  - name: solo\n    path: .\n"), 0o644)).To(Succeed())
		_, err := lock.Load(p)
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("ParseTracking",
		func(in string, want lock.Tracking, ok bool) {
			got, err := lock.ParseTracking(in)
			if !ok {
				Expect(err).To(HaveOccurred())
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("version", "version", lock.TrackVersion, true),
		Entry("hash uppercase", "HASH", lock.TrackHash, true),
		Entry("none", "none", lock.TrackNone, true),
		Entry("invalid", "sometimes", lock.Tracking(""), false),
	)
})
