package utils

import (
	"sync"
)

// SynchronizedMap a thread safe string to anything map
type SynchronizedMap struct {
	Items        map[string]interface{}
	sync.RWMutex // Read Write mutex, guards access to internal map.
}

// NewSynchronizedMap creates a new synchronized map
func NewSynchronizedMap() *SynchronizedMap {
	return &SynchronizedMap{Items: make(map[string]interface{})}
}

// Get retrieves an element from map under given key
func (m *SynchronizedMap) Get(key string) (interface{}, bool) {
	m.RLock()
	value, ok := m.Items[key]
	m.RUnlock()
	return value, ok
}

// Set sets the given value under the specified key
func (m *SynchronizedMap) Set(key string, value interface{}) {
	m.Lock()
	m.Items[key] = value
	m.Unlock()
}

// Remove removes an element from the map
func (m *SynchronizedMap) Remove(key string) {
	m.Lock()
	delete(m.Items, key)
	m.Unlock()
}

// UnSafeRemove removes an element from the map without lock
func (m *SynchronizedMap) UnSafeRemove(key string) {
	delete(m.Items, key)
}

// UnSafeSet sets the given value under the specified key without lock
func (m *SynchronizedMap) UnSafeSet(key string, value interface{}) {
	m.Items[key] = value
}
