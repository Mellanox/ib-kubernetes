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

package guid

import (
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
)

type Pool interface {
	// AllocateGUID reserves the given guid with its pkey.
	// It returns an error if the guid is invalid, out of range, or already reserved.
	AllocateGUID(string, string) error

	// GenerateGUID reserves and returns the next free GUID.
	// pkey is stored with the reservation so another caller cannot receive it.
	GenerateGUID(pkey string) (GUID, error)

	// ReleaseGUID release the reservation of the guid.
	// It returns error if the guid is not in the range.
	ReleaseGUID(string) error

	// Reset clears the current pool and resets it with given values (may be empty)
	Reset(guids map[string]string) error

	Get(string) (string, error)
}

var ErrGUIDPoolExhausted = errors.New("GUID pool is exhausted")

type guidPool struct {
	mu          sync.Mutex
	rangeStart  GUID            // first guid in range
	rangeEnd    GUID            // last guid in range
	currentGUID GUID            // next scan cursor
	guidPoolMap map[GUID]string // allocated guid map and pkey
}

func NewPool(conf *config.GUIDPoolConfig) (Pool, error) {
	log.Info().Msgf("creating guid pool, guidRangeStart %s, guidRangeEnd %s", conf.RangeStart, conf.RangeEnd)
	rangeStart, err := ParseGUID(conf.RangeStart)
	if err != nil {
		return nil, fmt.Errorf("failed to parse guidRangeStart %v", err)
	}
	rangeEnd, err := ParseGUID(conf.RangeEnd)
	if err != nil {
		return nil, fmt.Errorf("failed to parse guidRangeStart %v", err)
	}
	if !isValidRange(rangeStart, rangeEnd) {
		return nil, fmt.Errorf("invalid guid range. rangeStart: %v rangeEnd: %v", rangeStart, rangeEnd)
	}

	return &guidPool{
		rangeStart:  rangeStart,
		rangeEnd:    rangeEnd,
		currentGUID: rangeStart,
		guidPoolMap: map[GUID]string{},
	}, nil
}

// Reset clears the current pool and resets it with given values (may be empty).
// Out of range GUIDs are skipped.
func (p *guidPool) Reset(guids map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	log.Debug().Msg("resetting guid pool")

	p.guidPoolMap = map[GUID]string{}
	for guid, pkey := range guids {
		guidAddr, err := ParseGUID(guid)
		if err != nil {
			return err
		}
		if p.isGUIDInRange(guidAddr) {
			p.guidPoolMap[guidAddr] = pkey
		}
	}
	return nil
}

// GenerateGUID reserves and returns the next free GUID in the pool.
func (p *guidPool) GenerateGUID(pkey string) (GUID, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	next, ok := p.firstFree(p.currentGUID, p.rangeEnd)
	if !ok {
		next, ok = p.firstFree(p.rangeStart, p.rangeEnd)
	}
	if !ok {
		return 0, ErrGUIDPoolExhausted
	}

	p.guidPoolMap[next] = pkey
	p.currentGUID = next + 1
	return next, nil
}

// ReleaseGUID release allocated guid
func (p *guidPool) ReleaseGUID(guid string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	log.Debug().Msgf("releasing guid %s", guid)
	guidAddr, err := ParseGUID(guid)
	if err != nil {
		return err
	}

	if _, ok := p.guidPoolMap[guidAddr]; !ok {
		return fmt.Errorf("failed to release guid %s, not allocated ", guid)
	}
	delete(p.guidPoolMap, guidAddr)
	return nil
}

func (p *guidPool) Get(guid string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	guidAddr, err := ParseGUID(guid)
	if err != nil {
		return "", err
	}
	return p.guidPoolMap[guidAddr], nil
}

func (p *guidPool) AllocateGUID(guid string, pkey string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	log.Debug().Msgf("allocating guid %s", guid)

	guidAddr, err := ParseGUID(guid)
	if err != nil {
		return err
	}

	if !p.isGUIDInRange(guidAddr) {
		return fmt.Errorf("out of range guid %s, pool range %v - %v", guid, p.rangeStart, p.rangeEnd)
	}

	if _, exist := p.guidPoolMap[guidAddr]; exist {
		return fmt.Errorf("failed to allocate requested guid %s, already allocated", guid)
	}

	p.guidPoolMap[guidAddr] = pkey
	return nil
}

func isValidRange(rangeStart, rangeEnd GUID) bool {
	return rangeStart <= rangeEnd && rangeStart != 0 && rangeEnd != 0xFFFFFFFFFFFFFFFF
}

func (p *guidPool) isGUIDInRange(guid GUID) bool {
	return guid >= p.rangeStart && guid <= p.rangeEnd
}

// firstFree returns the first GUID in [start, end] that is not reserved.
// The caller holds p.mu. A start past end, or a zero start, means "nothing here".
func (p *guidPool) firstFree(start, end GUID) (GUID, bool) {
	if start == 0 || start > end {
		return 0, false
	}
	for guid := start; ; guid++ {
		if _, taken := p.guidPoolMap[guid]; !taken {
			return guid, true
		}
		if guid == end {
			return 0, false
		}
	}
}
