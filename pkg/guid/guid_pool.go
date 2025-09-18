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

	"github.com/rs/zerolog/log"

	"github.com/Mellanox/ib-kubernetes/pkg/config"
)

type Pool interface {
	// AllocateGUID allocate given guid if in range or
	// allocate the next free guid in the range if no given guid.
	// It returns the allocated guid or error if range is full.
	AllocateGUID(string, string) error

	GenerateGUID() (GUID, error)

	// ReleaseGUID release the reservation of the guid.
	// It returns error if the guid is not in the range.
	ReleaseGUID(string) error

	// Reset clears the current pool and resets it with given values (may be empty)
	Reset(guids map[string]string) error

	Get(string) (string, error)
}

var ErrGUIDPoolExhausted = errors.New("GUID pool is exhausted")

type guidPool struct {
	rangeStart  GUID            // first guid in range
	rangeEnd    GUID            // last guid in range
	currentGUID GUID            // last given guid
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

// Reset clears the current pool and resets it with given values (may be empty)
func (p *guidPool) Reset(guids map[string]string) error {
	log.Debug().Msg("resetting guid pool")

	p.guidPoolMap = map[GUID]string{}
	if guids == nil {
		return nil
	}

	for guid := range guids {
		pkey := guids[guid]
		guidInRange, err := p.isGUIDStringInRange(guid)
		if err != nil {
			log.Debug().Msgf("error validating GUID: %s: %v", guid, err)
			return err
		}
		if !guidInRange {
			// Out of range GUID may be expected and shouldn't be allocated in the pool
			continue
		}
		err = p.AllocateGUID(guid, pkey)
		if err != nil {
			log.Debug().Msgf("error resetting the pool with value: %s: %v", guid, err)
			return err
		}
	}
	return nil
}

// GenerateGUID generates a guid from the range
func (p *guidPool) GenerateGUID() (GUID, error) {
	// this look will ensure that we check all the range
	// first iteration from current guid to last guid in the range
	// second iteration from first guid in the range to the latest one
	if guid := p.getFreeGUID(p.currentGUID, p.rangeEnd); guid != 0 {
		return guid, nil
	}

	if guid := p.getFreeGUID(p.rangeStart, p.rangeEnd); guid != 0 {
		return guid, nil
	}
	return 0, ErrGUIDPoolExhausted
}

// ReleaseGUID release allocated guid
func (p *guidPool) ReleaseGUID(guid string) error {
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
	guidAddr, err := ParseGUID(guid)
	if err != nil {
		return "", err
	}
	pkey := p.guidPoolMap[guidAddr]
	return pkey, nil
}

func (p *guidPool) AllocateGUID(guid string, pkey string) error {
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

func (p *guidPool) isGUIDStringInRange(guid string) (bool, error) {
	guidAddr, err := ParseGUID(guid)
	if err != nil {
		return false, err
	}
	return p.isGUIDInRange(guidAddr), nil
}

// getFreeGUID return free guid in given range
func (p *guidPool) getFreeGUID(start, end GUID) GUID {
	for guid := start; guid <= end; guid++ {
		if _, ok := p.guidPoolMap[guid]; !ok {
			p.currentGUID++
			return guid
		}
	}

	return 0
}
