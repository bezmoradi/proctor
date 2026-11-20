package commands

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

const (
	initFolder    = "proctor"
	initBaseURL   = "http://127.0.0.1:8080"
	initReadyPath = "/readyz"
	initSpec      = initFolder + "/specs/health.yaml"
	initBinding   = initFolder + "/environments/local.yaml"
)

type initOptions struct {
	project, baseURL, readyPath string
	projectSet                  bool
}

type initFile struct {
	path string
	data string
}

func newInitCmd(stdout, stderr io.Writer) *cobra.Command {
	var o initOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starting proctor/ folder in the current folder: proctor.yaml, the manifest, one specification and a local binding",
		Example: `  proctor init
  proctor init --project shop-api --base-url http://127.0.0.1:3000 --ready-path /healthz`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.projectSet = cmd.Flags().Changed("project")
			return exitWith(initRun(o, stdout, stderr))
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.project, "project", "", "the project name in proctor.yaml (default: the current folder's name, kept to letters, digits, ., _ and -)")
	f.StringVar(&o.baseURL, "base-url", initBaseURL, "where the application runs, written to the local binding")
	f.StringVar(&o.readyPath, "ready-path", initReadyPath, "the application's health path: the binding's ready_path and the path the starting scenario reads")
	return cmd
}

func initRun(o initOptions, stdout, stderr io.Writer) int {
	refuse := func(code int, msg string) int {
		fmt.Fprintln(stderr, "proctor init: "+safe(msg))
		return code
	}
	if o.projectSet && !safeSegment.MatchString(o.project) {
		return refuse(exitInvalid, fmt.Sprintf("--project %q must start with a letter or digit and hold only letters, digits, ., _ and -, at most 128 characters", o.project))
	}
	if err := checkInitBaseURL(o.baseURL); err != nil {
		return refuse(exitInvalid, err.Error())
	}
	if err := checkInitReadyPath(o.readyPath); err != nil {
		return refuse(exitInvalid, err.Error())
	}
	cwd, err := os.Getwd()
	if err != nil {
		return refuse(exitError, err.Error())
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return refuse(exitError, err.Error())
	}
	inGit, outer := gitRootOf(real)
	if outer != "" {
		return refuse(exitInvalid, fmt.Sprintf("this folder is inside the Git repository at %s, and Proctor reads proctor/ at the repository's root; run proctor init there", outer))
	}
	if !o.projectSet {
		o.project = projectSegment(filepath.Base(real))
	}
	files := initFiles(o)
	fresh, err := initPlan(files)
	if err != nil {
		return refuse(exitInvalid, err.Error())
	}
	if fresh {
		err = writeInitFresh(files)
	} else {
		err = writeInitInto(files)
	}
	if err != nil {
		return refuse(exitError, "nothing was written: "+err.Error())
	}
	fmt.Fprintln(stdout, "Wrote:")
	for _, f := range files {
		fmt.Fprintln(stdout, "  "+f.path)
	}
	fmt.Fprintln(stdout, "\nNext:")
	steps := []string{
		"Edit " + initSpec + " to say what the application must do, and list each new specification in " + initFolder + "/manifest.yaml.",
		"Start the application at " + safe(o.baseURL) + "; a run waits until GET " + safe(o.readyPath) + " answers 2xx.",
		"proctor validate",
	}
	if !inGit {
		steps = append(steps, "git init: proctor run reads the specifications from Git, so it needs a repository.")
	}
	steps = append(steps, "proctor run --binding "+initBinding)
	for i, s := range steps {
		fmt.Fprintf(stdout, "  %d. %s\n", i+1, s)
	}
	fmt.Fprintln(stdout, "\nFor CI, see "+ciGuide()+".")
	return exitOK
}

func ciGuide() string {
	if UpdateRepo == "" {
		return "examples/github/README.md in Proctor's repository"
	}
	return "https://github.com/" + UpdateRepo + "/blob/main/examples/github/README.md"
}

func checkInitBaseURL(s string) error {
	if !printable(s) {
		return errors.New("--base-url holds a control, line-break or invisible character")
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return errors.New("--base-url must start with http:// or https://, such as " + initBaseURL)
	}
	u, err := url.Parse(s)
	var ue *url.Error
	switch {
	case errors.As(err, &ue):
		return fmt.Errorf("--base-url isn't a URL: %v", ue.Err)
	case err != nil:
		return errors.New("--base-url isn't a URL")
	case u.Host == "":
		return errors.New("--base-url has no host")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return errors.New("--base-url can't contain credentials, a query or a fragment")
	}
	return nil
}

func checkInitReadyPath(p string) error {
	bad := func(why string) error { return fmt.Errorf("--ready-path %q %s", p, why) }
	switch {
	case !printable(p):
		return bad("holds a control, line-break or invisible character")
	case !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//"):
		return bad("must be a path starting with a single /, such as " + initReadyPath)
	case strings.ContainsAny(p, "?#\\ "):
		return bad("can't hold ?, #, \\ or a space; give the path alone")
	case strings.Contains(p, "${"):
		return bad("can't hold ${, which a specification reads as a reference")
	case strings.Contains(strings.ToLower(p), "%2f"), strings.Contains(strings.ToLower(p), "%5c"):
		return bad("can't hold a percent-encoded / or \\")
	}
	for _, seg := range strings.Split(p[1:], "/") {
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return bad("has a broken percent-encoding")
		}
		if dec == "." || dec == ".." {
			return bad("can't hold a . or .. segment")
		}
	}
	return nil
}

func printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unsafeRune(r) {
			return false
		}
	}
	return true
}

func gitRootOf(dir string) (inGit bool, outer string) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); !errors.Is(err, fs.ErrNotExist) {
			if d == dir {
				return true, ""
			}
			return true, d
		}
		if filepath.Dir(d) == d {
			return false, ""
		}
	}
}

func yamlScalar(s string) string {
	out, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return strings.TrimSuffix(string(out), "\n")
}

func initFiles(o initOptions) []initFile {
	return []initFile{
		{initFolder + "/proctor.yaml", `version: 2
project: ` + yamlScalar(o.project) + `
manifest: manifest.yaml
drivers:
  http: { official: http }
identities:
  anonymous: { type: none }
`},
		{initFolder + "/manifest.yaml", `version: 2
specifications:
  - id: health
    path: specs/health.yaml
`},
		{initSpec, `version: 2
suite: health
scenarios:
  - id: HLT-001
    name: The application answers its health check
    steps:
      - id: ready
        do: http.request
        identity: anonymous
        with:
          method: GET
          path: ` + yamlScalar(o.readyPath) + `
        expect:
          equal:
            $.status: 200
          absent: [$.body_invalid]
`},
		{initBinding, `drivers:
  http:
    base_url: ` + yamlScalar(o.baseURL) + `
    user_agent: proctor-local/0
    ready_path: ` + yamlScalar(o.readyPath) + `
`},
	}
}

func initPlan(files []initFile) (fresh bool, err error) {
	fi, err := os.Lstat(initFolder)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	case err != nil:
		return false, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return false, errors.New(initFolder + " is a symbolic link; init never writes through a link, so remove it or replace it with a folder")
	case !fi.IsDir():
		return false, fmt.Errorf("%s is %s, not a folder; move it away, then run proctor init again", initFolder, notFolder(fi.Mode()))
	}
	var exist []string
	seen := map[string]bool{}
	for _, f := range files {
		dir := filepath.ToSlash(filepath.Dir(f.path))
		if dir != initFolder && !seen[dir] {
			seen[dir] = true
			fi, err := os.Lstat(dir)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				continue
			case err != nil:
				return false, err
			case fi.Mode()&fs.ModeSymlink != 0:
				return false, errors.New(dir + " is a symbolic link; init never writes through a link, so remove it or replace it with a folder")
			case !fi.IsDir():
				return false, fmt.Errorf("%s is %s, not a folder; move it away, then run proctor init again", dir, notFolder(fi.Mode()))
			}
		}
		if _, err := os.Lstat(f.path); !errors.Is(err, fs.ErrNotExist) {
			if err != nil {
				return false, err
			}
			exist = append(exist, f.path)
		}
	}
	if len(exist) > 0 {
		verb, pronoun := "exists", "it"
		if len(exist) > 1 {
			verb, pronoun = "exist", "them"
		}
		return false, fmt.Errorf("%s already %s, and init never overwrites a file; move %s away, or edit the folder yourself", strings.Join(exist, ", "), verb, pronoun)
	}
	return false, nil
}

func notFolder(m fs.FileMode) string {
	if m.IsRegular() {
		return "a file"
	}
	return "a " + describeMode(m)
}

func writeInitFresh(files []initFile) error {
	tmp, err := os.MkdirTemp(".", ".proctor-init-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	stage := filepath.Join(tmp, initFolder)
	if err := os.Mkdir(stage, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		p := filepath.Join(tmp, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := createInitFile(p, f.data); err != nil {
			return err
		}
	}
	return os.Rename(stage, initFolder)
}

func writeInitInto(files []initFile) (err error) {
	var made []string
	defer func() {
		if err == nil {
			return
		}
		for i := len(made) - 1; i >= 0; i-- {
			os.Remove(made[i])
		}
	}()
	for _, f := range files {
		p := filepath.FromSlash(f.path)
		if dir := filepath.Dir(p); dir != initFolder {
			switch err := os.Mkdir(dir, 0o755); {
			case err == nil:
				made = append(made, dir)
			case !errors.Is(err, fs.ErrExist):
				return err
			}
			if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
				return fmt.Errorf("%s isn't a folder any more", filepath.ToSlash(dir))
			}
		}
		if err := createInitFile(p, f.data); err != nil {
			return err
		}
		made = append(made, p)
	}
	return nil
}

func createInitFile(p, data string) (err error) {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(p)
		}
	}()
	if _, err = f.WriteString(data); err != nil {
		return err
	}
	return f.Sync()
}
