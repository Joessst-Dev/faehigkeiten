package skill_test

import (
	"os"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

const (
	mitText    = "MIT License\n\nCopyright (c) 2026 me\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\n...\nThe above copyright notice and this permission notice shall be included in all\ncopies or substantial portions of the Software.\n"
	apacheText = "                                 Apache License\n                           Version 2.0, January 2004\n"
)

func skillMD(license string) string {
	return "---\nname: pdf\ndescription: PDFs\nlicense: " + license + "\n---\n"
}

var _ = Describe("Identify", func() {
	DescribeTable("recognises well-known licenses",
		func(text, id string) { Expect(skill.Identify(text)).To(Equal(id)) },
		Entry("MIT", mitText, "MIT"),
		Entry("Apache", apacheText, "Apache-2.0"),
		Entry("GPL-3.0", "GNU GENERAL PUBLIC LICENSE\n Version 3, 29 June 2007", "GPL-3.0"),
		Entry("AGPL-3.0", "GNU AFFERO GENERAL PUBLIC LICENSE\nVersion 3, 19 November 2007", "AGPL-3.0"),
		Entry("BSD-3-Clause", "Redistribution and use in source and binary forms ... Neither the name of", "BSD-3-Clause"),
		Entry("BSD-2-Clause", "Redistribution and use in source and binary forms, with or without", "BSD-2-Clause"),
		Entry("unknown", "All rights reserved.", ""),
	)
})

var _ = Describe("DetectLicense", func() {
	var root, dir string

	BeforeEach(func() {
		root = GinkgoT().TempDir()
		dir = filepath.Join(root, "skills", "pdf")
	})

	resolve := func() skill.License {
		skills, err := skill.Discover(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(skills).To(HaveLen(1))
		return skills[0].License
	}

	It("prefers an SPDX identifier in the frontmatter", func() {
		Expect(testutil.WriteFiles(root, map[string]string{"skills/pdf/SKILL.md": skillMD("mit"), "LICENSE": apacheText})).To(Succeed())
		Expect(resolve()).To(Equal(skill.License{ID: "MIT", File: "LICENSE"}))
	})

	It("keeps SPDX expressions", func() {
		Expect(testutil.WriteFiles(root, map[string]string{"skills/pdf/SKILL.md": skillMD("(MIT OR Apache-2.0)")})).To(Succeed())
		Expect(resolve().ID).To(Equal("(MIT OR Apache-2.0)"))
	})

	It("identifies the file a frontmatter sentence points to", func() {
		Expect(testutil.WriteFiles(root, map[string]string{
			"skills/pdf/SKILL.md":    skillMD("Complete terms in LICENSE.txt"),
			"skills/pdf/LICENSE.txt": apacheText,
		})).To(Succeed())
		Expect(resolve()).To(Equal(skill.License{ID: "Apache-2.0", File: "skills/pdf/LICENSE.txt"}))
	})

	It("keeps a frontmatter sentence when the file is not a known license", func() {
		Expect(testutil.WriteFiles(root, map[string]string{
			"skills/pdf/SKILL.md":    skillMD("Proprietary. LICENSE.txt has complete terms"),
			"skills/pdf/LICENSE.txt": "All rights reserved.",
		})).To(Succeed())
		l := resolve()
		Expect(l.ID).To(Equal("Proprietary. LICENSE.txt has complete terms"))
		Expect(l.Label()).To(Equal("Proprietary"))
		Expect(l.File).To(Equal("skills/pdf/LICENSE.txt"))
	})

	It("prefers the skill's license file over the repository's", func() {
		Expect(testutil.WriteFiles(root, map[string]string{
			"skills/pdf/SKILL.md": testutil.SkillMD("pdf", "", ""),
			"skills/pdf/COPYING":  apacheText,
			"LICENSE":             mitText,
		})).To(Succeed())
		Expect(resolve()).To(Equal(skill.License{ID: "Apache-2.0", File: "skills/pdf/COPYING"}))
	})

	It("falls back to the repository's license file", func() {
		Expect(testutil.WriteFiles(root, map[string]string{
			"skills/pdf/SKILL.md": testutil.SkillMD("pdf", "", ""),
			"license.md":          mitText,
		})).To(Succeed())
		Expect(resolve()).To(Equal(skill.License{ID: "MIT", File: "license.md"}))
	})

	It("labels unknown license files as custom", func() {
		Expect(testutil.WriteFiles(root, map[string]string{
			"skills/pdf/SKILL.md": testutil.SkillMD("pdf", "", ""),
			"LICENSE":             "All rights reserved.",
		})).To(Succeed())
		l := resolve()
		Expect(l.ID).To(BeEmpty())
		Expect(l.Label()).To(Equal("custom"))
	})

	It("reports no license", func() {
		Expect(testutil.WriteFiles(root, map[string]string{"skills/pdf/SKILL.md": testutil.SkillMD("pdf", "", "")})).To(Succeed())
		Expect(resolve().Label()).To(BeEmpty())
	})

	It("ignores symlinked license files", func() {
		if runtime.GOOS == "windows" {
			Skip("symlinks need privileges on Windows")
		}
		secret := filepath.Join(GinkgoT().TempDir(), "secret")
		Expect(os.WriteFile(secret, []byte(mitText), 0o644)).To(Succeed())
		Expect(testutil.WriteFiles(root, map[string]string{"skills/pdf/SKILL.md": testutil.SkillMD("pdf", "", "")})).To(Succeed())
		Expect(os.Symlink(secret, filepath.Join(dir, "LICENSE"))).To(Succeed())
		Expect(resolve().Label()).To(BeEmpty())
	})
})

var _ = Describe("ReadLicenseText", func() {
	It("keeps lines but strips control characters", func() {
		p := filepath.Join(GinkgoT().TempDir(), "LICENSE")
		Expect(os.WriteFile(p, []byte("MIT\x1b[2J License\r\n\n\tindented\n"), 0o644)).To(Succeed())
		text, err := skill.ReadLicenseText(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(text).To(Equal("MIT [2J License\n\n    indented\n"))
	})
})
