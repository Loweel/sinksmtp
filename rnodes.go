//

package main

// Rule nodes and rule evaluation and so on.

import (
	"fmt"
	"sort"
	"strings"
)

// Phase is the SMTP conversation phase
type Phase int

const (
	// Note: pAny is and must be the zero value.
	pAny Phase = iota
	pConnect
	pHelo
	pMfrom
	pRto
	pData
	pMessage
)

var pMap = map[Phase]string{
	pAny: "@any", pConnect: "@connect",
	pHelo: "@helo", pMfrom: "@from",
	pRto: "@to", pData: "@data", pMessage: "@message",
}

func (p Phase) String() string {
	return pMap[p]
}

// Action is the action to take in response to a successful rule
// match.
type Action int

// Actions are in order from weakest (accept) to strongest (reject)
const (
	aError Action = iota
	aNoresult
	aAccept
	aStall
	aReject
)

var aMap = map[Action]string{
	aError: "ERROR", aAccept: "accept", aReject: "reject", aStall: "stall",
	aNoresult: "set-with",
}

func (a Action) String() string {
	return aMap[a]
}

// Option is bitmaps of all options for from-has/to-has, helo-has, and dns
// all merged into one type for convenience and my sanity.
type Option uint64

const (
	oZero Option = iota

	// EHLO/HELO options
	oHelo Option = 1 << iota
	oEhlo
	oNone
	oBogus
	oNodots
	oBareip
	oProperip
	oMyip
	oRemip
	oOtherip

	// DNS options
	oNodns
	oInconsist
	oNofwd
	oGood
	oExists

	// address options
	oUnqualified
	oRoute
	oQuoted
	oNoat
	oGarbage
	oDomainValid
	oDomainInvalid
	oDomainTempfail

	// dbl options; oEhlo is already above.
	oHost
	oFrom

	// merged bitmaps
	oBad = oUnqualified | oRoute | oNoat | oGarbage
	oIp  = oBareip | oProperip
	oAny = oHost | oEhlo | oFrom
)

// Result is the result of evaluating a rule expression. Currently it
// is either true or false; in the future it may also include 'Defer'.
type Result bool

// RClause represents a single rule clause and its with options
type RClause struct {
	expr  Expr
	withs map[string]string
}

// Rule represents a single rule, bundling together various information
// about what it needs and results in with the expression it evaluates.
type Rule struct {
	clauses  []*RClause
	result   Action
	requires Phase // Rule requires data from this phase; at most pRto now
	deferto  Phase // Rule wants to be deferred to this phase

	// The rule is that if deferto is set it is always equal to or
	// larger than requires. We don't allow '@from accept to ...'
	// or similar gimmicks; it's explicitly an error in the
	// parser.
}

func newRClause() *RClause {
	r := &RClause{withs: make(map[string]string)}
	return r
}

// check() checks a rule to see if it matches. If it does, the context
// is updated appropriately. We check each clause in turn; if one
// matches, we update c.withprops and return true.
func (r *Rule) check(c *Context) Result {
	c.rulemiss = false
	for i := range r.clauses {
		res := r.clauses[i].expr.Eval(c)
		if c.rulemiss {
			// the results on a rulemiss don't matter, since
			// we're skipping this rule anyways.
			return false
		}
		if !res {
			continue
		}
		for k, v := range r.clauses[i].withs {
			c.withprops[k] = v
		}
		return res
	}
	return false
}

func (r *Rule) addclause(rc *RClause) {
	r.clauses = append(r.clauses, rc)
}

// String() returns the string version of a rule clause.
// BUG: we don't properly quote strings that need it (ie that contain
// an embedded ").
func (rc *RClause) String() string {
	var with string
	var keys []string
	for k := range rc.withs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if rc.withs[k] != "" {
			with += fmt.Sprintf(" %s \"%s\"", k, rc.withs[k])
		} else {
			with += fmt.Sprintf(" %s", k)
		}
	}
	if with != "" {
		with = " with" + with
	}
	return fmt.Sprintf("%s%s", rc.expr.String(), with)
}

// String() returns the string form of a Rule. This is theoretically
// a parseable version of the canonical form of the rule.
//
func (r *Rule) String() string {
	var cstrs []string
	for _, rc := range r.clauses {
		cstrs = append(cstrs, rc.String())
	}
	if r.deferto != pAny {
		return fmt.Sprintf("%v %v %s", r.deferto, r.result, strings.Join(cstrs, "; "))
	} else {
		return fmt.Sprintf("%v %s", r.result, strings.Join(cstrs, "; "))
	}
}

// Expr is an expression node, aka an AST node. Expr nodes may be
// structural (eg and and or nodes) or terminal nodes (matchers).
type Expr interface {
	Eval(c *Context) Result
	String() string
}

// Structural nodes

// AndL is our normal running 'thing1 thing2 ...'
type AndL struct {
	nodes []Expr
}

func (a *AndL) Eval(c *Context) (r Result) {
	for i := range a.nodes {
		r = a.nodes[i].Eval(c)
		if !r {
			return r
		}
	}
	return true
}
func (a *AndL) String() string {
	var l []string
	for i := range a.nodes {
		l = append(l, a.nodes[i].String())
	}
	return fmt.Sprintf("( %s )", strings.Join(l, " "))
}

// NotN is not <thing>
type NotN struct {
	node Expr
}

func (n *NotN) Eval(c *Context) (r Result) {
	return !n.node.Eval(c)
}
func (n *NotN) String() string {
	return "not " + n.node.String()
}

// OrN is thing1 or thing2
type OrN struct {
	left, right Expr
}

func (o *OrN) String() string {
	return fmt.Sprintf("( %s or %s )", o.left.String(), o.right.String())
}
func (o *OrN) Eval(c *Context) (r Result) {
	r = o.left.Eval(c)
	if r {
		return r
	}
	return o.right.Eval(c)
}

//
// ---
// Terminal nodes that match things.
//

// AllN is all; it always matches
type AllN struct{}

func (a *AllN) String() string {
	return "all"
}
func (a *AllN) Eval(c *Context) (r Result) {
	return true
}

// TlsN is true if TLS is on. It is 'tls on|off'.
type TlsN struct {
	on bool
}

func (t *TlsN) String() string {
	if t.on {
		return "tls on"
	} else {
		return "tls off"
	}
}
func (t *TlsN) Eval(c *Context) (r Result) {
	return t.on == c.trans.tlson
}

// DNSblN is the matcher for DNS blocklist nodes.
type DNSblN struct {
	domain string
}

func (d *DNSblN) String() string {
	return "dnsbl " + d.domain
}

func (d *DNSblN) Eval(c *Context) (r Result) {
	if c.trans.rip == "" {
		return false
	}
	s := strings.Split(c.trans.rip, ".")
	// We currently only work on IPv4 addresses.
	if len(s) != 4 {
		return false
	}
	ln := fmt.Sprintf("%s.%s.%s.%s.%s", s[3], s[2], s[1], s[0], d.domain)

	res := c.getDnsblRes(ln)
	if res {
		c.addDnsblHit(d.domain)
	}
	return res
}

// MatchN is a general matcher for from/to/helo/host. All of these have
// a common pattern: they take an argument that may be a filename or a
// pattern and they do either address or host matching of some data source
// against it. Because 'host' matches against all verified host names,
// they all do list-matching; from/to/helo simply wrap up their single
// piece of data in a list.
type MatchN struct {
	what, arg string
	// match a literal against a pattern. Either matchHost or matchAddress
	matcher func(string, string) bool
	// get an array of strings of literals to match against.
	// from and helo have one-element arrays.
	getter func(*Context) []string
}

func (m *MatchN) String() string {
	return fmt.Sprintf("%s %s", m.what, m.arg)
}

func (m *MatchN) Eval(c *Context) Result {
	plist := c.getMatchList(m.arg)
	if len(plist) == 0 {
		c.rulemiss = true
		return false
		// we might as well return here, we're not matching.
	}
	for _, p := range plist {
		for _, e := range m.getter(c) {
			if m.matcher(e, p) {
				return true
			}
		}
	}
	return false
}

func newHeloNode(arg string) Expr {
	return &MatchN{what: "helo", arg: arg, matcher: matchHost,
		getter: func(c *Context) []string {
			return []string{c.heloname}
		},
	}
}

func newHostNode(arg string) Expr {
	return &MatchN{what: "host", arg: arg, matcher: matchHost,
		getter: func(c *Context) []string {
			return c.trans.rdns.verified
		},
	}
}

func newFromNode(arg string) Expr {
	return &MatchN{what: "from", arg: arg, matcher: matchAddress,
		getter: func(c *Context) []string {
			return []string{c.from}
		},
	}
}

func newToNode(arg string) Expr {
	return &MatchN{what: "to", arg: arg, matcher: matchAddress,
		getter: func(c *Context) []string {
			return []string{c.rcptto}
		},
	}
}

func newIPNode(arg string) Expr {
	return &MatchN{what: "ip", arg: arg, matcher: matchIp,
		getter: func(c *Context) []string {
			return []string{c.trans.rip}
		},
	}
}

// A Source matches host arg, ehlo arg, or from @<arg>.
// We do so by literally storing nodes internally. We could do this as
// a literal Or node, but we prefer slightly more structure here.
type matchSource struct {
	arg              string
	host, ehlo, from Expr
}

func (m *matchSource) String() string {
	return fmt.Sprintf("source %s", m.arg)
}
func (m *matchSource) Eval(c *Context) Result {
	return m.host.Eval(c) || m.ehlo.Eval(c) || m.from.Eval(c)
}

// Match the host(name) of the domain of (from) addresses. We glue
// '@' on front of the host to match against and call matchAddress().
// This is kind of inefficient but that's how it goes.
func matchFromHost(addr string, host string) bool {
	return matchAddress(addr, "@"+host)
}

func newSourceNode(arg string) Expr {
	return &matchSource{
		arg:  arg,
		host: newHostNode(arg),
		ehlo: newHeloNode(arg),
		// We must use our own matcher in order to preserve the
		// ability to do 'source /some/file', because in that case
		// we can't just glue a '@' on front of the arg here and be
		// done.
		from: &MatchN{what: "source_from", arg: arg,
			matcher: matchFromHost,
			getter: func(c *Context) []string {
				return []string{c.from}
			},
		},
	}
}

// ------

// DblNode is the matcher for DNS domain blocklist lookup. Like
// regular DNSBl nodes it has the DBL domain, but it also has where to
// get the domain(s) to check.
type DblNode struct {
	domain string
	src    Option
}

func (d *DblNode) String() string {
	return fmt.Sprintf("dbl %s %s", d.src, d.domain)
}

func (d *DblNode) Eval(c *Context) (r Result) {
	// track names to check in a map, so we can suppress duplicates
	// (which might be common in some circumstances, eg 'dbl any ...')
	check := make(map[string]struct{})
	if (d.src&(oEhlo|oHelo)) != 0 && c.heloname != "" {
		check[c.heloname] = struct{}{}
	}
	if (d.src & oHost) == oHost {
		// DNS names have a trailing '.', which we must remove.
		for _, s := range c.trans.rdns.verified {
			check[s[:len(s)-1]] = struct{}{}
		}
		for _, s := range c.trans.rdns.nofwd {
			check[s[:len(s)-1]] = struct{}{}
		}
		for _, s := range c.trans.rdns.inconsist {
			check[s[:len(s)-1]] = struct{}{}
		}
	}
	if (d.src&oFrom) == oFrom && c.from != "" {
		aopt := getAddrOpts(c.from, c)
		if (aopt & (oDomainValid | oDomainInvalid | oDomainTempfail)) != 0 {
			idx := strings.IndexByte(c.from, '@')
			check[c.from[idx+1:]] = struct{}{}
		}
	}
	// We might have nothing to check if we don't have certain
	// information, eg we've been asked to check the hostname
	// and there is none.
	if len(check) == 0 {
		return false
	}

	// If we have multiple domains (possible from multiple sources)
	// we check all of them even if one hits. Since check is a map,
	// we will see domains in a random order. We don't care about
	// this since we check them all.
	ret := Result(false)
	for s := range check {
		ln := fmt.Sprintf("%s.%s", s, d.domain)
		res := c.getDnsblRes(ln)
		if res {
			c.addDnsblHit(d.domain)
			ret = Result(true)
		}
	}
	return ret
}

func newDblNode(typ Option, arg string) Expr {
	return &DblNode{domain: arg, src: typ}
}

// ------

// OptionN is the general matcher for options.
// Options have getter functions that interrogate the context to determine
// what is the case. Those live in rules.go.
type OptionN struct {
	what   string
	opts   Option
	getter func(*Context) Option
}

func (t *OptionN) Eval(c *Context) (r Result) {
	opt := t.getter(c)
	return t.opts&opt > 0
}
func (opts Option) String() string {
	var l []string
	if (opts & oBad) == oBad {
		l = append(l, "bad")
		opts = opts - oBad
	}
	if (opts & oIp) == oIp {
		l = append(l, "ip")
		opts = opts - oIp
	}
	if (opts & oAny) == oAny {
		l = append(l, "any")
		opts = opts - oAny
	}
	for k, v := range revMap {
		if (k & opts) == k {
			l = append(l, v)
		}
	}
	// remember, Go map traversal order is deliberately unpredictable
	// we have to make it predictable to have something we can round
	// trip.
	sort.Strings(l)
	return strings.Join(l, ",")
}

func (t *OptionN) String() string {
	return fmt.Sprintf("%s %v", t.what, t.opts)
}

// GORY HACK. Construct inverse opts mapping through magic knowledge
// of both the lexer and the parser. We're all very friendly here,
// right?
func optsReverse() map[Option]string {
	rev := make(map[Option]string)
	revi := make(map[itemType]string)
	for s, i := range keywords {
		revi[i] = s
	}
	for _, m := range mapMap {
		for k, v := range m {
			rev[v] = revi[k]
		}
	}
	// very special hack, required because the dblMap maps two things
	// to the same value.
	rev[oHelo] = "helo"
	rev[oEhlo] = "ehlo"
	return rev
}

var revMap = optsReverse()

// -- create them.
func newDnsOpt(o Option) Expr {
	return &OptionN{what: "dns", opts: o, getter: dnsGetter}
}

func newHeloOpt(o Option) Expr {
	return &OptionN{what: "helo-has", opts: o, getter: heloGetter}
}

func getFromOpts(c *Context) Option {
	return getAddrOpts(c.from, c)
}
func newFromHasOpt(o Option) Expr {
	return &OptionN{what: "from-has", opts: o, getter: getFromOpts}
}

func getToOpts(c *Context) Option {
	return getAddrOpts(c.rcptto, c)
}
func newToHasOpt(o Option) Expr {
	return &OptionN{what: "to-has", opts: o, getter: getToOpts}
}
