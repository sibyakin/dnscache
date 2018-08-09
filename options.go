package dnscache

import (
	"fmt"
	"time"

	"github.com/korovkin/limiter"
	"github.com/miekg/dns"
)

type ResolverOption func(*Resolver)

// WithUpstream sets upstream server to forward requests to.
//
// Default is 127.0.0.11 (docker internal resolver).
func WithUpstream(addr string, port int) ResolverOption {
	return func(r *Resolver) {
		r.upstream = fmt.Sprintf("%s:%d", addr, port)
	}
}

// WithDialTimeout sets timeout for net.Dial used to forward
// queries over TCP.
//
// Default is 5s.
func WithDialTimeout(timeout time.Duration) ResolverOption {
	return func(r *Resolver) {
		if int64(timeout) > 0 {
			r.dialTimeout = timeout
		}
	}
}

// WithCacheUpdateInterval sets the delay before next cache update.
// e.g with this set to 30s the cache will be updated every 30 seconds.
// Default is 15s.
func WithCacheUpdateInterval(interval time.Duration) ResolverOption {
	return func(r *Resolver) {
		if int64(interval) > 0 {
			r.cacheUpdateInterval = interval
		}
	}
}

// WithCacheTTL sets time after which whole cache entry will be deleted.
//
// Default is 30m.
func WithCacheTTL(ttl time.Duration) ResolverOption {
	return func(r *Resolver) {
		if int64(ttl) > 0 {
			r.cacheEntryTTL = ttl
		}
	}
}

// WithConcurrencyLimiter allows you to set maximum number of concurrent queries.
//
// This also affects cache updates.
func WithConcurrencyLimiter(limit int) ResolverOption {
	return func(r *Resolver) {
		r.limiter = limiter.NewConcurrencyLimiter(limit)
		r.exchangeFunc = func(m *dns.Msg, a string) (*dns.Msg, error) {
			resc := make(chan *dns.Msg, 1)
			errc := make(chan error, 1)

			r.limiter.Execute(func() {
				resp, err := dns.Exchange(m, a)
				resc <- resp
				errc <- err
			})
			resp, err := <-resc, <-errc
			return resp, err
		}
	}
}

// WithCacheTypes sets query types that should be cached.
//
// You can set records using miekg/dns types (dns.TypeA, dns.TypeMX, etc.)
//
// By default only A and AAAA records are cached.
func WithCacheTypes(qtypes ...uint16) ResolverOption {
	return func(r *Resolver) {
		r.cache.cacheTypes = qtypes
	}
}
