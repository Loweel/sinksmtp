//
// Determine if a domain name is a valid mail domain.
// This is not in the smtpd package because right now we have to do
// crazy things to determine temporary DNS failures from permanent ones.
// The problem is that DNSError.Temporary() is only true for timeout
// errors from the local DNS resolver; if the local DNS resolver returns
// a SERVFAIL result, Temporary() is *false* right now. Sigh. This leaves
// us with the fragile approach of inspecting the actual error string.

package main

import (
	"fmt"
	"net"
	"strings"
)

type dnsResult int

// note that we must be in order from bad to good results here.
const (
	dnsUndef dnsResult = iota
	dnsBad
	dnsTempfail
	dnsGood
)

func (d dnsResult) String() string {
	switch d {
	case dnsUndef:
		return "<dns-undef>"
	case dnsGood:
		return "<dns-good>"
	case dnsBad:
		return "<dns-bad>"
	case dnsTempfail:
		return "<dns-tempfail>"
	default:
		return fmt.Sprintf("<dns-%d>", d)
	}
}

// this is extremely ugly, but the net.DNSError code gives us
// no better way. serverrstr is the exact error string that
// src/net/dnsclient.go uses in answer() if the server returns
// anything but dnsRcodeSuccess or dnsRcodeNameError, and in particular
// when the rcode is dnsRcodeServerFailure (aka SERVFAIL, aka what DNS
// servers return if eg they can't talk to any of the authoritative
// servers).
var serverrstr = "server misbehaving"

// TODO: create an interface for .Temporary() and coerce the error
// to it, to pick up all net.* errors with Temporary().
// ... well, that would be DNSError and the internal timeout
// error, so there may not be much point to that.
func isTemporary(err error) bool {
	if e, ok := err.(*net.DNSError); ok {
		if e.Temporary() || e.Err == serverrstr {
			return true
		}
	}
	return false
}

// See http://en.wikipedia.org/wiki/Private_network#Private_IPv4_address_spaces
// TODO: Maybe we should exclude link-local addresses too?
var _, net10, _ = net.ParseCIDR("10.0.0.0/8")
var _, net172, _ = net.ParseCIDR("172.16.0.0/12")
var _, net192, _ = net.ParseCIDR("192.168.0.0/16")
var _, ipv6private, _ = net.ParseCIDR("FC00::/7")
var _, ipv6siteloc, _ = net.ParseCIDR("FEC0::/10")

// checkIP checks an IP to see if it is a valid mail delivery target.
// A valid mail delivery target must have at least one IP address and
// all of its IP addresses must be global unicast IP addresses (not
// localhost IPs, not multicast, etc).
func checkIP(domain string) (dnsResult, error) {
	addrs, err := net.LookupIP(domain)
	if err != nil && isTemporary(err) {
		return dnsTempfail, err
	}
	if err != nil {
		return dnsBad, err
	}
	if len(addrs) == 0 {
		return dnsBad, fmt.Errorf("%s: no IPs", domain)
	}
	// We disqualify any name that has an IP address that is not a global
	// unicast address.
	for _, i := range addrs {
		if !i.IsGlobalUnicast() {
			return dnsBad, fmt.Errorf("host %s IP %s not global unicast", domain, i)
		}
		// Disallow RFC1918 address space too.
		if net10.Contains(i) || net172.Contains(i) || net192.Contains(i) || ipv6private.Contains(i) || ipv6siteloc.Contains(i) {
			return dnsBad, fmt.Errorf("host %s IP %s is in bad address space", domain, i)
		}
	}
	return dnsGood, nil
}

// ValidDomain returns whether or not the domain or host name exists
// in DNS as a valid target for mail. We use a gory hack to try to
// determine if any error was a temporary failure, in which case we
// return an indicator of this.
//
// The presence of any MX entry of '.' or 'localhost.' is taken as an
// indicator that this domain is not a valid mail delivery
// target. This is regardless of what other MX entries there may
// be. Similarly, a host with any IP addresses that are not valid
// global unicast addresses is disqualified even if it has other valid
// IP addresses.
//
// Note: RFC1918 addresses et al are not considered 'global' addresses
// by us. This may be arguable.
func ValidDomain(domain string) (dnsResult, error) {
	mxs, err := net.LookupMX(domain + ".")
	if err != nil && isTemporary(err) {
		return dnsTempfail, fmt.Errorf("MX tempfail: %s", err)
	}
	// No MX entry? Fall back to A record lookup.
	if err != nil {
		return checkIP(domain + ".")
	}

	// Check MX entries to see if they are valid. The whole thing is
	// valid the moment any one of them is; however, we can't short
	// circuit the check because we want to continue to check for
	// '.' et al in all MXes, even high preference ones.
	var verr error
	valid := dnsUndef // we start with no DNS results at all.
	// We assume that there is at least one MX entry since LookupMX()
	// returned without error. This may be a bad idea but we'll see.
	for _, m := range mxs {
		// Explicitly check for an RFC 7505 null MX. We opt to check
		// for the preference being 0.
		if m.Host == "." && m.Pref == 0 {
			return dnsBad, fmt.Errorf("%s: RFC 7505 null MX", domain)
		}

		lc := strings.ToLower(m.Host)
		// Any MX entry of '.' or 'localhost.' means that this is
		// not a valid target; they've said 'do not send us email'.
		// *ANY* MX entry set this way will disqualify a host.
		// We will get here for an MX to '.' if it doesn't have a
		// 0 preference, which in general makes the preference worth
		// reporting.
		if lc == "." || lc == "localhost." {
			return dnsBad, fmt.Errorf("rejecting bogus MX %d %s", m.Pref, m.Host)
		}
		// TODO: immediately fail anyone who MXs to an IP address?

		v, err := checkIP(m.Host)
		// Replace worse results with better results as we get
		// them; dnsGod > dnsTempfail > dnsBad. With the
		// better results, we also save the error. This means
		// that the error set is the error for the first host
		// with our best result, if there are eg multiple bad
		// MX entries.
		if v > valid {
			valid = v
			verr = err
		}
	}
	return valid, verr
}
