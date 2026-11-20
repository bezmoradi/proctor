package protdiff

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"proctor/internal/gitrepo"
	"proctor/internal/model"
)

const (
	bodyFileKey   = "body_file"
	bodyBase64Key = "body_base64"
	shownBase64   = 64
)

type bodyFileSide struct {
	repo *gitrepo.Repo
	tree map[string]gitrepo.Entry
	cfg  *model.Config
	root string
}

func (s bodyFileSide) path(st model.Step) (string, bool) {
	if s.cfg == nil {
		return "", false
	}
	driver, _, ok := model.SplitDo(st.Do)
	if !ok || s.cfg.Drivers[driver].Official != officialHTTP {
		return "", false
	}
	with, isObject := st.With.(map[string]any)
	if !isObject {
		return "", false
	}
	rel, isText := with[bodyFileKey].(string)
	if !isText {
		return "", false
	}
	return s.root + "/" + rel, true
}

func (s bodyFileSide) content(p string) (sum, shown string, err error) {
	e, ok := s.tree[p]
	if !ok || !e.Regular() {
		return "", p + " (not a regular file in this commit)", nil
	}
	data, err := s.repo.Blob(e.OID)
	if err != nil {
		return "", "", err
	}
	h := sha256.Sum256(data)
	sum = hex.EncodeToString(h[:])
	return sum, fmt.Sprintf("%s sha256 %s (%d bytes)", p, sum, len(data)), nil
}

func bodyFileChanges(base, head bodyFileSide, bs, hs map[string]scenarioVersion) ([]ExpectationChange, error) {
	var out []ExpectationChange
	for _, id := range sortedKeys(hs) {
		h, b := hs[id].typed, bs[id].typed
		if h == nil || b == nil {
			continue
		}
		hm, order := indexSteps(h)
		bm, _ := indexSteps(b)
		for _, sid := range order {
			hp, ok := head.path(hm[sid].step)
			if !ok {
				continue
			}
			bl, had := bm[sid]
			if !had {
				continue
			}
			hSum, hShown, err := head.content(hp)
			if err != nil {
				return nil, err
			}
			bSum, bShown := "", "no body_file"
			if bp, ok := base.path(bl.step); ok {
				if bSum, bShown, err = base.content(bp); err != nil {
					return nil, err
				}
			}
			if (bSum != hSum || bSum == "") && bShown != hShown {
				out = append(out, ExpectationChange{Scenario: id, Step: sid, What: "body_file", From: bShown, To: hShown})
			}
		}
	}
	return out, nil
}

func shownWith(with any) string {
	m, isObject := with.(map[string]any)
	if !isObject {
		return canonical(with)
	}
	raw, isText := m[bodyBase64Key].(string)
	if !isText || len(raw) <= shownBase64 {
		return canonical(with)
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	compact := strings.Join(strings.Fields(raw), "")
	shown := fmt.Sprintf("[%d base64 characters]", len(compact))
	if b, err := base64.StdEncoding.Strict().DecodeString(compact); err == nil {
		h := sha256.Sum256(b)
		shown = fmt.Sprintf("[%d bytes, sha256 %s]", len(b), hex.EncodeToString(h[:]))
	}
	cp[bodyBase64Key] = shown
	return canonical(cp)
}
