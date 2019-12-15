// Package memocache provides in memory cache that is safe for concurrent use by
// multiple goroutines. It's meant to be used for expensive to compute or slow
// to fetch values.
//
// There are a few interfaces defined in this package. MapInterface is an
// interface to a map that is safe for concurrent use. A good example
// implementation is sync.Map and this package implements LRUMap. CacheInterface
// provides LoadOrCall function and it saves computation or RPC calls for the
// same keys. Implementation of CacheInterface often use MapInterface.
package memocache

import (
	"container/list"
	"sync"
	"sync/atomic"
)

// Value is a single value that is initialized once by calling the given
// function only once. Value should not be copied after first use.
type Value struct {
	once  sync.Once
	value interface{}
}

// LoadOrCall gets the value. If the value isn't ready it calls getValue to get
// the value.
func (e *Value) LoadOrCall(getValue func() interface{}) interface{} {
	e.once.Do(func() {
		e.value = getValue()
	})

	return e.value
}

// Map is a kind of key value cache map but it is safe for concurrent use by
// multiple goroutines. It can avoid multiple duplicate function calls
// associated with the same key. When the cache is missing, the given function
// is used to compute or fetch the value for the key. Subsequent calls to the
// same key waits until the function returns, but calls to a different key are
// not blocked. Map should not be copied after first use.
//
// Deprecated: Use NewCache(&sync.Map{}).
type Map struct {
	m sync.Map
}

// LoadOrCall gets pre-cached value associated with the given key or calls
// getValue to get the value for the key. The function getValue is called only
// once for the given key. Even if different getValue is given for the same key,
// only one function is called. The key should be hashable.
func (m *Map) LoadOrCall(key interface{}, getValue func() interface{}) interface{} {
	e, _ := m.m.LoadOrStore(key, &Value{})
	return e.(*Value).LoadOrCall(getValue)
}

// Delete deletes the cache value for the key. Prior LoadOrCall() with the same
// key won't be affected by the delete calls. Later LoadOrCall() with the same
// key will have to call getValue, since the cache is cleared for the key. The
// key should be hashable.
func (m *Map) Delete(key interface{}) {
	m.m.Delete(key)
}

// CacheInterface is an interface that provides map interface which is safe to use
// in multiple goroutines.
type CacheInterface interface {
	LoadOrCall(key interface{}, getValue func() interface{}) interface{}
	Delete(key interface{})
}

// MultiLevelMap is an expansion of a Map that can manage tree like structure.
// It's possible to prune a subtree. There shouldn't be any conflicts between a
// subtree and the leaf node. For example, if a path ("a", "b", "c") has a
// value, path ("a", "b") cannot have a value. Each elemnt of path should be
// hashable. MultiLevelMap should not be copied after first use. MultiLevelMap
// uses a single level cache that implements CacheInterface such as *sync.Map as
// a backend. For some cache with replacement policies, cache maps on a
// different levels may need to share some stats like the current size of the
// cache.
type MultiLevelMap struct {
	v      Value
	newMap func() CacheInterface
}

// NewMultiLevelMap returns a new MultiLevelMap with the given newMap factory.
// For LRU cache, you may call:
//
// 	const maxSize = 10000
// 	sharedList := list.New()
// 	m := NewMultiLevelMap(func() memocache.CacheInterface {
// 		return NewCache(NewLRUMap(sharedList, maxSize))
// 	})
//
// For random replacement cache, you may call:
//
// 	const maxSize = 10000
// 	var currentSize int32
// 	m := NewMultiLevelMap(func() memocache.CacheInterface {
// 		return NewRRCache(&currentSize, maxSize, maxSize/2, rand.Intn)
// 	})
func NewMultiLevelMap(newMap func() CacheInterface) *MultiLevelMap {
	return &MultiLevelMap{
		newMap: newMap,
	}
}

// findLeafNode finds a leaf node from the given non-nil root node.
func findLeafNode(root CacheInterface, newMap func() CacheInterface, path ...interface{}) CacheInterface {
	if len(path) == 0 {
		return root
	}

	newRoot := root.LoadOrCall(path[0], func() interface{} {
		return newMap()
	}).(CacheInterface)

	return findLeafNode(newRoot, newMap, path[1:]...)
}

// getRoot returns a root of the tree. If the map multi map is not used before,
// a new root is created in a multi-goroutine-safe way.
func (m *MultiLevelMap) getRoot() CacheInterface {
	return m.v.LoadOrCall(func() interface{} {
		if m.newMap == nil {
			m.newMap = func() CacheInterface {
				return &Map{}
			}
		}
		return m.newMap()
	}).(CacheInterface)
}

// LoadOrCall loads the value in path. If the value doesn't exist, it calls
// getValue only once. All concurrent calls to the same path will block until
// the value is available. Calls to other paths are not blocked. Each path
// element should be hashable.
func (m *MultiLevelMap) LoadOrCall(getValue func() interface{}, path ...interface{}) interface{} {
	n := len(path)
	if n == 0 {
		panic("path was not given")
	}

	root := m.getRoot()
	return findLeafNode(root, m.newMap, path[:n-1]...).LoadOrCall(path[n-1], getValue)
}

// Prune removes a subtree of the path. It may or may not affect other
// LoadOrCall calls made at the same time. But subsequent LoadOrCall calls in
// the same goroutine are affected by the Prune call, so newly updated value
// will be cached again.
func (m *MultiLevelMap) Prune(path ...interface{}) {
	n := len(path)
	if n == 0 {
		panic("pruning the whole tree is not supported yet")
	}

	root := m.getRoot()
	findLeafNode(root, m.newMap, path[:n-1]...).Delete(path[n-1])
}

// MapInterface implements a map safe for concurrent use by multiple goroutines.
// For example, *sync.Map implements MapInterface.
type MapInterface interface {
	LoadOrStore(key, value interface{}) (actual interface{}, loaded bool)
	Delete(key interface{})
}

// Cache is a kind of key value cache map but it is safe for concurrent use by
// multiple goroutines. It can avoid multiple duplicate function calls
// associated with the same key. When the cache is missing, the given function
// is used to compute or fetch the value for the key. Subsequent calls to the
// same key waits until the function returns, but calls to a different key are
// not blocked. Map should not be copied after first use.
type Cache struct {
	m MapInterface
}

// NewCache returns a new cache backed by the given m which should be safe for
// concurrent use by multiple goroutines.
func NewCache(m MapInterface) *Cache {
	return &Cache{m: m}
}

// LoadOrCall gets pre-cached value associated with the given key or calls
// getValue to get the value for the key. The function getValue is called only
// once for the given key. Even if different getValue is given for the same key,
// only one function is called. The key should be hashable.
func (c *Cache) LoadOrCall(key interface{}, getValue func() interface{}) interface{} {
	e, _ := c.m.LoadOrStore(key, &Value{})
	return e.(*Value).LoadOrCall(getValue)
}

// Delete deletes the cache value for the key. Prior LoadOrCall() with the same
// key won't be affected by the delete calls. Later LoadOrCall() with the same
// key will have to call getValue, since the cache is cleared for the key. The
// key should be hashable.
func (c *Cache) Delete(key interface{}) {
	c.m.Delete(key)
}

// RRCache implements the random replacement cache. It removes about a half
// (random) of cached items when it goes over the max size. RRCache has smaller
// memory overhead than LRUCache has.
type RRCache struct {
	m           sync.Map
	currentSize *int32
	maxSize     int32
	targetNum   int32
	intn        func(n int) int
	mu          sync.Mutex // Lock for delete
}

// NewRRCache creates a new random replacement cache. If the maxSize is reached,
// items are evicted approximately until the given targetNum (like half of
// maxSize) items are remaining. The evicted items may not be truly random. A
// pointer to currentSize is used to share the counter for the number of items
// for multi level maps. Pass rand.Intn as intn or any random number generator
// that is safe for concurrent use.
func NewRRCache(currentSize *int32, maxSize, targetNum int32, intn func(n int) int) *RRCache {
	return &RRCache{
		currentSize: currentSize,
		maxSize:     maxSize,
		targetNum:   targetNum,
		intn:        intn,
	}
}

// LoadOrCall loads the value in path. If the value doesn't exist, it calls
// getValue only once. All concurrent calls to the same path will block until
// the value is available. Calls to other paths are not blocked. Each path
// element should be hashable. If the number of items exceeds the maxSize, it
// will evict random items.
func (r *RRCache) LoadOrCall(key interface{}, getValue func() interface{}) interface{} {
	e, _ := r.m.LoadOrStore(key, &Value{})
	return e.(*Value).LoadOrCall(func() interface{} {
		atomic.AddInt32(r.currentSize, 1)
		r.maybeEvict()
		return getValue()
	})
}

// Delete deletes the cache value for the key. Prior LoadOrCall() with the same
// key won't be affected by the delete calls. Later LoadOrCall() with the same
// key will have to call getValue, since the cache is cleared for the key. The
// key should be hashable.
func (r *RRCache) Delete(key interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if value, ok := r.m.Load(key); ok {
		if child, ok := value.(*RRCache); ok {
			child.clear()
		}
		atomic.AddInt32(r.currentSize, -1)
		r.m.Delete(key)
	}
}

func (r *RRCache) clear() {
	r.m.Range(func(key, value interface{}) bool {
		if child, ok := value.(*RRCache); ok {
			child.clear()
		}
		r.Delete(key)
		return true
	})
}

func (r *RRCache) maybeEvict() {
	count := 0
	for atomic.LoadInt32(r.currentSize) > r.maxSize {
		count++
		if count > 5 {
			break
		}
		r.m.Range(func(key, value interface{}) bool {
			if child, ok := value.(*RRCache); ok {
				child.maybeEvict()
			}
			currentSize := atomic.LoadInt32(r.currentSize)
			numToEvict := currentSize - r.targetNum
			randResult := int32(r.intn(int(currentSize)))
			if randResult < numToEvict {
				r.Delete(key)

			}
			return true
		})
	}
}

type keyValue struct {
	M     map[interface{}]*list.Element
	Key   interface{}
	Value interface{}
}

// LRUMap implements the least recently used map with manual deletion. LRUMap
// needs a linked list and has some overhead on the memory space.
type LRUMap struct {
	mu      sync.Mutex
	list    *list.List
	m       map[interface{}]*list.Element
	maxSize int
}

// NewLRUMap returns a new LRU cache.
func NewLRUMap(l *list.List, maxSize int) *LRUMap {
	return &LRUMap{
		list:    l,
		m:       make(map[interface{}]*list.Element),
		maxSize: maxSize,
	}
}

// LoadOrStore returns the existing value for the key if present. Otherwise, it
// stores and returns the given value. The loaded result is true if the value
// was loaded, false if stored. If the cache size exceeds the maxSize, it
// removes the value from the map.
func (l *LRUMap) LoadOrStore(key, value interface{}) (actual interface{}, loaded bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[key]
	if ok {
		l.list.MoveToFront(e)
		return e.Value.(*keyValue).Value, true
	}
	e = l.list.PushFront(&keyValue{M: l.m, Key: key, Value: value})
	l.m[key] = e
	for l.list.Len() > l.maxSize {
		oldest := l.list.Back()
		kv := oldest.Value.(*keyValue)
		delete(kv.M, kv.Key)
		l.list.Remove(oldest)
	}
	return e.Value.(*keyValue).Value, false
}

// clear removes all values in this LRUMap.
func (l *LRUMap) clear() {
	for k := range l.m {
		l.Delete(k)
	}
}

// Delete deletes the value for a key.
func (l *LRUMap) Delete(key interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[key]
	if !ok {
		return
	}
	if ll, ok := e.Value.(*keyValue).Value.(*LRUMap); ok {
		ll.clear()
	}
	l.list.Remove(e)
	delete(l.m, key)
}
