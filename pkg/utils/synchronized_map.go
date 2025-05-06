// Copyright 2025 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

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
	m.UnSafeSet(key, value)
	m.Unlock()
}

// Remove removes an element from the map
func (m *SynchronizedMap) Remove(key string) {
	m.Lock()
	m.UnSafeRemove(key)
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
