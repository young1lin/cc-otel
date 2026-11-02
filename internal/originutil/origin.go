// Package originutil hosts the port-aware same-host Origin check shared by
// the dashboard's sameOriginGuard and the OTLP/HTTP receiver's origin reject.
// Keeping ONE copy matters: the rules (bare-host never matches a non-default
// port; non-default ports must match exactly; only a scheme-default port
// omission is tolerated) were settled after a cross-port bypass (R4) and a
// browser drive-by injection finding (R5); drifting copies would reopen one
// of them.
package originutil

import (
	"net"
	"net/url"
	"strings"
)

// SameHost compares an Origin header value with a request Host[:port].
//
// The ONLY tolerated difference is a scheme-default port the browser omitted:
// http://host (port 80) vs host:80, https://host (443) vs host:443. A
// bare-host Origin never matches a request on a NON-default port
// (http://host vs host:18899 is cross-origin — different port means a
// different site), and non-default ports must match exactly.
func SameHost(originURL, requestHost string) bool {
	if originURL == requestHost {
		return true
	}
	u, err := url.Parse(originURL)
	if err != nil || u.Host == "" {
		return false
	}
	oh, oport, oerr := net.SplitHostPort(u.Host)
	if oerr != nil {
		oh, oport = u.Host, "" // browser omitted the port
	}
	rh, rport, rerr := net.SplitHostPort(requestHost)
	if rerr != nil {
		rh, rport = requestHost, ""
	}
	oh = strings.Trim(oh, "[]")
	rh = strings.Trim(rh, "[]")
	if oh != rh {
		return false
	}
	if oport == rport {
		return true
	}
	// Tolerate exactly the default-port omission for the origin's scheme.
	defaultPort := "80"
	if u.Scheme == "https" {
		defaultPort = "443"
	}
	return (oport == "" && rport == defaultPort) || (rport == "" && oport == defaultPort)
}
