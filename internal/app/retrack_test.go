package app_test

import (
	"context"
	"errors"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

var _ = Describe("Retracker", func() {
	var (
		a      *app.App
		in     *install.Installer
		origin string
		ctx    = context.Background()
	)

	// installHashed installs the named skill of origin with hash tracking.
	installHashed := func(name string) lock.Entry {
		skills, err := skill.Discover(origin)
		Expect(err).NotTo(HaveOccurred())
		selected, err := app.Select(skills, []string{name})
		Expect(err).NotTo(HaveOccurred())
		claude, _ := a.Agents.Get("claude-code")
		e, err := in.Install(install.Request{
			Skill:    selected[0],
			Checkout: &source.Checkout{URL: origin, Dir: origin},
			Agents:   []agent.Agent{claude},
			Tracking: lock.TrackHash,
		})
		Expect(err).NotTo(HaveOccurred())
		return e
	}

	BeforeEach(func() {
		base := GinkgoT().TempDir()
		GinkgoT().Setenv(config.EnvConfigDir, filepath.Join(base, "config"))
		GinkgoT().Setenv(config.EnvCacheDir, filepath.Join(base, "cache"))
		GinkgoT().Setenv("HOME", filepath.Join(base, "home"))
		GinkgoT().Setenv("USERPROFILE", filepath.Join(base, "home"))
		origin = filepath.Join(base, "origin")
		Expect(testutil.WriteFiles(origin, map[string]string{
			"skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "1.0.0"),
			"skills/beta/SKILL.md":  testutil.SkillMD("beta", "Unversioned", ""),
		})).To(Succeed())
		project := filepath.Join(base, "project")
		Expect(testutil.WriteFiles(project, map[string]string{"README.md": "x"})).To(Succeed())
		var err error
		a, err = app.New()
		Expect(err).NotTo(HaveOccurred())
		t, err := a.Target(agent.ScopeProject, project)
		Expect(err).NotTo(HaveOccurred())
		in, err = a.Installer(t)
		Expect(err).NotTo(HaveOccurred())
	})

	It("uses the installed version without looking upstream", func() {
		e := installHashed("alpha")
		e.Source = filepath.Join(origin, "missing") // never fetched
		got, err := a.NewRetracker(in).Retracked(ctx, e, lock.TrackVersion)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Version).To(Equal("1.0.0"))
	})

	It("tracks a skill without a version from 0.0.0 when upstream declares one", func() {
		e := installHashed("beta")
		Expect(testutil.WriteFiles(origin, map[string]string{
			"skills/beta/SKILL.md": testutil.SkillMD("beta", "Unversioned", "1.2.0"),
		})).To(Succeed())
		got, err := a.NewRetracker(in).Retracked(ctx, e, lock.TrackVersion)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Tracking).To(Equal(lock.TrackVersion))
		Expect(got.Version).To(Equal(install.UnknownVersion))
		Expect(got.Hash).To(Equal(e.Hash), "the installed content stays the baseline")
		recorded, _ := in.Lock.Get("beta")
		Expect(recorded.Tracking).To(Equal(lock.TrackHash), "the lockfile is not changed")
	})

	It("refuses version tracking when upstream has no version either", func() {
		e := installHashed("beta")
		_, err := a.NewRetracker(in).Retracked(ctx, e, lock.TrackVersion)
		Expect(err).To(MatchError(ContainSubstring("no version, installed or upstream")))
	})

	It("reports a source that cannot be fetched", func() {
		e := installHashed("beta")
		e.Source = filepath.Join(origin, "missing")
		_, err := a.NewRetracker(in).Retracked(ctx, e, lock.TrackVersion)
		Expect(err).To(MatchError(ContainSubstring("has no version installed; checking upstream")))
	})

	It("confirms a choice without computing it again", func() {
		e := installHashed("beta")
		Expect(testutil.WriteFiles(origin, map[string]string{
			"skills/beta/SKILL.md": testutil.SkillMD("beta", "Unversioned", "1.2.0"),
		})).To(Succeed())
		c := in.Retracked(e, lock.TrackVersion)
		Expect(c.NeedsUpstream).To(BeTrue())
		Expect(c.Entry.Version).To(Equal(install.UnknownVersion))
		got, err := a.NewRetracker(in).Confirm(ctx, c)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(c.Entry))
	})

	It("passes on choices that need no upstream check and their errors", func() {
		e := installHashed("alpha")
		e.Source = filepath.Join(origin, "missing") // never fetched
		r := a.NewRetracker(in)
		c := in.Retracked(e, lock.TrackNone)
		Expect(r.Confirm(ctx, c)).To(Equal(c.Entry))
		_, err := r.Confirm(ctx, install.Choice{Err: errors.New("boom")})
		Expect(err).To(MatchError("boom"))
	})

	It("leaves other modes to the installer", func() {
		e := installHashed("beta")
		got, err := a.NewRetracker(in).Retracked(ctx, e, lock.TrackNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Hash).To(BeEmpty())
	})
})
