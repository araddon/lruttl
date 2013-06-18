// Copyright 2012, Google Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The implementation borrows heavily from SmallLRUCache (originally by Nathan
// Schrenk). The object maintains a doubly-linked list of elements in the
// When an element is accessed it is promoted to the head of the list, and when
// space is needed the element at the tail of the list (the least recently used
// element) is evicted.
package cache

import (
	"container/list"
	"fmt"
	"sync"
	"time"
)

type LRUCache struct {
	mu sync.Mutex

	// is this a ttl based evict lru?
	timed bool

	// time we last checked ttl expirations
	last_ttl_check time.Time

	EvictCallback func(string, Value)

	// list & table of *entry objects
	list  *list.List
	table map[string]*list.Element

	// Our current size, in bytes. Obviously a gross simplification and low-grade
	// approximation.
	size uint64

	// How many bytes we are limiting the cache to.
	capacity uint64
}

// Values that go into LRUCache need to satisfy this interface.
type Value interface {
	Size() int
}

type Item struct {
	Key   string
	Value Value
}

type entry struct {
	key           string
	value         Value
	size          int
	time_accessed time.Time
}

func NewLRUCache(capacity uint64) *LRUCache {
	return &LRUCache{
		list:     list.New(),
		table:    make(map[string]*list.Element),
		capacity: capacity,
	}
}

// create a new Lru, which evicts units after a time period of non use
//
//    @ttl is time an entry exists before it is evicted
//    @checkEvery is duration to check every x
//
//    l := NewTimeEvictLru(10000, 200*time.Millisecond, 50*time.Millisecond)
//    var evictCt int = 0
//    l.EvictCallback = func(key string, v Value) {
//    	evictCt++
//    }
func NewTimeEvictLru(capacity uint64, ttl, checkEvery time.Duration) *LRUCache {

	lru := LRUCache{
		timed:          true,
		last_ttl_check: time.Now(),
		list:           list.New(),
		table:          make(map[string]*list.Element),
		capacity:       capacity,
	}

	timer := time.NewTicker(checkEvery)
	go func() {
		for {
			select {
			case now := <-timer.C:
				TimeEvict(&lru, now, ttl)
				// case _ = <-done:
				// 	return
			}
		}
	}()

	return &lru
}

// this time eviction is called by a scheduler for background, periodic evictions
func TimeEvict(lru *LRUCache, now time.Time, ttl time.Duration) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	if lru.list.Len() < 1 {
		return
	}

	var n *entry
	var el *list.Element
	hasEvictCallback := lru.EvictCallback != nil

	el = lru.list.Back()

	for {
		if el == nil {
			break
		}
		n = el.Value.(*entry)
		if now.Sub(n.time_accessed) > ttl {
			// the Difference is greater than max TTL so we need to evict this entry
			// first grab the next entry (as this is about to dissappear)
			el = el.Prev()
			lru.remove(n.key)
			if hasEvictCallback {
				lru.EvictCallback(n.key, n.value)
			}
		} else {
			// since they are stored in time order, as soon as we find
			// first item newer than ttl window, we are safe to bail
			break
		}
	}
	lru.last_ttl_check = time.Now()
}

func (lru *LRUCache) Get(key string) (v Value, ok bool) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	element := lru.table[key]
	if element == nil {
		return nil, false
	}
	lru.moveToFront(element)
	return element.Value.(*entry).value, true
}

func (lru *LRUCache) Set(key string, value Value) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	if element := lru.table[key]; element != nil {
		lru.updateInplace(element, value)
	} else {
		lru.addNew(key, value)
	}
}

func (lru *LRUCache) SetIfAbsent(key string, value Value) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	if element := lru.table[key]; element != nil {
		lru.moveToFront(element)
	} else {
		lru.addNew(key, value)
	}
}

func (lru *LRUCache) remove(key string) bool {
	element := lru.table[key]
	if element == nil {
		return false
	}

	lru.list.Remove(element)
	delete(lru.table, key)
	lru.size -= uint64(element.Value.(*entry).size)
	return true
}

func (lru *LRUCache) Delete(key string) bool {
	lru.mu.Lock()
	defer lru.mu.Unlock()
	return lru.remove(key)
}

func (lru *LRUCache) Clear() {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	lru.list.Init()
	lru.table = make(map[string]*list.Element)
	lru.size = 0
}

func (lru *LRUCache) SetCapacity(capacity uint64) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	lru.capacity = capacity
	lru.checkCapacity()
}

func (lru *LRUCache) Stats() (length, size, capacity uint64, oldest time.Time) {
	lru.mu.Lock()
	defer lru.mu.Unlock()
	if lastElem := lru.list.Back(); lastElem != nil {
		oldest = lastElem.Value.(*entry).time_accessed
	}
	return uint64(lru.list.Len()), lru.size, lru.capacity, oldest
}

func (lru *LRUCache) StatsJSON() string {
	if lru == nil {
		return "{}"
	}
	l, s, c, o := lru.Stats()
	return fmt.Sprintf("{\"Length\": %v, \"Size\": %v, \"Capacity\": %v, \"OldestAccess\": \"%v\"}", l, s, c, o)
}

func (lru *LRUCache) Keys() []string {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	keys := make([]string, 0, lru.list.Len())
	for e := lru.list.Front(); e != nil; e = e.Next() {
		keys = append(keys, e.Value.(*entry).key)
	}
	return keys
}

func (lru *LRUCache) Len() int {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	return lru.list.Len()
}

func (lru *LRUCache) Items() []Item {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	items := make([]Item, 0, lru.list.Len())
	for e := lru.list.Front(); e != nil; e = e.Next() {
		v := e.Value.(*entry)
		items = append(items, Item{Key: v.key, Value: v.value})
	}
	return items
}

func (lru *LRUCache) updateInplace(element *list.Element, value Value) {
	valueSize := value.Size()
	sizeDiff := valueSize - element.Value.(*entry).size
	element.Value.(*entry).value = value
	element.Value.(*entry).size = valueSize
	lru.size += uint64(sizeDiff)
	lru.moveToFront(element)
	lru.checkCapacity()
}

func (lru *LRUCache) moveToFront(element *list.Element) {
	lru.list.MoveToFront(element)
	element.Value.(*entry).time_accessed = time.Now()
}

func (lru *LRUCache) addNew(key string, value Value) {
	newEntry := &entry{key, value, value.Size(), time.Now()}
	element := lru.list.PushFront(newEntry)
	lru.table[key] = element
	lru.size += uint64(newEntry.size)
	lru.checkCapacity()
}

func (lru *LRUCache) checkCapacity() {
	// Partially duplicated from Delete
	for lru.size > lru.capacity {
		delElem := lru.list.Back()
		delValue := delElem.Value.(*entry)
		lru.list.Remove(delElem)
		delete(lru.table, delValue.key)
		lru.size -= uint64(delValue.size)
	}
}
