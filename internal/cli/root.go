// Package cli implements the faehigkeiten command line interface.
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/tui"
)

// BuildInfo is injected at build time.
type BuildInfo struct {
	Version, Commit, Date string
}

// ExitError carries a specific exit code without printing an extra message.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// scopeFlags are shared by commands operating on an install scope.
type scopeFlags struct {
	global  bool
	project string
}

func (f *scopeFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&f.global, "global", "g", false, "use the global (user-wide) scope")
	cmd.Flags().StringVarP(&f.project, "project", "p", "", "project directory (default: git root of the working directory)")
	cmd.MarkFlagsMutuallyExclusive("global", "project")
}

func (f *scopeFlags) target(a *app.App) (install.Target, error) {
	if f.global {
		return a.Target(agent.ScopeGlobal, "")
	}
	return a.Target(agent.ScopeProject, f.project)
}

// NewRootCommand builds the command tree.
func NewRootCommand(info BuildInfo) *cobra.Command {
	root := &cobra.Command{
		Use:   "faehigkeiten",
		Short: "Install and track agent skills from skill repositories",
		Long: `faehigkeiten installs agent skills (folders with a SKILL.md) from skill
repositories into a project or globally, for Claude Code, Codex, Copilot,
Cursor, Gemini CLI and more, and keeps them up to date.

Run without arguments to start the interactive TUI.`,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", info.Version, info.Commit, info.Date),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return tui.Run(cmd.Context(), a, info.Version)
		},
	}
	root.AddCommand(
		newInstallCommand(),
		newListCommand(),
		newCheckCommand(),
		newUpdateCommand(),
		newRemoveCommand(),
		newAdoptCommand(),
		newSearchCommand(),
		newRepoCommand(),
		newAgentsCommand(),
	)
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(ctx context.Context, info BuildInfo, args []string, stdout, stderr io.Writer) int {
	cmd := NewRootCommand(info)
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	if err := cmd.ExecuteContext(ctx); err != nil {
		if e, ok := err.(ExitError); ok {
			return e.Code
		}
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	return 0
}

func splitList(vals []string) []string {
	var out []string
	for _, v := range vals {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
