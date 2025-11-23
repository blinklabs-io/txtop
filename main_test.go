// Copyright 2025 Blink Labs Software
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

package main

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

type txInfo struct {
	size int
	icon string
	hash string
}

func BenchmarkSortTransactions(b *testing.B) {
	// Generate 1000 transactions with random sizes
	txs := make([]txInfo, 1000)
	for i := range txs {
		txs[i] = txInfo{
			size: rand.Intn(10000),
			icon: "🐱",
			hash: fmt.Sprintf("hash%d", i),
		}
	}

	b.Run("by_size", func(b *testing.B) {
		for b.Loop() {
			// Copy slice to avoid modifying original
			sorted := make([]txInfo, len(txs))
			copy(sorted, txs)
			// Sort by size descending
			sort.Slice(sorted, func(i, j int) bool {
				return sorted[i].size > sorted[j].size
			})
			// Take top 100 (simulating MaxDisplayedTransactions)
			if len(sorted) > 100 {
				sorted = sorted[:100]
			}
		}
	})

	b.Run("by_time", func(b *testing.B) {
		for b.Loop() {
			// Copy slice to avoid modifying original
			timed := make([]txInfo, len(txs))
			copy(timed, txs)
			// No sorting, just take first 100 (simulating time order)
			if len(timed) > 100 {
				timed = timed[:100]
			}
		}
	})
}
