package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// proctorBin is the binary under test, built once from ./cmd/proctor.
// proctorOIDCBin is the same code built to trust an OIDC issuer named by
// PROCTOR_TEST_OIDC_ISSUER, so the authoritative tests can sign tokens
// with their own key; the release build trusts only GitHub's issuer.
var proctorBin, proctorOIDCBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "proctor-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	proctorBin = filepath.Join(dir, "proctor")
	args := []string{"build", "-o", proctorBin}
	// PROCTOR_E2E_RACE=1 builds the binary under test with the race
	// detector, so data races inside proctor fail the run.
	if os.Getenv("PROCTOR_E2E_RACE") == "1" {
		args = append(args, "-race")
	}
	proctorOIDCBin = filepath.Join(dir, "proctor-oidc-test")
	for _, cmdArgs := range [][]string{
		append(append([]string{}, args...), "./cmd/proctor"),
		append(append([]string{"build", "-o", proctorOIDCBin, "-ldflags", "-X proctor/internal/github.Issuer=env"}, args[3:]...), "./cmd/proctor"),
	} {
		build := exec.Command("go", cmdArgs...)
		build.Dir = ".."
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building proctor: %v\n%s", err, out)
			os.Exit(1)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// runProctor runs the binary as a separate process and returns its exit
// code, standard output and standard error.
func runProctor(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runBinary(t, proctorBin, args...)
}

func runBinary(t *testing.T, bin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String(), errOut.String()
	case errors.As(err, &exit):
		return exit.ExitCode(), out.String(), errOut.String()
	default:
		t.Fatalf("running proctor: %v", err)
		return -1, "", ""
	}
}
