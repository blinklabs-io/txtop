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
	"sync/atomic"
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

func TestGetVersionString(t *testing.T) {
	tests := []struct {
		name         string
		version      string
		commitHash   string
		expected     string
	}{
		{
			name:       "with version",
			version:    "1.0.0",
			commitHash: "abc123",
			expected:   "1.0.0 (commit abc123)",
		},
		{
			name:       "without version",
			version:    "",
			commitHash: "def456",
			expected:   "devel (commit def456)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original values
			origVersion := Version
			origCommitHash := CommitHash

			// Set test values
			Version = tt.version
			CommitHash = tt.commitHash

			// Restore original values after test
			defer func() {
				Version = origVersion
				CommitHash = origCommitHash
			}()

			result := GetVersionString()
			if result != tt.expected {
				t.Errorf("GetVersionString() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestUpdateFooterText(t *testing.T) {
	tests := []struct {
		name     string
		paused   bool
		sortBy   string
		expected string
	}{
		{
			name:     "not paused, sort by size",
			paused:   false,
			sortBy:   "size",
			expected: " [yellow](esc/q)[white] Quit | [yellow](p)[white] Pause | [yellow](s)[white] Sort: size",
		},
		{
			name:     "paused, sort by time",
			paused:   true,
			sortBy:   "time",
			expected: " [yellow](esc/q)[white] Quit | [yellow](p)[white] Pause [yellow](paused) | [yellow](s)[white] Sort: time",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := updateFooterText(tt.paused, tt.sortBy)
			if result != tt.expected {
				t.Errorf("updateFooterText() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestIsPaused(t *testing.T) {
	// Save original paused value
	origPaused := atomic.LoadInt32(&paused)
	defer atomic.StoreInt32(&paused, origPaused)

	tests := []struct {
		name     string
		paused   int32
		expected bool
	}{
		{"not paused", 0, false},
		{"paused", 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atomic.StoreInt32(&paused, tt.paused)
			result := isPaused()
			if result != tt.expected {
				t.Errorf("isPaused() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestTogglePaused(t *testing.T) {
	// Save original paused value
	origPaused := atomic.LoadInt32(&paused)
	defer atomic.StoreInt32(&paused, origPaused)

	// Start with not paused
	atomic.StoreInt32(&paused, 0)

	// First toggle should return true (now paused)
	result1 := togglePaused()
	if result1 != true {
		t.Errorf("togglePaused() first call = %v, want true", result1)
	}
	if !isPaused() {
		t.Error("Expected to be paused after first toggle")
	}

	// Second toggle should return false (now not paused)
	result2 := togglePaused()
	if result2 != false {
		t.Errorf("togglePaused() second call = %v, want false", result2)
	}
	if isPaused() {
		t.Error("Expected to not be paused after second toggle")
	}
}

func TestLogBuffer_Write(t *testing.T) {
	lb := &LogBuffer{maxLines: 3}

	// Test writing lines
	testLines := []string{"line1", "line2", "line3", "line4"}

	for _, line := range testLines {
		n, err := lb.Write([]byte(line))
		if err != nil {
			t.Errorf("LogBuffer.Write() error = %v", err)
		}
		if n != len(line) {
			t.Errorf("LogBuffer.Write() returned %d, want %d", n, len(line))
		}
	}

	// Check that only maxLines are kept
	lb.mu.RLock()
	if len(lb.lines) != 3 {
		t.Errorf("LogBuffer lines length = %d, want 3", len(lb.lines))
	}
	// Should contain the last 3 lines
	expected := []string{"line2", "line3", "line4"}
	for i, expectedLine := range expected {
		if lb.lines[i] != expectedLine {
			t.Errorf("LogBuffer line %d = %q, want %q", i, lb.lines[i], expectedLine)
		}
	}
	lb.mu.RUnlock()
}
