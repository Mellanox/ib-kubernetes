// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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

package daemon

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Serialized daemon lifecycle", func() {
	It("runs cleanup, NAD and add steps in order before the next pass", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls []string
		runPeriodicSteps(ctx, time.Hour,
			func() { calls = append(calls, "cleanup") },
			func() { calls = append(calls, "NAD") },
			func() { calls = append(calls, "add"); cancel() })
		Expect(calls).To(Equal([]string{"cleanup", "NAD", "add"}))
	})

	It("skips remaining steps when the current step cancels leadership", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		runPeriodicSteps(ctx, time.Hour,
			func() { calls++; cancel() },
			func() { calls++ })
		Expect(calls).To(Equal(1))
	})

	It("does not start an asynchronous leader callback after shutdown", func() {
		work := &leaderWork{}
		work.stopAndWait()
		ran := false
		work.run(context.Background(), func(context.Context) { ran = true })
		Expect(ran).To(BeFalse())
	})

	It("cancels and drains active leader work before shutdown returns", func() {
		work := &leaderWork{}
		started := make(chan struct{})
		canceled := make(chan struct{})
		release := make(chan struct{})
		exited := make(chan struct{})
		stopped := make(chan struct{})
		go func() {
			defer close(exited)
			work.run(context.Background(), func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
			})
		}()
		Eventually(started).Should(BeClosed())
		go func() { work.stopAndWait(); close(stopped) }()
		Eventually(canceled).Should(BeClosed())
		// The cancellation handshake ensures stopAndWait reached the drain.
		select {
		case <-stopped:
			Fail("shutdown returned while leader work was still active")
		default:
		}
		close(release)
		Eventually(stopped).Should(BeClosed())
		Eventually(exited).Should(BeClosed())
	})

	It("passes involuntary leadership cancellation to the worker", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		work := &leaderWork{}
		work.run(ctx, func(workCtx context.Context) {
			cancel()
			Expect(workCtx.Err()).To(MatchError(context.Canceled))
		})
		work.stopAndWait()
	})
})
