package dnscache

import (
	"sync"
	"time"

	"github.com/miekg/dns"
)

type cacheEntry struct {
	Records  map[uint16]*dns.Msg //uint16 is record type (A, AAAA, etc.)
	Updated  time.Time
	Accessed time.Time
}

type cache struct {
	lock       sync.RWMutex
	cacheTypes []uint16
	data       map[string]cacheEntry
}

func (c *cache) Get(key string, qtype uint16) (*dns.Msg, bool) {
	c.lock.RLock()
	entry, ok := c.data[key]
	c.lock.RUnlock()

	defer func() {
		if ok {
			c.lock.Lock()
			entry.Accessed = time.Now()
			c.data[key] = entry
			c.lock.Unlock()
		}
	}()

	records, ok := entry.Records[qtype]
	return records, ok
}

func (c *cache) Set(key string, qtype uint16, value *dns.Msg) {
	c.lock.Lock()
	defer c.lock.Unlock()

	var accessed time.Time
	var records map[uint16]*dns.Msg
	now := time.Now()
	if entry, ok := c.data[key]; ok {
		accessed = entry.Accessed
		records = entry.Records
	} else {
		accessed = now
		records = map[uint16]*dns.Msg{}
	}

	records[qtype] = value
	c.data[key] = cacheEntry{
		Records:  records,
		Updated:  now,
		Accessed: accessed,
	}
}

func (c *cache) DelKey(key string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	delete(c.data, key)
}

func (c *cache) DelRecord(key string, qtype uint16) {
	c.lock.Lock()
	defer c.lock.Unlock()

	delete(c.data[key].Records, qtype)
}

func (c *cache) Entries() []string {
	c.lock.RLock()
	defer c.lock.RUnlock()

	var result []string
	for fqdn, _ := range c.data {
		result = append(result, fqdn)
	}

	return result
}

func (c *cache) LastAccess(key string) time.Time {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.data[key].Accessed
}

func (c *cache) LastUpdate(key string) time.Time {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.data[key].Updated
}

func (c *cache) GetCachedTypes() []uint16 {
	return c.cacheTypes
}

func (c *cache) IsCachedType(qtype uint16) bool {
	for _, cachedType := range c.cacheTypes {
		if cachedType == qtype {
			return true
		}
	}

	return false
}
