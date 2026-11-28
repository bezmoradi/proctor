package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

const hostArg = "host"

func validHostName(h string) error {
	switch {
	case h == "":
		return fmt.Errorf("host is empty")
	case len(h) > 253:
		return fmt.Errorf("host %q is longer than 253 characters", h)
	case strings.Trim(h, "abcdefghijklmnopqrstuvwxyz0123456789-.") != "":
		return fmt.Errorf("host %q must be a host name in lower case: letters, digits, - and ., with no scheme, port or path", h)
	case strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") || strings.Contains(h, ".."):
		return fmt.Errorf("host %q has an empty label", h)
	}
	return nil
}

func (d *driver) allowedHost(h string) bool {
	for _, re := range d.vhosts {
		if re.MatchString(h) {
			return true
		}
	}
	return false
}

func (d *driver) hostPort(host string) string {
	if p := d.base.Port(); p != "" {
		return net.JoinHostPort(host, p)
	}
	return host
}

func (d *driver) targetBase(host string) (string, error) {
	if host == "" {
		return d.base.Scheme + "://" + d.base.Host, nil
	}
	if err := validHostName(host); err != nil {
		return "", fmt.Errorf("%v (HTTP-05)", err)
	}
	if len(d.vhosts) == 0 {
		return "", fmt.Errorf("host %q: the binding lists no virtual_hosts for this driver, so a step can't name a host (HTTP-05)", host)
	}
	if !d.allowedHost(host) {
		return "", fmt.Errorf("host %q matches none of the binding's virtual_hosts (HTTP-05)", host)
	}
	return d.base.Scheme + "://" + d.hostPort(host), nil
}

func (d *driver) dialBase(base *url.URL) func(ctx context.Context, network, addr string) (net.Conn, error) {
	var dialer net.Dialer
	port := base.Port()
	if port == "" {
		port = "80"
		if base.Scheme == "https" {
			port = "443"
		}
	}
	real := net.JoinHostPort(base.Hostname(), port)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, p, err := net.SplitHostPort(addr)
		if err == nil && p == port && d.allowedHost(host) {
			addr = real
		}
		return dialer.DialContext(ctx, network, addr)
	}
}
