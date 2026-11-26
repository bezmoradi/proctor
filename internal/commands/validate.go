package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/spf13/cobra"

	"proctor/internal/gitrepo"
	"proctor/internal/integrity"
	"proctor/internal/mutate"
	"proctor/internal/runner"
)

type validateOptions struct {
	common
	commit string
}

func newValidateCmd(stdout, stderr io.Writer) *cobra.Command {
	var o validateOptions
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check the configuration, manifest and specifications, with each driver's own checks, without contacting the application",
		Example: `  proctor validate
  proctor validate --commit HEAD~1 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return exitWith(validate(o, stdout, stderr))
		},
	}
	addCommon(cmd, &o.common)
	cmd.Flags().StringVar(&o.commit, "commit", "", "read files from this commit instead of the working tree")
	return cmd
}

func validate(o validateOptions, stdout, stderr io.Writer) int {
	c, commit := o.common, o.commit
	repoDir, inGit := "", true
	repo, err := gitrepo.Open(c.repo)
	if err != nil {
		var plain *gitrepo.NotRepositoryError
		if commit != "" || !errors.As(err, &plain) {
			fmt.Fprintf(stderr, "proctor: %s\n", safe(err.Error()))
			return exitInvalid
		}
		repoDir, inGit = plain.Dir, false
	} else {
		repoDir = repo.Dir()
	}
	var src integrity.Source = integrity.WorkTreeSource{Root: repoDir}
	var cs *integrity.CommitSource
	if commit != "" {
		cs, err = integrity.NewCommitSource(repo, commit)
		if err != nil {
			fmt.Fprintf(stderr, "proctor: %s\n", safe(err.Error()))
			return exitInvalid
		}
		src = cs
	}
	r := integrity.Load(src, c.config)
	switch {
	case cs != nil:
		r.CheckUnlisted(cs.RegularPaths(), nil)
	case inGit:
		r.CheckUnlisted(workTreeFiles(repoDir))
	default:
		r.CheckUnlisted(folderFiles(repoDir, r.ProtectedRoot))
	}
	if inGit {
		r.CheckTrackedWorkTree(repo.Index)
	}
	if r.Config != nil && r.Config.Mutation != nil {
		if inGit {
			checkMutation(r, repoDir, src, true)
		} else {
			r.CheckMutantFiles(repoDir, src, untrackable, true)
		}
	}
	dc := &driverCheck{outsideGit: !inGit}
	if r.Config != nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		_, dc.drivers, dc.setup = describeSteps(ctx, r, nil, runner.Options{RepoDir: repoDir})
	}
	code := r.ExitCode()
	if dc.setup != nil && dc.setup.ExitCode > code {
		code = dc.setup.ExitCode
	}
	return write(stdout, c.json, "VALIDATE", r, dc, code, resultName(code))
}

var errNoGit = errors.New("not a Git repository, so Proctor can't tell whether Git tracks this file or ignores it; validate the mutation settings in a Git repository")

func untrackable(string) (integrity.FileState, error) {
	return integrity.FileState{}, errNoGit
}

func checkMutation(r *integrity.Report, repoDir string, src integrity.Source, lines bool) {
	if cs, ok := src.(*integrity.CommitSource); ok {
		r.CheckMutantFiles(repoDir, src, cs.FileState, lines)
		return
	}
	files, err := mutate.ListFiles(repoDir)
	if err != nil {
		r.CheckMutantFiles(repoDir, src, func(string) (integrity.FileState, error) { return integrity.FileState{}, err }, lines)
		return
	}
	listed := map[string]bool{}
	for _, f := range files {
		listed[f] = true
	}
	r.CheckMutantFiles(repoDir, src, integrity.WorkTreeState(repoDir, listed), lines)
}

func workTreeFiles(dir string) ([]string, error) {
	listed, err := mutate.ListFiles(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range listed {
		if fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(p))); err == nil && fi.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	return out, nil
}

func folderFiles(dir, root string) ([]string, error) {
	if root == "" {
		return nil, nil
	}
	var out []string
	err := filepath.WalkDir(filepath.Join(dir, filepath.FromSlash(root)), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return out, err
}
