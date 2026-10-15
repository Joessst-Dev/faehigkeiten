package app_test

import (
	"os"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

var _ = Describe("LicenseText", func() {
	It("reads license files below the root only", func() {
		root := GinkgoT().TempDir()
		Expect(testutil.WriteFiles(root, map[string]string{"skills/pdf/LICENSE": "terms"})).To(Succeed())
		Expect(app.LicenseText(root, skill.License{File: "skills/pdf/LICENSE"})).To(Equal("terms\n"))
		_, err := app.LicenseText(root, skill.License{File: "../secret"})
		Expect(err).To(MatchError(ContainSubstring("invalid license file")))
	})

	It("refuses license files behind symlinked directories", func() {
		if runtime.GOOS == "windows" {
			Skip("symlinks need privileges on Windows")
		}
		root, outside := GinkgoT().TempDir(), GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(outside, "LICENSE"), []byte("secret"), 0o644)).To(Succeed())
		Expect(os.Symlink(outside, filepath.Join(root, "link"))).To(Succeed())
		_, err := app.LicenseText(root, skill.License{File: "link/LICENSE"})
		Expect(err).To(MatchError(ContainSubstring("outside")))
	})
})
