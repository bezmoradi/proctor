package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"proctor/internal/github"
)

var UpdateRepo = "bezmoradi/proctor"

var UpdateAPI = "https://api.github.com"

const updateWait = 2 * time.Second

var checkedCommands = map[string]bool{"validate": true, "integrity": true, "diff": true, "run": true}

type updateCheck struct {
	started time.Time
	result  chan string
}

func wantsUpdateCheck(cmd *cobra.Command) bool {
	if !checkedCommands[cmd.Name()] || cmd.Parent() == nil || cmd.Parent().Parent() != nil {
		return false
	}
	if a, err := cmd.Flags().GetBool("authoritative"); err == nil && a {
		return false
	}
	for _, env := range []string{"PROCTOR_NO_UPDATE_CHECK", "CI", "GITHUB_ACTIONS"} {
		if os.Getenv(env) != "" {
			return false
		}
	}
	_, ok := parseVersion(Version)
	return ok && Version != devVersion && UpdateRepo != ""
}

func (u *updateCheck) start() {
	u.started = time.Now()
	u.result = make(chan string, 1)
	go func() { u.result <- newerVersion() }()
}

func (u *updateCheck) report(w io.Writer) {
	if u.result == nil {
		return
	}
	wait := updateWait - time.Since(u.started)
	if wait < 0 {
		wait = 0
	}
	select {
	case latest := <-u.result:
		if latest != "" {
			fmt.Fprintf(w, "\nproctor %s is available (you have %s): https://github.com/%s/releases/latest\nSet PROCTOR_NO_UPDATE_CHECK=1 to stop this check.\n", latest, Version, UpdateRepo)
		}
	case <-time.After(wait):
	}
}

func newerVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), updateWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UpdateAPI+"/repos/"+UpdateRepo+"/releases/latest", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "proctor/"+Version)
	res, err := github.Client(updateWait).Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	var info struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&info); err != nil || info.Draft || info.Prerelease {
		return ""
	}
	latest := strings.TrimPrefix(info.TagName, "v")
	if !isNewer(latest, Version) {
		return ""
	}
	return latest
}

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)

type version struct {
	core [3]int
	pre  string
}

func parseVersion(s string) (version, bool) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	var v version
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return version{}, false
		}
		v.core[i] = n
	}
	v.pre = strings.TrimPrefix(m[4], "-")
	return v, true
}

func isNewer(a, b string) bool {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	if !okA || !okB || va.pre != "" {
		return false
	}
	for i := 0; i < 3; i++ {
		if va.core[i] != vb.core[i] {
			return va.core[i] > vb.core[i]
		}
	}
	return vb.pre != ""
}
