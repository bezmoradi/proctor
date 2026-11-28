package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"

	"github.com/bezmoradi/proctor/protocol"
)

const jarPrefix = "jar-"

type jars struct {
	mu   sync.Mutex
	next int
	open map[string]*cookiejar.Jar
}

func (j *jars) add() (string, *cookiejar.Jar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.open == nil {
		j.open = map[string]*cookiejar.Jar{}
	}
	j.next++
	id := fmt.Sprintf("%s%d", jarPrefix, j.next)
	j.open[id] = jar
	return id, jar, nil
}

func (j *jars) lookup(handle string) (*cookiejar.Jar, error) {
	j.mu.Lock()
	jar, ok := j.open[handle]
	j.mu.Unlock()
	if !ok {
		if strings.HasPrefix(handle, jarPrefix) {
			return nil, protocol.Errorf(protocol.CauseInvalidRequest, "the http driver has no open cookie jar %q", handle)
		}
		return nil, protocol.Errorf(protocol.CauseInvalidRequest, "handle %q isn't a cookie jar; %s runs on a handle a %s step opened with as", handle, opRequest, opRequest)
	}
	return jar, nil
}

func (j *jars) close(handle string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.open[handle]; !ok {
		return false
	}
	delete(j.open, handle)
	return true
}

func (d *driver) clientFor(p protocol.CallParams) (*http.Client, string, error) {
	switch {
	case p.Open && p.Handle != "":
		return nil, "", protocol.Errorf(protocol.CauseInvalidRequest, "a %s either opens a cookie jar (as) or uses one (on), not both", opRequest)
	case p.Open:
		id, jar, err := d.jars.add()
		if err != nil {
			return nil, "", protocol.Errorf(protocol.CauseDriverFailed, "a cookie jar can't be made: %v", err)
		}
		return d.withJar(jar), id, nil
	case p.Handle != "":
		jar, err := d.jars.lookup(p.Handle)
		if err != nil {
			return nil, "", err
		}
		return d.withJar(jar), "", nil
	}
	return d.client, "", nil
}

func (d *driver) withJar(jar http.CookieJar) *http.Client {
	return &http.Client{Transport: d.client.Transport, CheckRedirect: d.client.CheckRedirect, Jar: jar}
}
