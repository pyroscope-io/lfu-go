package lfu

import (
	"container/list"
	"sync"
)

type Eviction struct {
	Key   string
	Value interface{}
}

type Cache struct {
	// If len > UpperBound, cache will automatically evict
	// down to LowerBound.  If either value is 0, this behavior
	// is disabled.
	UpperBound       int
	LowerBound       int
	values           map[string]*cacheEntry
	freqs            *list.List
	len              int
	lock             *sync.Mutex
	EvictionChannel  chan<- Eviction
	WriteBackChannel chan<- Eviction
}

type cacheEntry struct {
	key       string
	value     interface{}
	freqNode  *list.Element
	persisted bool
}

type listEntry struct {
	entries map[*cacheEntry]byte
	freq    int
}

func New() *Cache {
	c := new(Cache)
	c.values = make(map[string]*cacheEntry)
	c.freqs = list.New()
	c.lock = new(sync.Mutex)
	return c
}

func (c *Cache) Get(key string) interface{} {
	c.lock.Lock()
	defer c.lock.Unlock()
	if e, ok := c.values[key]; ok {
		c.increment(e)
		return e.value
	}
	return nil
}

func (c *Cache) Set(key string, value interface{}) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if e, ok := c.values[key]; ok {
		// value already exists for key.  overwrite
		e.value = value
		e.persisted = false
		c.increment(e)
	} else {
		// value doesn't exist.  insert
		e = new(cacheEntry)
		e.key = key
		e.value = value
		c.values[key] = e
		c.increment(e)
		c.len++
		// bounds mgmt
		if c.UpperBound > 0 && c.LowerBound > 0 {
			if c.len > c.UpperBound {
				c.evict(c.len - c.LowerBound)
			}
		}
	}
}

func (c *Cache) Delete(key string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if e, ok := c.values[key]; ok {
		c.delete(e)
	}
}

func (c *Cache) delete(entry *cacheEntry) {
	delete(c.values, entry.key)
	c.remEntry(entry.freqNode, entry)
	c.len--
}

func (c *Cache) Len() int {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.len
}

func (c *Cache) Evict(count int) int {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.evict(count)
}

func (c *Cache) WriteBack(count int) int {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.persist(count)
}

func (c *Cache) evict(count int) int {
	// No lock here so it can be called
	// from within the lock (during Set)
	var evicted int
	for i := 0; i < count; {
		if place := c.freqs.Front(); place != nil {
			for entry := range place.Value.(*listEntry).entries {
				if i < count {
					if c.EvictionChannel != nil && !entry.persisted {
						c.EvictionChannel <- Eviction{
							Key:   entry.key,
							Value: entry.value,
						}
					}
					c.delete(entry)
					evicted++
					i++
				}
			}
		}
	}
	return evicted
}

func (c *Cache) persist(count int) int {
	var persisted int
	for i := 0; i < count; {
		if place := c.freqs.Front(); place != nil {
			for entry := range place.Value.(*listEntry).entries {
				if i < count {
					if c.WriteBackChannel != nil && !entry.persisted {
						select {
						default:
						case c.WriteBackChannel <- Eviction{Key: entry.key, Value: entry.value}:
							entry.persisted = true
							persisted++
						}
					}
					i++
				}
			}
		}
	}
	return persisted
}

func (c *Cache) increment(e *cacheEntry) {
	currentPlace := e.freqNode
	var nextFreq int
	var nextPlace *list.Element
	if currentPlace == nil {
		// new entry
		nextFreq = 1
		nextPlace = c.freqs.Front()
	} else {
		// move up
		nextFreq = currentPlace.Value.(*listEntry).freq + 1
		nextPlace = currentPlace.Next()
	}

	if nextPlace == nil || nextPlace.Value.(*listEntry).freq != nextFreq {
		// create a new list entry
		li := new(listEntry)
		li.freq = nextFreq
		li.entries = make(map[*cacheEntry]byte)
		if currentPlace != nil {
			nextPlace = c.freqs.InsertAfter(li, currentPlace)
		} else {
			nextPlace = c.freqs.PushFront(li)
		}
	}
	e.freqNode = nextPlace
	nextPlace.Value.(*listEntry).entries[e] = 1
	if currentPlace != nil {
		// remove from current position
		c.remEntry(currentPlace, e)
	}
}

func (c *Cache) remEntry(place *list.Element, entry *cacheEntry) {
	entries := place.Value.(*listEntry).entries
	delete(entries, entry)
	if len(entries) == 0 {
		c.freqs.Remove(place)
	}
}
