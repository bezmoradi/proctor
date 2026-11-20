package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"proctor/internal/integrity"
	"proctor/internal/model"
	"proctor/internal/runner"
)

const (
	receivedFormat   = "proctor-received/1"
	receivedManifest = "manifest.json"
	receivedBase     = "proctor-received"
)

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type receivedEntry struct {
	Specification  string `json:"specification"`
	Scenario       string `json:"scenario"`
	File           string `json:"file"`
	SHA256         string `json:"sha256"`
	Approved       string `json:"approved"`
	ApprovedSHA256 string `json:"approved_sha256,omitempty"`
	Result         string `json:"result"`
	ScenarioResult string `json:"scenario_result"`
}

type receivedDoc struct {
	Format         string            `json:"format"`
	RunID          string            `json:"run_id"`
	Mode           string            `json:"mode"`
	ProctorVersion string            `json:"proctor_version"`
	Repository     string            `json:"repository"`
	Commit         string            `json:"commit,omitempty"`
	Config         string            `json:"config"`
	Inputs         map[string]string `json:"sha256"`
	ScrubSHA256    string            `json:"scrub_sha256"`
	AppLogsSHA256  string            `json:"app_logs_sha256"`
	WrittenAt      string            `json:"written_at"`
	Transcripts    []receivedEntry   `json:"transcripts"`
	Logs           []receivedEntry   `json:"logs"`
}

type receivedDir struct {
	dir  string
	repo string
}

type pathError struct {
	code int
	msg  string
}

func (e *pathError) Error() string { return e.msg }

func invalidPath(format string, args ...any) error {
	return &pathError{code: exitInvalid, msg: fmt.Sprintf(format, args...)}
}

func pathErrorCode(err error) int {
	var pe *pathError
	if errors.As(err, &pe) {
		return pe.code
	}
	return exitError
}

func wantsTranscripts(rep *integrity.Report) bool {
	for _, s := range rep.Specs {
		for i := range s.Spec.Scenarios {
			if s.Spec.Scenarios[i].TranscriptWanted() || s.Spec.Scenarios[i].ApprovedLogWanted() {
				return true
			}
		}
	}
	return false
}

func scrubSHA(cfg *model.Config) string {
	data, _ := json.Marshal(cfg.Transcripts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func appLogsSHA(cfg *model.Config) string {
	data, _ := json.Marshal(cfg.AppLogs)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func inputHashes(rep *integrity.Report) map[string]string {
	out := map[string]string{}
	for k, v := range rep.Hashes {
		if !rep.IsApproved(k) {
			out[k] = v
		}
	}
	return out
}

func projectSegment(name string) string {
	if safeSegment.MatchString(name) {
		return name
	}
	var b strings.Builder
	for _, r := range name {
		if r < 0x80 && (r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	s := strings.TrimLeft(b.String(), "._-")
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		return "project"
	}
	return s
}

func repoRoot(dir string) (string, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	return filepath.Abs(real)
}

func insideRepo(existing, real, root string) bool {
	if r, err := filepath.Rel(root, real); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return true
	}
	rfi, err := os.Stat(root)
	if err != nil {
		return true
	}
	for d := existing; ; {
		if dfi, err := os.Stat(d); err == nil && os.SameFile(dfi, rfi) {
			return true
		}
		up := filepath.Dir(d)
		if up == d {
			return false
		}
		d = up
	}
}

func refuseInside(what, existing, real, root string) error {
	if insideRepo(existing, real, root) {
		return invalidPath("%s %s is inside the repository (%s); received transcripts are written outside it, so a run never changes the checkout and they can't be committed by mistake", what, real, root)
	}
	return nil
}

func privateFolder(p string) error {
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if fi, err = os.Lstat(p); err != nil {
			return err
		}
	case err != nil:
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return invalidPath("%s is a symbolic link; Proctor keeps received transcripts only in real folders", p)
	case !fi.IsDir():
		return invalidPath("%s isn't a folder", p)
	case !ownedByMe(fi):
		return invalidPath("%s belongs to another user; Proctor keeps received transcripts only in folders of its own", p)
	case fi.Mode().Perm()&0o022 != 0:
		return invalidPath("%s can be written by other users (mode %s); Proctor keeps received transcripts only in folders only you can write", p, fi.Mode().Perm())
	}
	return nil
}

func checkReceivedFlag(flag, root string) (string, bool, error) {
	abs, err := filepath.Abs(flag)
	if err != nil {
		return "", false, err
	}
	abs = filepath.Clean(abs)
	fi, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		parent := filepath.Dir(abs)
		pfi, err := os.Stat(parent)
		if err != nil || !pfi.IsDir() {
			return "", false, invalidPath("--received %s: its folder %s doesn't exist; create it first", flag, parent)
		}
		real, err := filepath.EvalSymlinks(parent)
		if err != nil {
			return "", false, err
		}
		target := filepath.Join(real, filepath.Base(abs))
		return target, true, refuseInside("--received", parent, target, root)
	case err != nil:
		return "", false, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return "", false, invalidPath("--received %s is a symbolic link; give a real folder, new or empty", flag)
	case !fi.IsDir():
		return "", false, invalidPath("--received %s isn't a folder", flag)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false, err
	}
	return real, false, refuseInside("--received", abs, real, root)
}

func defaultReceivedBase(project string) (string, string, error) {
	tmp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", "", err
	}
	real, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		return "", "", err
	}
	return tmp, filepath.Join(real, receivedBase, projectSegment(project)), nil
}

func prepareReceived(flag, repoDir string, rep *integrity.Report, runID string) (*receivedDir, error) {
	root, err := repoRoot(repoDir)
	if err != nil {
		return nil, err
	}
	needed := wantsTranscripts(rep)
	if flag != "" {
		dir, isNew, err := checkReceivedFlag(flag, root)
		if err != nil || !needed {
			return nil, err
		}
		if isNew {
			if err := os.Mkdir(dir, 0o700); err != nil {
				return nil, err
			}
		} else {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			if len(entries) > 0 {
				return nil, invalidPath("--received %s isn't empty (%d entries); give a new or empty folder, so proctor approve reads only this run's files", flag, len(entries))
			}
		}
		if err := privateFolder(dir); err != nil {
			return nil, err
		}
		return &receivedDir{dir: dir, repo: root}, nil
	}
	if !needed {
		return nil, nil
	}
	tmp, base, err := defaultReceivedBase(rep.Config.Project)
	if err != nil {
		return nil, err
	}
	if err := refuseInside("the temporary folder", tmp, base, root); err != nil {
		return nil, err
	}
	for _, p := range []string{filepath.Dir(base), base} {
		if err := privateFolder(p); err != nil {
			return nil, err
		}
	}
	dir := filepath.Join(base, runID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	if err := privateFolder(dir); err != nil {
		return nil, err
	}
	return &receivedDir{dir: dir, repo: root}, nil
}

func receivedName(name string) (string, bool) {
	for _, suffix := range []string{model.TranscriptSuffix, model.ApprovedLogSuffix} {
		if id, ok := strings.CutSuffix(name, suffix); ok {
			return id, true
		}
	}
	return "", false
}

func (d *receivedDir) write(specID, name string, data []byte) (string, error) {
	id, ok := receivedName(name)
	if !ok {
		return "", fmt.Errorf("%q isn't the name of a received transcript or log", name)
	}
	for _, seg := range []string{specID, id} {
		if !safeSegment.MatchString(seg) {
			return "", fmt.Errorf("%q can't name a received file", seg)
		}
	}
	sub := filepath.Join(d.dir, specID)
	if err := privateFolder(sub); err != nil {
		return "", err
	}
	if err := writePrivate(filepath.Join(sub, name), data); err != nil {
		return "", err
	}
	return specID + "/" + name, nil
}

func (d *receivedDir) finish(out *runner.Outcome, rep *integrity.Report, mode, commit string) error {
	doc := receivedDoc{Format: receivedFormat, RunID: out.RunID, Mode: mode, ProctorVersion: Version, Repository: d.repo, Commit: commit,
		Config: rep.ConfigPath, Inputs: inputHashes(rep), ScrubSHA256: scrubSHA(rep.Config), AppLogsSHA256: appLogsSHA(rep.Config),
		WrittenAt: time.Now().UTC().Format(time.RFC3339), Transcripts: []receivedEntry{}, Logs: []receivedEntry{}}
	entry := func(owner string, tr *runner.TranscriptResult) receivedEntry {
		return receivedEntry{Specification: tr.SpecID, Scenario: owner, File: tr.Received, SHA256: tr.ReceivedSHA256,
			Approved: tr.ApprovedPath, ApprovedSHA256: tr.ApprovedSHA256, Result: tr.Result, ScenarioResult: string(tr.Own)}
	}
	for _, sc := range out.Scenarios {
		if tr := sc.Transcript; tr != nil {
			doc.Transcripts = append(doc.Transcripts, entry(sc.ID, tr))
		}
		if lr := sc.ApprovedLog; lr != nil {
			doc.Logs = append(doc.Logs, entry(sc.ID, lr))
		}
	}
	for _, t := range out.Tables {
		if lr := t.ApprovedLog; lr != nil {
			doc.Logs = append(doc.Logs, entry(t.ID, lr))
		}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out.Received = d.dir
	return writePrivate(filepath.Join(d.dir, receivedManifest), append(data, '\n'))
}
