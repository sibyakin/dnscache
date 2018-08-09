// Package dnscache provides a simple caching DNS resolver,
// mainly designed to work with docker internal resolver.
package dnscache

import (
	"fmt"
	"io/ioutil"
	"net"
	"time"

	"strings"

	"github.com/korovkin/limiter"
	"github.com/miekg/dns"
	"github.com/sirupsen/logrus"
)

var (
	DefaultUpstream       = "127.0.0.11:53"
	DefaultTimeout        = time.Second * 5
	DefaultUpdateInterval = time.Second * 15
	DefaultEntryTTL       = time.Second * 1800

	// if entry is newer than 5s, we don't re-resolve it. This isn't configurable atm
	noUpdatePeriod = time.Second * 5
)

type Resolver struct {
	listenAddr   string
	listenPort   int
	upstream     string
	dialTimeout  time.Duration
	udpServer    *dns.Server
	tcpServer    *dns.Server
	stopChan     chan struct{}
	exchangeFunc func(*dns.Msg, string) (*dns.Msg, error)
	limiter      *limiter.ConcurrencyLimiter

	cache               *cache
	cacheUpdateInterval time.Duration
	cacheEntryTTL       time.Duration
}

// NewResolver creates a new resolver with given options
// or with default values for missing ones.
func NewResolver(host string, port int, opts ...ResolverOption) *Resolver {
	r := Resolver{
		listenAddr:  host,
		listenPort:  port,
		upstream:    DefaultUpstream,
		dialTimeout: DefaultTimeout,
		stopChan:    make(chan struct{}, 1),
		cache: &cache{
			cacheTypes: []uint16{dns.TypeA, dns.TypeAAAA},
			data:       map[string]cacheEntry{},
		},
		exchangeFunc:        dns.Exchange,
		cacheUpdateInterval: DefaultUpdateInterval,
		cacheEntryTTL:       DefaultEntryTTL,
	}

	for _, option := range opts {
		option(&r)
	}

	return &r
}

// Start runs tcp/udp listeners and cache updating goroutine.
// Keep in mind that this call doesn't block.
func (r *Resolver) Start() error {

	err := r.startUDP()
	if err != nil {
		return err
	}

	err = r.startTCP()
	if err != nil {
		return err
	}

	t := time.NewTicker(r.cacheUpdateInterval)
	go func() {
		for {
			select {
			case <-t.C:
				r.updateCache()
			case <-r.stopChan:
				t.Stop()
			}
		}
	}()

	return nil
}

// Stop shuts down both TCP and UDP listeners and stops the ticker goroutine.
func (r *Resolver) Stop() {
	r.stopChan <- struct{}{}

	// shutdown also closes connections
	if r.udpServer != nil {
		r.udpServer.Shutdown()
	}

	if r.tcpServer != nil {
		r.tcpServer.Shutdown()
	}
}

// ListenAddress returns address that the resolver is listening on (without port).
func (r *Resolver) ListenAddress() string {
	return r.listenAddr
}

// ServeDNS implements Handler interface of miekg/dns package.
func (r *Resolver) ServeDNS(w dns.ResponseWriter, query *dns.Msg) {
	if query == nil || len(query.Question) == 0 {
		return
	}

	fail := func() {
		resp := new(dns.Msg)
		resp.SetRcode(query, dns.RcodeServerFailure)
		w.WriteMsg(resp)
	}

	name := query.Question[0].Name
	qtype := query.Question[0].Qtype
	qtypeStr := dns.TypeToString[qtype]
	logrus.Debugf("[resolver] resolving '%s', qtype '%s'", name, qtypeStr)
	proto := w.LocalAddr().Network()
	if r.cache.IsCachedType(qtype) {
		if resp, ok := r.responseFromCache(query); ok {
			w.WriteMsg(resp)
			return
		}

		logrus.Debugf("[resolver] cache miss: %s", name)
		resp, err := r.forward(proto, query)
		if err != nil {
			logrus.Warn(err)
			fail()
			return
		}

		w.WriteMsg(resp)
		if resp.Rcode == dns.RcodeSuccess {
			r.cache.Set(name, qtype, resp)
		}
	}

	logrus.Debugf("[resolver] forwarding query to '%s'", r.upstream)
	resp, err := r.forward(proto, query)
	if err != nil {
		logrus.Debugf("[resolver] got an error from upstream:", err)
		fail()
		return
	}

	w.WriteMsg(resp)
}

func (r *Resolver) startUDP() error {
	started := make(chan struct{})
	errc := make(chan error)
	udpAddr := &net.UDPAddr{
		IP:   net.ParseIP(r.listenAddr),
		Port: r.listenPort,
	}

	udpListener, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}

	udpServer := &dns.Server{Handler: r, PacketConn: udpListener}
	udpServer.NotifyStartedFunc = func() {
		started <- struct{}{}
	}
	r.udpServer = udpServer
	go func() {
		errc <- udpServer.ActivateAndServe()
	}()

	select {
	case <-started:
		return nil
	case err := <-errc:
		return err
	}
}

func (r *Resolver) startTCP() error {
	started := make(chan struct{})
	errc := make(chan error)
	tcpAddr := &net.TCPAddr{
		IP:   net.ParseIP(r.listenAddr),
		Port: r.listenPort,
	}

	tcpListener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return err
	}

	tcpServer := &dns.Server{Handler: r, Listener: tcpListener}
	tcpServer.NotifyStartedFunc = func() {
		started <- struct{}{}
	}
	r.udpServer = tcpServer
	go func() {
		errc <- tcpServer.ActivateAndServe()
	}()

	select {
	case <-started:
		return nil
	case err := <-errc:
		return err
	}
}

func (r *Resolver) responseFromCache(query *dns.Msg) (*dns.Msg, bool) {
	name := query.Question[0].Name
	qtype := query.Question[0].Qtype
	logrus.Debugf("[resolver] looking up cache for '%s'", name)

	resp, ok := r.cache.Get(name, qtype)
	if ok {
		resp.SetReply(query)
	}

	return resp, ok
}

func (r *Resolver) updateCache() {
	logrus.Debug("[cache] updating cache")
	entries := r.cache.Entries()
	for _, fqdn := range entries {
		r.updateCacheEntry(fqdn)
	}
}

func (r *Resolver) updateCacheEntry(fqdn string) {
	lastAccess := r.cache.LastAccess(fqdn)
	if time.Since(lastAccess) >= r.cacheEntryTTL {
		logrus.Debugf("[cache] ttl expired, deleting '%s'", fqdn)
		r.cache.DelKey(fqdn)
		return
	}

	lastUpdate := r.cache.LastUpdate(fqdn)
	if time.Since(lastUpdate) <= noUpdatePeriod {
		logrus.Debugf("[cache] entry for '%s' is too fresh, skipping update", fqdn)
		return
	}

	for _, qtype := range r.cache.GetCachedTypes() {
		q := new(dns.Msg)
		q.Id = dns.Id()
		q.RecursionDesired = true
		q.Question = []dns.Question{
			{
				Name:   fqdn,
				Qtype:  qtype,
				Qclass: dns.ClassINET,
			},
		}

		resp, err := r.exchangeFunc(q, r.upstream)
		if err != nil || resp == nil {
			logrus.Debugf("[cache] got an error, deleting entry: err: %s, resp: %v", err, resp)
			r.cache.DelRecord(fqdn, qtype) // just delete the entry as we cannot ensure its validity
			return
		}

		if resp.Rcode != dns.RcodeSuccess {
			if rcodestr, ok := dns.RcodeToString[resp.Rcode]; ok {
				logrus.Debugf("[cache] response rcode != NOERROR, deleting entry: rcode: %s", rcodestr)
			} else {
				logrus.Debugf("[cache] response rcode unknown, deleting entry: rcode: %d", resp.Rcode)
			}
			return
		}

		r.cache.Set(fqdn, qtype, resp)
	}

	logrus.Debugf("[cache] updated %s", fqdn)
}

func (r *Resolver) forward(proto string, msg *dns.Msg) (*dns.Msg, error) {
	switch proto {
	case "udp":
		return r.exchangeFunc(msg, r.upstream)
	case "tcp":
		conn, err := net.DialTimeout(proto, r.upstream, r.dialTimeout)
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		dnsConn := &dns.Conn{
			Conn:    conn,
			UDPSize: dns.DefaultMsgSize,
		}
		defer dnsConn.Close()

		err = dnsConn.WriteMsg(msg)
		if err != nil {
			return nil, err
		}

		return dnsConn.ReadMsg()
	default:
		return nil, fmt.Errorf("[resolver] wrong proto: %s", proto)
	}
}

// ReplaceDockerDNS edits /etc/resolv.conf, replacing lines containing
// nameserver 127.0.0.11 (which is docker internal resolver address) with given address.
// https://github.com/docker/compose/issues/2847
// It has very limited use cases, so I don't recommend to use it.
func ReplaceDockerDNS(addr string) error {
	input, err := ioutil.ReadFile("/etc/resolv.conf")
	if err != nil {
		return err
	}

	lines := strings.Split(string(input), "\n")

	for i, line := range lines {
		if strings.Contains(line, "nameserver 127.0.0.11") {
			lines[i] = "nameserver " + addr
		}
	}

	output := strings.Join(lines, "\n")
	err = ioutil.WriteFile("/etc/resolv.conf", []byte(output), 0644)
	if err != nil {
		return err
	}

	return nil
}
