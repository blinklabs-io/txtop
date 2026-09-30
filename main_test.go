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
	"encoding/hex"
	"fmt"
	"math/big"
	"math/rand"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/fxamacker/cbor/v2"
	"github.com/rivo/tview"
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
			expected: " [yellow](esc/q)[white] Quit | [yellow](p)[white] Pause [yellow](paused — ↑/↓ browse · Enter details) | [yellow](s)[white] Sort: time",
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

func TestFormatTxDetail(t *testing.T) {
	tx := Tx{
		Hash:     "d4f1a2e9",
		Icon:     "🏹",
		Size:     16384,
		Fee:      182611,
		TotalOut: 124500000,
		IsValid:  true,
		Inputs:   []string{"9af3b2#0", "1c770a#2"},
		Outputs: []TxOutput{
			{Address: "addr1qx7c", Amount: 120000000, NumAssets: 2},
			{Address: "addr1w9fq", Amount: 4500000},
		},
		ProtocolLabel:  "Dexhunter",
		MatchedAddress: "addr1w9hvelc46ptfq",
		MetadataLabels: []uint64{674},
		Cip20Message:   []string{"Dexhunter Trade"},
		CertTypes:      []string{"StakeDelegation"},
		TTL:            12345678,
	}
	out := formatTxDetail(tx)

	for _, want := range []string{
		"d4f1a2e9",         // hash
		"16384",            // size
		"0.182611",         // fee in ADA
		"Inputs:    2",     // summary input count
		"Outputs: 2",       // summary output count
		"Inputs / outputs", // section heading
		"Dexhunter",        // protocol label
		"674",              // metadata label
		"Dexhunter Trade",  // cip20 message
		"9af3b2#0",         // input UTxO
		"addr1qx7c",        // output address
		"120.000000",       // output amount (ADA)
		"+2 assets",        // asset count annotation
		"StakeDelegation",  // cert type
		"12345678",         // TTL
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatTxDetail() missing %q\n---\n%s", want, out)
		}
	}
}

func TestFormatTxDetailEmptySections(t *testing.T) {
	// A bare tx (no metadata/certs/votes) must still render stable "none" lines.
	out := formatTxDetail(Tx{Hash: "abc", IsValid: true})
	for _, want := range []string{"Protocol", "Certs", "none"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatTxDetail(bare) missing %q\n---\n%s", want, out)
		}
	}
}

func TestPopulateTable(t *testing.T) {
	// Width 0 (no draw yet) means hashes are shown in full.
	origWidth := lastTableWidth
	lastTableWidth = 0
	defer func() { lastTableWidth = origWidth }()

	table := tview.NewTable()
	txs := []Tx{
		{Hash: "aaa", Size: 16384, Icon: "🏹"},
		{Hash: "bbb", Size: 8192},
	}
	populateTable(table, txs)

	// header row
	if got := table.GetCell(0, 0).Text; !strings.Contains(got, "Size") {
		t.Errorf("header col0 = %q, want to contain Size", got)
	}
	if got := table.GetCell(0, 2).Text; !strings.Contains(got, "TxHash") {
		t.Errorf("header col2 = %q, want to contain TxHash", got)
	}
	// data rows
	if got := table.GetCell(1, 0).Text; !strings.Contains(got, "16384") {
		t.Errorf("row1 size = %q, want to contain 16384", got)
	}
	if got := table.GetCell(1, 1).Text; got != "🏹" {
		t.Errorf("row1 icon = %q, want 🏹", got)
	}
	if got := table.GetCell(1, 2).Text; !strings.Contains(got, "aaa") {
		t.Errorf("row1 hash = %q, want to contain aaa", got)
	}
	// row count = header + 2 data rows
	if table.GetRowCount() != 3 {
		t.Errorf("row count = %d, want 3", table.GetRowCount())
	}

	// With a narrow table width, a long hash is shortened to fit the hash column.
	lastTableWidth = 40
	longHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	populateTable(table, []Tx{{Hash: longHash, Size: 1}})
	got := table.GetCell(1, 2).Text
	if got == longHash {
		t.Errorf("expected long hash to be shortened at narrow width, got full hash")
	}
	if !strings.Contains(got, "...") {
		t.Errorf("shortened hash %q should contain an ellipsis", got)
	}
}

func TestFormatHash(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 64 chars

	// Full hash when it fits or the width is unknown.
	for _, avail := range []int{0, -1, len(hash), len(hash) + 50} {
		if got := formatHash(hash, avail); got != hash {
			t.Errorf("formatHash(hash, %d) = %q, want full hash", avail, got)
		}
	}

	// The result must never be wider than avail (the overflow regression). Check
	// every shortening width, including the very narrow ones.
	for avail := 1; avail < len(hash); avail++ {
		if got := formatHash(hash, avail); len(got) > avail {
			t.Errorf("formatHash(hash, %d) = %q (len %d) exceeds avail", avail, got, len(got))
		}
	}

	// Balanced middle ellipsis: avail 20 -> 8 chars + "..." + 9 chars = 20.
	if got, want := formatHash(hash, 20), hash[:8]+"..."+hash[len(hash)-9:]; got != want {
		t.Errorf("formatHash(hash, 20) = %q, want %q", got, want)
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

func TestProtocolLabelForIcon(t *testing.T) {
	tests := []struct {
		icon string
		want string
	}{
		{"🏹", "Dexhunter"},
		{"🐱", "Minswap"},
		{"🧱", "SteelSwap"}, // detected but absent from the legend
		{"🕺", "Silk Toad"}, // detected but absent from the legend
		{"🔵", "VyFinance"}, // detected but absent from the legend
		{"👁️ ", "Indigo"},  // detection icon has a trailing space
		{"", ""},
		{"🚀", ""}, // unknown icon
	}
	for _, tt := range tests {
		if got := protocolLabelForIcon(tt.icon); got != tt.want {
			t.Errorf("protocolLabelForIcon(%q) = %q, want %q", tt.icon, got, tt.want)
		}
	}
}

func TestMetadataLabels(t *testing.T) {
	md := lcommon.MetaMap{Pairs: []lcommon.MetaPair{
		{Key: lcommon.MetaInt{Value: big.NewInt(674)}, Value: lcommon.MetaText{Value: "x"}},
		{Key: lcommon.MetaInt{Value: big.NewInt(8746)}, Value: lcommon.MetaText{Value: "y"}},
	}}
	got := metadataLabels(md)
	want := []uint64{674, 8746}
	if len(got) != len(want) {
		t.Fatalf("metadataLabels() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("metadataLabels()[%d] = %d, want %d", i, got[i], want[i])
		}
	}
	if metadataLabels(nil) != nil {
		t.Errorf("metadataLabels(nil) should be nil")
	}
}

func TestIconFromOutputAddress(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"dripdropz", "addr1v8pr9mwnqarw808gtllvmlxvk70hnszrukjeqfstr9t9g5crud8c4", "🚰"},
		{"dexhunter", "addr1w9hvftxrlw74wzk6vf0jfyp8wl8vt4arf8aq70rm4paselc46ptfq", "🏹"},
		{"jpg", "addr1zxgx3far7qygq0k6epa0zcvcvrevmn0ypsnfsue94nsn3tvpw288a4x0xf8pxgcntelxmyclq83s0ykeehchz2wtspks905plm", "🦛"},
		{"vyfinance", "addr1w8ll74xa05dkn69n3rmp93h8maphmms2408nt0nyruarzvqr9zf64", "🔵"},
		{"unknown", "addr1qxyz", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := iconFromOutputAddress(tt.addr); got != tt.want {
				t.Errorf("iconFromOutputAddress(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestIconFromStakeAddress(t *testing.T) {
	if got := iconFromStakeAddress("stake1u8ffzkegp8h48mare3g3ntf3xmjce3jqptsdtj38ee3yh3c9t4uum"); got != "🦭" {
		t.Errorf("iconFromStakeAddress(seal) = %q, want 🦭", got)
	}
	if got := iconFromStakeAddress("stake1uxyz"); got != "" {
		t.Errorf("iconFromStakeAddress(unknown) = %q, want \"\"", got)
	}
}

func TestIconFromCertificates(t *testing.T) {
	tests := []struct {
		name  string
		certs []lcommon.Certificate
		want  string
	}{
		{"none", nil, ""},
		{"staking", []lcommon.Certificate{&lcommon.StakeDelegationCertificate{}}, "🥩"},
		{"pool", []lcommon.Certificate{&lcommon.PoolRegistrationCertificate{}}, "🏊"},
		{"governance", []lcommon.Certificate{&lcommon.RegistrationDrepCertificate{}}, "🏛️"},
		{"first match wins", []lcommon.Certificate{
			&lcommon.PoolRetirementCertificate{},
			&lcommon.StakeDelegationCertificate{},
		}, "🏊"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := iconFromCertificates(tt.certs); got != tt.want {
				t.Errorf("iconFromCertificates() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCertTypeNames(t *testing.T) {
	got := certTypeNames([]lcommon.Certificate{
		&lcommon.StakeDelegationCertificate{},
		&lcommon.PoolRegistrationCertificate{},
	})
	want := []string{"StakeDelegation", "PoolRegistration"}
	if len(got) != len(want) {
		t.Fatalf("certTypeNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("certTypeNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIconFromCip20Messages(t *testing.T) {
	tests := []struct {
		name string
		msgs []string
		want string
	}{
		{"empty", nil, ""},
		{"dexhunter", []string{"Dexhunter Trade"}, "🏹"},
		{"minswap swap", []string{"Minswap: Swap Exact In Order"}, "🐱"},
		{"sundae", []string{"SSP: Swap Request"}, "🍨"},
		{"steelswap after another line", []string{"CarDeM", "SteelSwap: 1.18.0"}, "🧱"},
		{"steelswap first line", []string{"SteelSwap: 2.0.0"}, "🧱"},
		{"steelswap after dexhunter", []string{"Dexhunter Trade", "SteelSwap: 1.18.0"}, "🧱"},
		{"steelswap after minswap", []string{"Minswap: Swap Exact In Order", "SteelSwap: 1.18.0"}, "🧱"},
		{"steelswap without prefix", []string{"not SteelSwap"}, ""},
		{"only first line considered", []string{"unknown", "Dexhunter Trade"}, ""},
		{"unknown", []string{"hello world"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := iconFromCip20Messages(tt.msgs); got != tt.want {
				t.Errorf("iconFromCip20Messages(%v) = %q, want %q", tt.msgs, got, tt.want)
			}
		})
	}
}

func TestExtractTxMalformed(t *testing.T) {
	// Random bytes are not a valid transaction; extractTx must return an error,
	// not panic, so buildTxs can skip the tx.
	_, err := extractTx([]byte{0x00, 0x01, 0x02, 0x03})
	if err == nil {
		t.Fatal("extractTx(garbage) returned nil error, want error")
	}
}

// txCborWithMessages builds a minimal Conway transaction whose auxiliary data
// carries a CIP-20 (label 674) message.
func txCborWithMessages(t *testing.T, msgs []string) []byte {
	t.Helper()
	body := map[uint]any{
		0: []any{[]any{make([]byte, 32), uint(0)}},
		1: []any{[]any{append([]byte{0x61}, make([]byte, 28)...), uint(1000000)}},
		2: uint(200000),
	}
	auxData := map[uint]any{674: map[string]any{"msg": msgs}}
	raw, err := cbor.Marshal([]any{body, map[uint]any{}, true, auxData})
	if err != nil {
		t.Fatalf("marshal tx: %v", err)
	}
	return raw
}

func TestExtractTxSteelSwapIcon(t *testing.T) {
	tests := []struct {
		name string
		msgs []string
		want string
	}{
		{"steelswap", []string{"CarDeM", "SteelSwap: 1.18.0"}, "🧱"},
		{"other version", []string{"CarDeM", "SteelSwap: 2.0.0"}, "🧱"},
		{"unrelated message", []string{"hello world"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := extractTx(txCborWithMessages(t, tt.msgs))
			if err != nil {
				t.Fatalf("extractTx() error = %v", err)
			}
			if tx.Icon != tt.want {
				t.Errorf("extractTx() icon = %q, want %q", tx.Icon, tt.want)
			}
		})
	}
}

// TestExtractTxRealSteelSwapTransaction decodes an unmodified mainnet
// transaction (c78c995f40885f82eae2e3a5af498bcb805adf6737d8aab10788bfac20ecb239) whose label-674 message is
// ["CarDeM", "SteelSwap: 1.18.0"].
func TestExtractTxRealSteelSwapTransaction(t *testing.T) {
	raw, err := hex.DecodeString(
		"84a500d901028182582016a66157e4444eea03a3832cbcc581f872d8f065cdfe" +
			"042acfbcbf2a7038e0f7000182a300581d71c134d839a64a5dfb9b155869ef3f" +
			"34280751a622f69958baa8ffd29c011a1e0a6e00028201d81858f1d8799f1a00" +
			"1e8480d8799fd8799f581ccbb7479da4fee934d6cad1c741ec1db1763f14ccc9" +
			"1ee20855a90c52ffd8799fd8799fd8799f581c2ce46b16000844a172a4126878" +
			"954ef93d9c533f0e6a254793cd3babffffffffd8799fd8799f581ccbb7479da4" +
			"fee934d6cad1c741ec1db1763f14ccc91ee20855a90c52ffd8799fd8799fd879" +
			"9f581c2ce46b16000844a172a4126878954ef93d9c533f0e6a254793cd3babff" +
			"ffffff40d879801b000001a818ffd4374040581c8db269c3ec630e06ae29f74b" +
			"c39edd1f87c819f1056206e879a1cd614c446a65644d6963726f555344d8799f" +
			"d879801a07a5c781ff0101ff82583901cbb7479da4fee934d6cad1c741ec1db1" +
			"763f14ccc91ee20855a90c522ce46b16000844a172a4126878954ef93d9c533f" +
			"0e6a254793cd3bab1a03310085021a0002c519031a0bd9b50c0758203b308c14" +
			"8cd78090ef1c8e41e30f288b4c04df1a2296fd1c89b320d120bb4975a1008182" +
			"58203b326825653beddb6d706ed19638e4a5482e035683bd6c246e67d0c1edcd" +
			"ad415840a367d31c47a2db16272f009ca01ab7432509a36085d7709c3489f184" +
			"f7b9826336eb78a38be2da05a742d435dda35a11dfef491e55a37761fd150313" +
			"1d016506f5d90103a100a11902a2a1636d7367826643617244654d7153746565" +
			"6c537761703a20312e31382e30",
	)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	tx, err := extractTx(raw)
	if err != nil {
		t.Fatalf("extractTx() error = %v", err)
	}
	if tx.Icon != "🧱" {
		t.Errorf("extractTx() icon = %q, want %q", tx.Icon, "🧱")
	}
	if got := protocolLabelForIcon(tx.Icon); got != "SteelSwap" {
		t.Errorf("protocol label = %q, want SteelSwap", got)
	}
}

func TestSortAndCapTxs(t *testing.T) {
	in := []Tx{
		{Hash: "a", Size: 100},
		{Hash: "b", Size: 300},
		{Hash: "c", Size: 200},
	}

	t.Run("by size desc", func(t *testing.T) {
		got := sortAndCapTxs(in, "size", 10)
		order := []string{got[0].Hash, got[1].Hash, got[2].Hash}
		want := []string{"b", "c", "a"}
		for i := range want {
			if order[i] != want[i] {
				t.Errorf("order[%d] = %q, want %q", i, order[i], want[i])
			}
		}
	})

	t.Run("by time keeps input order", func(t *testing.T) {
		got := sortAndCapTxs(in, "time", 10)
		if got[0].Hash != "a" || got[1].Hash != "b" || got[2].Hash != "c" {
			t.Errorf("time order changed: %v", got)
		}
	})

	t.Run("caps to max", func(t *testing.T) {
		got := sortAndCapTxs(in, "size", 2)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].Hash != "b" || got[1].Hash != "c" {
			t.Errorf("capped wrong txs: %v", got)
		}
	})

	t.Run("does not mutate input", func(t *testing.T) {
		_ = sortAndCapTxs(in, "size", 10)
		if in[0].Hash != "a" {
			t.Errorf("input was mutated: %v", in)
		}
	})

	t.Run("negative max returns all", func(t *testing.T) {
		got := sortAndCapTxs(in, "time", -1)
		if len(got) != len(in) {
			t.Errorf("len = %d, want %d", len(got), len(in))
		}
	})
}
