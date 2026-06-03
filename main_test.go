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
	"math/big"
	"math/rand"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/rivo/uniseg"
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
		name       string
		version    string
		commitHash string
		expected   string
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
				t.Errorf(
					"GetVersionString() = %v, want %v",
					result,
					tt.expected,
				)
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
				t.Errorf(
					"updateFooterText() = %q, want %q",
					result,
					tt.expected,
				)
			}
		})
	}
}

func displayIndex(row, needle string) int {
	index := strings.Index(row, needle)
	if index == -1 {
		return -1
	}
	return uniseg.StringWidth(row[:index])
}

// TestBuildLegendTextAlignsGrid verifies that labels in the same legend column
// start at the same visible terminal position across all rows.
func TestBuildLegendTextAlignsGrid(t *testing.T) {
	legend := strings.ReplaceAll(buildLegendText(), "[white]", "")
	rows := strings.Split(legend, "\n")
	if len(rows) != 3 {
		t.Fatalf("buildLegendText() returned %d rows, want 3", len(rows))
	}

	labelRows := [][]string{
		{"Dexhunter", "DripDropz", "Indigo", "JPGstore", "Liqwid", "Minswap"},
		{"Optim", "Splash", "Sundae", "SealVM", "Wingriders"},
		{"Strike", "Staking", "SPOs", "Governance", "AdaHandle", "Materios"},
	}

	expectedLabelPositions := make([]int, len(labelRows[0]))
	for i, label := range labelRows[0] {
		expectedLabelPositions[i] = displayIndex(rows[0], label)
	}

	for rowIndex, labels := range labelRows[1:] {
		for columnIndex, label := range labels {
			got := displayIndex(rows[rowIndex+1], label)
			if got != expectedLabelPositions[columnIndex] {
				t.Errorf(
					"label %q starts at display column %d, want %d",
					label,
					got,
					expectedLabelPositions[columnIndex],
				)
			}
		}
	}
}

// TestIsMateriosMetadata verifies detection of Materios Cardano metadata
// without requiring a live node or mempool transaction.
func TestIsMateriosMetadata(t *testing.T) {
	materiosMetadata := lcommon.MetaMap{Pairs: []lcommon.MetaPair{
		{
			Key: lcommon.MetaInt{Value: big.NewInt(8746)},
			Value: lcommon.MetaList{Items: []lcommon.TransactionMetadatum{
				lcommon.MetaMap{Pairs: []lcommon.MetaPair{
					{
						Key:   lcommon.MetaText{Value: "k"},
						Value: lcommon.MetaText{Value: "p"},
					},
					{
						Key:   lcommon.MetaText{Value: "v"},
						Value: lcommon.MetaText{Value: "materios"},
					},
				}},
			}},
		},
	}}

	tests := []struct {
		name     string
		metadata lcommon.TransactionMetadatum
		expected bool
	}{
		{
			name:     "materios label and marker",
			metadata: materiosMetadata,
			expected: true,
		},
		{
			name: "same marker under different label",
			metadata: lcommon.MetaMap{Pairs: []lcommon.MetaPair{
				{
					Key:   lcommon.MetaInt{Value: big.NewInt(674)},
					Value: materiosMetadata.Pairs[0].Value,
				},
			}},
			expected: false,
		},
		{
			name: "materios label without marker",
			metadata: lcommon.MetaMap{Pairs: []lcommon.MetaPair{
				{
					Key: lcommon.MetaInt{Value: big.NewInt(8746)},
					Value: lcommon.MetaMap{Pairs: []lcommon.MetaPair{
						{
							Key:   lcommon.MetaText{Value: "v"},
							Value: lcommon.MetaText{Value: "other"},
						},
					}},
				},
			}},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isMateriosMetadata(tt.metadata)
			if result != tt.expected {
				t.Errorf("isMateriosMetadata() = %v, want %v", result, tt.expected)
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

func TestErrorChanIsolation(t *testing.T) {
	// Regression test for issue panic: send on closed channel.
	//
	// gouroboros closes whichever error channel it is given during connection
	// shutdown. Passing the same shared errorChan to every connection means
	// the first shutdown closes it, and the second connection panics when it
	// tries to send on the now-closed channel.
	//
	// The fix gives each connection its own connErrChan and relays errors into
	// the shared channel. This test verifies that pattern: two simulated
	// connection lifecycles must not panic and must relay their errors.

	sharedErrChan := make(chan error, 10)

	simulateConnection := func(id int) {
		connErrChan := make(chan error, 10)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for err := range connErrChan {
				sharedErrChan <- err
			}
		}()
		connErrChan <- fmt.Errorf("error from connection %d", id)
		close(connErrChan) // gouroboros closes this on shutdown
		<-done             // wait for relay goroutine to finish
	}

	simulateConnection(1)
	simulateConnection(2) // would panic before the fix

	if len(sharedErrChan) != 2 {
		t.Errorf("expected 2 relayed errors, got %d", len(sharedErrChan))
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
			t.Errorf(
				"LogBuffer line %d = %q, want %q",
				i,
				lb.lines[i],
				expectedLine,
			)
		}
	}
	lb.mu.RUnlock()
}
