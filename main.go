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
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	models "github.com/blinklabs-io/cardano-models"
	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/fxamacker/cbor/v2"
	"github.com/gdamore/tcell/v2"
	"github.com/kelseyhightower/envconfig"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"
	"gopkg.in/yaml.v3"
)

type LogBuffer struct {
	mu       sync.RWMutex
	lines    []string
	maxLines int
}

func (lb *LogBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	lb.lines = append(lb.lines, string(p))
	if len(lb.lines) > lb.maxLines {
		lb.lines = lb.lines[len(lb.lines)-lb.maxLines:]
	}
	lb.mu.Unlock()
	return len(p), nil
}

func (lb *LogBuffer) String() string {
	lb.mu.RLock()
	s := strings.Join(lb.lines, "")
	lb.mu.RUnlock()
	return s
}

var logBuffer = &LogBuffer{maxLines: 1000}

var globalConfig = &Config{
	App: AppConfig{
		Network:                  "",
		Refresh:                  3,
		Retries:                  3,
		LogBufferSize:            1000,
		MaxBackoff:               30,
		MaxDisplayedTransactions: 100,
		SortBy:                   "size",
	},
	Node: NodeConfig{
		Network:    "mainnet",
		Port:       30001,
		SocketPath: "/opt/cardano/ipc/socket",
	},
}

var (
	app   = tview.NewApplication()
	pages = tview.NewPages()
	flex  = tview.NewFlex()
)

var headerText = tview.NewTextView().
	SetDynamicColors(true).
	SetTextColor(tcell.ColorGreen)

var footerText = tview.NewTextView().
	SetDynamicColors(true).
	SetChangedFunc(func() { app.Draw() })

var legendText = tview.NewTextView().
	SetDynamicColors(true).
	SetTextColor(tcell.ColorGreen)

var summaryText = tview.NewTextView().
	SetDynamicColors(true).
	SetChangedFunc(func() { app.Draw() })

var txTable = tview.NewTable().
	SetFixed(1, 0).
	SetSelectable(false, false)

var (
	paused        int32 = 0 // 0 = false, 1 = true (atomic)
	sortMu        sync.RWMutex
	currentSortBy string = "size"
)

var (
	currentTxs   []Tx
	currentTxsMu sync.RWMutex // guards currentTxs; written/read only via the helpers below

	// lastTableWidth is the terminal width seen at the last draw. The table spans
	// the full-width root Flex, so this is also the table's width; it is used to
	// shorten transaction hashes to the available room and to re-shorten them on
	// resize. Accessed only on the event loop.
	lastTableWidth int
)

type legendItem struct {
	icon  string
	label string
}

// Atomic helpers for paused variable
func isPaused() bool {
	return atomic.LoadInt32(&paused) == 1
}

func togglePaused() bool {
	for {
		cur := atomic.LoadInt32(&paused)
		next := cur ^ 1
		if atomic.CompareAndSwapInt32(&paused, cur, next) {
			return next == 1
		}
	}
}

// These are populated at build time
var (
	Version    string
	CommitHash string
)

func GetVersionString() string {
	if Version != "" {
		return fmt.Sprintf("%s (commit %s)", Version, CommitHash)
	} else {
		return fmt.Sprintf("devel (commit %s)", CommitHash)
	}
}

type Config struct {
	App  AppConfig  `yaml:"app"`
	Node NodeConfig `yaml:"node"`
}

type AppConfig struct {
	Network                  string `envconfig:"NETWORK"                    yaml:"network"`
	Refresh                  uint32 `envconfig:"REFRESH"                    yaml:"refresh"`
	Retries                  uint32 `envconfig:"RETRIES"                    yaml:"retries"`
	LogBufferSize            uint32 `envconfig:"LOG_BUFFER_SIZE"            yaml:"log_buffer_size"`
	MaxBackoff               uint32 `envconfig:"MAX_BACKOFF"                yaml:"max_backoff"`
	MaxDisplayedTransactions uint32 `envconfig:"MAX_DISPLAYED_TRANSACTIONS" yaml:"max_displayed_transactions"`
	SortBy                   string `envconfig:"SORT_BY"                    yaml:"sort_by"`
}

type NodeConfig struct {
	Network      string `envconfig:"CARDANO_NETWORK"              yaml:"network"`
	NetworkMagic uint32 `envconfig:"CARDANO_NODE_NETWORK_MAGIC"   yaml:"network_magic"`
	SocketPath   string `envconfig:"CARDANO_NODE_SOCKET_PATH"     yaml:"socket_path"`
	Address      string `envconfig:"CARDANO_NODE_SOCKET_TCP_HOST" yaml:"address"`
	Port         uint32 `envconfig:"CARDANO_NODE_SOCKET_TCP_PORT" yaml:"port"`
}

func (c *Config) Load(configFile string) error {
	// Load config file as YAML if provided
	if configFile != "" {
		buf, err := os.ReadFile(filepath.Clean(configFile))
		if err != nil {
			return fmt.Errorf("error reading config file: %w", err)
		}
		err = yaml.Unmarshal(buf, c)
		if err != nil {
			return fmt.Errorf("error parsing config file: %w", err)
		}
	}
	// Load config values from environment variables
	err := envconfig.Process("txtop", c)
	if err != nil {
		return fmt.Errorf("error processing environment: %w", err)
	}
	return nil
}

func LoadConfig() (*Config, error) {
	configFile := os.Getenv("CONFIG_FILE")
	err := globalConfig.Load(configFile)
	if err != nil {
		return nil, err
	}
	if err := globalConfig.populateNetworkMagic(); err != nil {
		return nil, err
	}
	return globalConfig, nil
}

func GetConfig() *Config {
	return globalConfig
}

// Populates NetworkMagic from named networks
func (c *Config) populateNetworkMagic() error {
	if c.Node.NetworkMagic == 0 {
		if c.App.Network != "" {
			network, ok := ouroboros.NetworkByName(c.App.Network)
			if !ok {
				return fmt.Errorf("unknown network: %s", c.App.Network)
			}
			// Set Node's network, networkMagic, port, and socketPath
			c.Node.Network = c.App.Network
			c.Node.NetworkMagic = uint32(network.NetworkMagic)
			c.Node.SocketPath = "/ipc/node.socket"
			return nil
		} else if c.Node.Network != "" {
			network, ok := ouroboros.NetworkByName(c.Node.Network)
			if !ok {
				return fmt.Errorf("unknown network: %s", c.Node.Network)
			}
			c.Node.NetworkMagic = uint32(network.NetworkMagic)
			return nil
		} else {
			return errors.New("unable to set network magic")
		}
	}
	return nil
}

func GetConnection(errorChan chan error) (*ouroboros.Connection, error) {
	cfg := GetConfig()
	retries := int(cfg.App.Retries)
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			slog.Info(
				"Retrying connection",
				"attempt",
				attempt,
				"max_retries",
				retries,
			)
			delay := min(
				time.Duration(1<<attempt)*time.Second,
				time.Duration(cfg.App.MaxBackoff)*time.Second,
			)
			time.Sleep(delay)
		}
		// gouroboros closes the error channel it's given during shutdown, so we
		// must never hand it the shared errorChan directly. A fresh connection
		// per retry also avoids re-dialing a half-open connection after EOF.
		connErrorChan := make(chan error, 10)
		oConn, err := ouroboros.NewConnection(
			ouroboros.WithNetworkMagic(uint32(cfg.Node.NetworkMagic)),
			ouroboros.WithErrorChan(connErrorChan),
			ouroboros.WithNodeToNode(false),
			ouroboros.WithKeepAlive(true),
		)
		if err != nil {
			return nil, fmt.Errorf("failure creating ouroboros connection: %w", err)
		}
		if cfg.Node.Address != "" && cfg.Node.Port > 0 {
			err = oConn.Dial(
				"tcp",
				fmt.Sprintf("%s:%d", cfg.Node.Address, cfg.Node.Port),
			)
			if err != nil {
				slog.Warn(
					"Failed to connect via TCP",
					"address",
					fmt.Sprintf("%s:%d", cfg.Node.Address, cfg.Node.Port),
					"error",
					err,
					"attempt",
					attempt,
				)
				lastErr = err
				oConn.Close()
				continue
			}
		} else if cfg.Node.SocketPath != "" {
			err = oConn.Dial("unix", cfg.Node.SocketPath)
			if err != nil {
				slog.Warn("Failed to connect via UNIX socket", "path", cfg.Node.SocketPath, "error", err, "attempt", attempt)
				lastErr = err
				oConn.Close()
				continue
			}
		} else {
			oConn.Close()
			return nil, errors.New("specify either the UNIX socket path or the address/port")
		}
		slog.Info("Successfully connected to node")
		go func() {
			for err := range connErrorChan {
				errorChan <- err
			}
		}()
		return oConn, nil
	}
	return nil, fmt.Errorf(
		"failed to connect after %d attempts, last error: %w",
		retries+1,
		lastErr,
	)
}

func GetSizes(oConn *ouroboros.Connection) string {
	if oConn == nil {
		return " [red]failed to connect to node"
	}
	capacity, size, numberOfTxs, err := oConn.LocalTxMonitor().Client.GetSizes()
	if err != nil {
		return fmt.Sprintf(" [red]ERROR: GetSizes: %s", err)
	}
	return fmt.Sprintf(
		" [white]Mempool size (bytes): [blue]%-10d[white] Mempool capacity (bytes): [blue]%-10d[white] Transactions: [blue]%-10d[white]\n",
		size,
		capacity,
		numberOfTxs,
	)
}

// buildTxs walks the mempool and returns the extracted, sorted, capped []Tx.
// Per-tx decode errors are logged and skipped rather than aborting the view.
func buildTxs(oConn *ouroboros.Connection) ([]Tx, error) {
	if oConn == nil {
		return nil, nil
	}
	cfg := GetConfig()
	maxTx := int(cfg.App.MaxDisplayedTransactions)
	var txs []Tx
	for {
		txRawBytes, err := oConn.LocalTxMonitor().Client.NextTx()
		if err != nil {
			return nil, fmt.Errorf("NextTx: %w", err)
		}
		if txRawBytes == nil {
			break
		}
		tx, err := extractTx(txRawBytes)
		if err != nil {
			slog.Warn("skipping malformed transaction", "error", err)
			continue
		}
		txs = append(txs, tx)
	}
	sortMu.RLock()
	sortBy := currentSortBy
	sortMu.RUnlock()
	return sortAndCapTxs(txs, sortBy, maxTx), nil
}

// lovelaceToADA formats a lovelace amount as ADA with 6 decimal places.
func lovelaceToADA(lovelace uint64) string {
	return fmt.Sprintf("%d.%06d", lovelace/1_000_000, lovelace%1_000_000)
}

// formatTxDetail renders a Tx into the detail-page body, with four sections.
// Empty sections render a "none" line so the layout stays stable.
func formatTxDetail(tx Tx) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "[green]Summary[white]\n")
	fmt.Fprintf(&sb, "  Hash:      %s\n", tx.Hash)
	fmt.Fprintf(&sb, "  Size:      %d bytes\n", tx.Size)
	fmt.Fprintf(&sb, "  Fee:       %s ADA\n", lovelaceToADA(tx.Fee))
	fmt.Fprintf(&sb, "  Inputs:    %d     Outputs: %d\n", len(tx.Inputs), len(tx.Outputs))
	fmt.Fprintf(&sb, "  Total out: %s ADA       Valid: %t\n\n", lovelaceToADA(tx.TotalOut), tx.IsValid)

	fmt.Fprintf(&sb, "[green]Protocol & metadata[white]\n")
	if tx.ProtocolLabel != "" {
		fmt.Fprintf(&sb, "  Protocol:  %s %s\n", tx.ProtocolLabel, tx.Icon)
	} else {
		fmt.Fprintf(&sb, "  Protocol:  none\n")
	}
	if tx.MatchedAddress != "" {
		fmt.Fprintf(&sb, "  Address:   %s\n", tx.MatchedAddress)
	}
	if len(tx.MetadataLabels) > 0 {
		fmt.Fprintf(&sb, "  Metadata:  labels %v\n", tx.MetadataLabels)
	} else {
		fmt.Fprintf(&sb, "  Metadata:  none\n")
	}
	for _, m := range tx.Cip20Message {
		fmt.Fprintf(&sb, "  Message:   %q\n", m)
	}
	sb.WriteString("\n")

	fmt.Fprintf(&sb, "[green]Inputs / outputs[white]\n")
	for i, in := range tx.Inputs {
		fmt.Fprintf(&sb, "  In  [%d] %s\n", i, in)
	}
	for i, out := range tx.Outputs {
		line := fmt.Sprintf("  Out [%d] %s   %s ADA", i, out.Address, lovelaceToADA(out.Amount))
		if out.NumAssets > 0 {
			line += fmt.Sprintf("   +%d assets", out.NumAssets)
		}
		sb.WriteString(line + "\n")
	}
	if len(tx.Inputs) == 0 && len(tx.Outputs) == 0 {
		sb.WriteString("  none\n")
	}
	sb.WriteString("\n")

	fmt.Fprintf(&sb, "[green]Certs / governance / mint[white]\n")
	if len(tx.CertTypes) > 0 {
		fmt.Fprintf(&sb, "  Certificates: %s\n", strings.Join(tx.CertTypes, ", "))
	} else {
		fmt.Fprintf(&sb, "  Certificates: none\n")
	}
	fmt.Fprintf(&sb, "  Votes: %d   Proposals: %d   Withdrawals: %s ADA\n",
		tx.NumVotes, tx.NumProposals, lovelaceToADA(tx.Withdrawals))
	fmt.Fprintf(&sb, "  Minted assets: %d\n", tx.MintedAssets)
	fmt.Fprintf(&sb, "  TTL: %d   Validity start: %d\n", tx.TTL, tx.ValidityStart)

	return sb.String()
}

// populateTable clears tbl and fills it: a fixed header row (Size / Icon /
// TxHash) plus one non-selectable header and one row per transaction.
func populateTable(tbl *tview.Table, txs []Tx) {
	tbl.Clear()
	headers := []string{" Size", " Icon", " TxHash"}

	// Size and Icon are always narrow, so they keep their content width; the
	// TxHash column expands to fill the rest of the row. Work out how much width
	// that leaves for the hash so long hashes can be shortened to fit.
	sizeWidth := uniseg.StringWidth(headers[0])
	iconWidth := uniseg.StringWidth(headers[1])
	sizeStrs := make([]string, len(txs))
	for i, tx := range txs {
		s := fmt.Sprintf(" %d", tx.Size)
		sizeStrs[i] = s
		if w := uniseg.StringWidth(s); w > sizeWidth {
			sizeWidth = w
		}
		if w := uniseg.StringWidth(tx.Icon); w > iconWidth {
			iconWidth = w
		}
	}
	// lastTableWidth is the full table width (set from the screen size on each
	// draw); subtract the fixed columns and the one-cell gap between each pair of
	// columns to get the room left for the hash. It is 0 before the first draw,
	// in which case the full hash is shown.
	const columnGaps = 2
	hashWidth := 0
	if lastTableWidth > 0 {
		hashWidth = lastTableWidth - sizeWidth - iconWidth - columnGaps
	}

	for col, h := range headers {
		cell := tview.NewTableCell(h).
			SetTextColor(tcell.ColorWhite).
			SetSelectable(false)
		if col == 2 {
			cell.SetExpansion(1)
		}
		tbl.SetCell(0, col, cell)
	}
	for i, tx := range txs {
		row := i + 1
		tbl.SetCell(row, 0, tview.NewTableCell(sizeStrs[i]).
			SetTextColor(tcell.ColorWhite))
		tbl.SetCell(row, 1, tview.NewTableCell(tx.Icon).
			SetTextColor(tcell.ColorWhite))
		tbl.SetCell(row, 2, tview.NewTableCell(formatHash(tx.Hash, hashWidth)).
			SetTextColor(tcell.ColorBlue).
			SetExpansion(1))
	}
}

// formatHash returns hash unchanged when it fits within avail display cells (or
// when avail is unknown, i.e. <= 0). Otherwise it shortens the hash with a
// middle ellipsis sized to avail, keeping a balanced number of leading and
// trailing characters (e.g. "abc123...def789"). The result is never wider than
// avail.
func formatHash(hash string, avail int) string {
	const ellipsis = "..."
	if avail <= 0 || len(hash) <= avail {
		return hash
	}
	if avail <= len(ellipsis) {
		return hash[:avail]
	}
	left := (avail - len(ellipsis)) / 2
	right := avail - len(ellipsis) - left
	return hash[:left] + ellipsis + hash[len(hash)-right:]
}

// applyTxs swaps in a new snapshot and repopulates the table. MUST be called on
// the event loop (inside QueueUpdateDraw or an input handler).
func applyTxs(txs []Tx) {
	currentTxsMu.Lock()
	currentTxs = txs
	currentTxsMu.Unlock()
	populateTable(txTable, txs)
}

func initializeData(errorChan chan error) {
	oConn, err := GetConnection(errorChan)
	if err != nil {
		slog.Error("Failed to initialize connection", "error", err)
		summaryText.SetText(fmt.Sprintf(" [red]failed to connect to node: %s", err))
		return
	}
	defer oConn.Close()
	summaryText.SetText(GetSizes(oConn))
	txs, err := buildTxs(oConn)
	if err != nil {
		slog.Error("Failed to build transaction list", "error", err)
		summaryText.SetText(fmt.Sprintf(" [red]ERROR: %s", err))
		return
	}
	applyTxs(txs)
}

func updateFooterText(paused bool, sortBy string) string {
	pausedText := ""
	if paused {
		pausedText = " [yellow](paused — ↑/↓ browse · Enter details)"
	}
	return fmt.Sprintf(
		" [yellow](esc/q)[white] Quit | [yellow](p)[white] Pause%s | [yellow](s)[white] Sort: %s",
		pausedText,
		sortBy,
	)
}

func padDisplayWidth(s string, width int) string {
	displayWidth := uniseg.StringWidth(s)
	if displayWidth >= width {
		return s
	}
	return s + strings.Repeat(" ", width-displayWidth)
}

func buildLegendText() string {
	const (
		iconWidth  = 2
		labelWidth = 10
		columnGap  = "  "
		prefix     = " Legend: "
	)
	iconRows := [][]string{
		{"🏹", "🚰", "👁️", "🦛", "💧", "🐱"},
		{"🅾️", "💦", "🍨", "🦭", "🦸"},
		{"⚡", "🥩", "🏊", "🏛️", "💲", "Ⓜ️"},
	}
	itemRows := make([][]legendItem, len(iconRows))
	for r, icons := range iconRows {
		row := make([]legendItem, len(icons))
		for c, icon := range icons {
			row[c] = legendItem{icon: icon, label: protocolLabelForIcon(icon)}
		}
		itemRows[r] = row
	}

	columns := 0
	for _, itemRow := range itemRows {
		if len(itemRow) > columns {
			columns = len(itemRow)
		}
	}

	rows := make([]string, 0, len(itemRows))
	for _, itemRow := range itemRows {
		rowItems := make([]string, 0, columns)
		for _, item := range itemRow {
			rowItems = append(
				rowItems,
				fmt.Sprintf(
					"%s %s",
					padDisplayWidth(item.icon, iconWidth),
					padDisplayWidth(item.label, labelWidth),
				),
			)
		}
		rows = append(rows, strings.Join(rowItems, columnGap))
	}

	for i := range rows {
		if i == 0 {
			rows[i] = prefix + "[white]" + rows[i]
			continue
		}
		rows[i] = strings.Repeat(" ", len(prefix)) + rows[i]
	}
	return strings.Join(rows, "\n")
}

func setupUI() {
	headerText.SetText(fmt.Sprintln(" > txtop -", GetVersionString()))
	sortMu.RLock()
	sortBy := currentSortBy
	sortMu.RUnlock()
	footerText.SetText(updateFooterText(false, sortBy))
	legendText.SetText(buildLegendText())

	detailText := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true)
	detailText.SetBorder(true)

	txTable.SetSelectedFunc(func(row, _ int) {
		if row < 1 {
			return
		}
		currentTxsMu.RLock()
		defer currentTxsMu.RUnlock()
		if row-1 >= len(currentTxs) {
			return
		}
		tx := currentTxs[row-1]
		detailText.SetTitle(fmt.Sprintf(" tx %s ", tx.Hash))
		detailText.SetText(formatTxDetail(tx) + "\n [yellow](Esc to return to list)")
		detailText.ScrollToBeginning()
		pages.SwitchToPage("Detail")
	})
	detailText.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			pages.SwitchToPage("Main")
			app.SetFocus(txTable)
			return nil
		}
		return event
	})

	flex.SetDirection(tview.FlexRow).
		AddItem(headerText, 1, 1, false).
		AddItem(summaryText, 1, 0, false).
		AddItem(txTable, 0, 6, true).
		AddItem(legendText, 3, 0, false).
		AddItem(footerText, 2, 0, false)

	flex.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Rune() == 'p':
			newPaused := togglePaused()
			sortMu.RLock()
			sortBy := currentSortBy
			sortMu.RUnlock()
			footerText.Clear()
			footerText.SetText(updateFooterText(newPaused, sortBy))
			if newPaused {
				// freeze + enable browsing
				txTable.SetSelectable(true, false)
				txTable.Select(1, 0)
				app.SetFocus(txTable)
			} else {
				// resume live: disable browsing, reset highlight
				txTable.SetSelectable(false, false)
				txTable.ScrollToBeginning()
			}
			return nil
		case event.Rune() == 's':
			sortMu.Lock()
			if currentSortBy == "size" {
				currentSortBy = "time"
			} else {
				currentSortBy = "size"
			}
			sortBy := currentSortBy
			sortMu.Unlock()
			footerText.Clear()
			footerText.SetText(updateFooterText(isPaused(), sortBy))
			if isPaused() {
				// re-sort the frozen snapshot in place, no network
				currentTxsMu.RLock()
				snapshot := currentTxs
				currentTxsMu.RUnlock()
				applyTxs(sortAndCapTxs(snapshot, sortBy, int(GetConfig().App.MaxDisplayedTransactions)))
			}
			return nil
		case event.Rune() == 'q' || event.Key() == tcell.KeyEscape:
			app.Stop()
		}
		return event
	})
	pages.AddPage("Main", flex, true, true)
	pages.AddPage("Detail", detailText, true, false)

	// Re-shorten hashes to the new width whenever the terminal width changes
	// (first draw and resizes). The screen width is the table width (full-width
	// root Flex) and is known here even before the first layout, unlike the
	// table's inner rect. Runs on the draw goroutine.
	app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		width, _ := screen.Size()
		if width == lastTableWidth {
			return false
		}
		lastTableWidth = width
		selectedRow, selectedCol := txTable.GetSelection()
		currentTxsMu.RLock()
		txs := currentTxs
		currentTxsMu.RUnlock()
		populateTable(txTable, txs)
		if selectedRow < txTable.GetRowCount() {
			txTable.Select(selectedRow, selectedCol)
		}
		return false
	})
}

func startRefreshLoop(cfg *Config, errorChan chan error) {
	go func(cfg *Config) {
		interval := time.Second * time.Duration(cfg.App.Refresh)
		for {
			if !isPaused() {
				oConn, err := GetConnection(errorChan)
				if err != nil {
					slog.Error("Failed to refresh connection", "error", err)
					app.QueueUpdateDraw(func() {
						summaryText.SetText(fmt.Sprintf(" [red]failed to connect to node: %s", err))
					})
				} else {
					sizes := GetSizes(oConn)
					txs, buildErr := buildTxs(oConn)
					oConn.Close()
					app.QueueUpdateDraw(func() {
						// Re-check pause: the user may have paused mid-fetch; do
						// not clobber the snapshot they are now browsing.
						if isPaused() {
							return
						}
						if buildErr != nil {
							slog.Error("Failed to build transaction list", "error", buildErr)
							summaryText.SetText(fmt.Sprintf(" [red]ERROR: %s", buildErr))
							return
						}
						summaryText.SetText(sizes)
						applyTxs(txs)
					})
				}
			}
			time.Sleep(interval)
		}
	}(cfg)
}

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Print(logBuffer.String())
		fmt.Printf("failed to load config: %s", err)
		os.Exit(1)
	}
	slog.SetDefault(
		slog.New(slog.NewTextHandler(logBuffer, &slog.HandlerOptions{})),
	)
	if cfg.App.LogBufferSize > 0 {
		logBuffer.maxLines = int(cfg.App.LogBufferSize)
	}
	errorChan := make(chan error)
	go func() {
		for {
			err := <-errorChan
			slog.Error("Async error", "error", err)
			app.QueueUpdateDraw(func() {
				summaryText.SetText(fmt.Sprintf(" [red]ERROR: async: %s", err))
			})
		}
	}()
	initializeData(errorChan)
	setupUI()
	startRefreshLoop(cfg, errorChan)
	defer func() { fmt.Print(logBuffer.String()) }()
	if err := app.SetRoot(pages, true).EnableMouse(false).Run(); err != nil {
		panic(err)
	}
}

// Tx holds the full per-transaction information extracted from the mempool.
type Tx struct {
	Hash     string
	Icon     string
	Size     int
	Fee      uint64 // lovelace
	TotalOut uint64 // sum of output amounts, lovelace
	IsValid  bool

	// inputs / outputs (per-UTxO)
	Inputs  []string   // "txid#index" per consumed input
	Outputs []TxOutput // per output: address, amount, asset count

	// protocol / metadata
	ProtocolLabel  string
	MatchedAddress string
	MetadataLabels []uint64
	Cip20Message   []string

	// certs / governance / mint
	CertTypes     []string
	NumVotes      int
	NumProposals  int
	Withdrawals   uint64 // total lovelace withdrawn
	MintedAssets  int
	TTL           uint64
	ValidityStart uint64
}

// TxOutput is one transaction output for the detail view.
type TxOutput struct {
	Address   string
	Amount    uint64 // lovelace
	NumAssets int
}

// sortAndCapTxs returns a new slice sorted per sortBy ("size" = descending by
// size; anything else = original insertion order) and truncated to max entries;
// pass max < 0 to return all entries. The input slice is not mutated.
func sortAndCapTxs(txs []Tx, sortBy string, max int) []Tx {
	out := make([]Tx, len(txs))
	copy(out, txs)
	if sortBy == "size" {
		sort.Slice(out, func(i, j int) bool {
			return out[i].Size > out[j].Size
		})
	}
	if max >= 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// protocolLabels maps a detected emoji icon to the human-readable protocol name.
// It is the single source of truth for protocol names; buildLegendText sources
// its labels from this map (Task 10).
var protocolLabels = map[string]string{
	"🏹":  "Dexhunter",
	"🚰":  "DripDropz",
	"👁️": "Indigo",
	"🦛":  "JPGstore",
	"💧":  "Liqwid",
	"🐱":  "Minswap",
	"🅾️": "Optim",
	"💦":  "Splash",
	"🍨":  "Sundae",
	"🦭":  "SealVM",
	"🦸":  "Wingriders",
	"⚡":  "Strike",
	"🥩":  "Staking",
	"🏊":  "SPOs",
	"🏛️": "Governance",
	"💲":  "AdaHandle",
	"Ⓜ️": "Materios",
	"🕺":  "Silk Toad",
	"🔵":  "VyFinance",
}

// protocolLabelForIcon returns the protocol name for a detected icon, or "" if
// the icon is empty or unknown. Trailing spaces (e.g. the Indigo "👁️ " icon,
// padded for terminal width) are trimmed before lookup.
func protocolLabelForIcon(icon string) string {
	return protocolLabels[strings.TrimSpace(icon)]
}

func isMetaInt(m lcommon.TransactionMetadatum, value int64) bool {
	metaInt, ok := m.(lcommon.MetaInt)
	return ok && metaInt.Value != nil && metaInt.Value.Int64() == value
}

func isMetaText(m lcommon.TransactionMetadatum, value string) bool {
	metaText, ok := m.(lcommon.MetaText)
	return ok && metaText.Value == value
}

func isMateriosMetadata(m lcommon.TransactionMetadatum) bool {
	metaMap, ok := m.(lcommon.MetaMap)
	if !ok {
		return false
	}
	for _, pair := range metaMap.Pairs {
		if isMetaInt(pair.Key, 8746) && hasMateriosMarker(pair.Value) {
			return true
		}
	}
	return false
}

// metadataLabels returns the top-level metadata label numbers present (e.g. 674,
// 8746). Cardano transaction metadata is a map keyed by integer labels; this
// reads those keys. Returns nil if there is no metadata map.
func metadataLabels(md lcommon.TransactionMetadatum) []uint64 {
	metaMap, ok := md.(lcommon.MetaMap)
	if !ok {
		return nil
	}
	var labels []uint64
	for _, pair := range metaMap.Pairs {
		if k, ok := pair.Key.(lcommon.MetaInt); ok && k.Value != nil && k.Value.IsUint64() {
			labels = append(labels, k.Value.Uint64())
		}
	}
	return labels
}

// iconFromOutputAddress returns the protocol icon for a known script/payment
// address, or "" if unknown.
func iconFromOutputAddress(addr string) string {
	switch addr {
	// Dripdropz
	case "addr1v8pr9mwnqarw808gtllvmlxvk70hnszrukjeqfstr9t9g5crud8c4":
		return "🚰"
	// DexHunter
	case "addr1w9hvftxrlw74wzk6vf0jfyp8wl8vt4arf8aq70rm4paselc46ptfq",
		"addr1wxtlzuefvjg9k0r36ysk895gzjk97wl8027ffqc2snfje7gkzkf3h":
		return "🏹"
	// Indigo
	case "addr1w80ptp0qgmcklhmeweesqgeurtlma8fsxsr9dt8au30fzss0czhl9",
		"addr1w92w34pys9h4h02zxdfsp8lhcvdd5t9aaln9z96szsgh73scty4aj",
		"addr1w8q673nyx6vtcules4aqess7e9yuu6geja95xhg90hzy3wqpsjzzz",
		"addr1wxj88juwkzmpcqacd9hua2cur2yl50kgx3tjs588c2470qc2ftfae":
		return "👁️ " // space because it's only 1 char wide
	// JPG
	case "addr1zxgx3far7qygq0k6epa0zcvcvrevmn0ypsnfsue94nsn3tvpw288a4x0xf8pxgcntelxmyclq83s0ykeehchz2wtspks905plm":
		return "🦛"
	// Liqwid
	case "addr1wx6htk5hfmr4dw32lhxdcp7t6xpe4jhs5fxylq90mqwnldsvr87c6",
		"addr1wyn2aflq8ff7xaxpmqk9vz53ks28hz256tkyaj739rsvrrq3u5ft3",
		"addr1w8arvq7j9qlrmt0wpdvpp7h4jr4fmfk8l653p9t907v2nsss7w7r4",
		"addr1w8gx94twjy68u37eart4saezuw2tmm23rf5ne59sn59gu9stp0dcy",
		"addr1wxmpvupcrarexj5lp2k9dwsxwueue2l8rzcamrtdpdrfqvs6jk8k9",
		"addr1wxxjtzuaprw2kulnzedpzagage95vptzvsy3c9ufmfvgwfsj8cv4s",
		"addr1w9afj34vc68qdm7heuz7esmr8sj76wpa45t7dh3ag8xpplgml3zuk":
		return "💧"
	// Minswap
	case "addr1wxt2d5z2cxpaxj0jwlcqmyav08lpwrwwxacna2dyj0melqgugxgdz",
		"addr1w9qlcn8s6lngj9p533vqx2dvm2nrtyv3j7p2hqy65zvyn0ck0gj25",
		"addr1wyx22z2s4kasd3w976pnjf9xdty88epjqfvgkmfnscpd0rg3z8y6v",
		"addr1wxn9efv2f6w82hagxqtn62ju4m293tqvw0uhmdl64ch8uwc0h43gt",
		"addr1zxn9efv2f6w82hagxqtn62ju4m293tqvw0uhmdl64ch8uw6j2c79gy9l76sdg0xwhd7r0c0kna0tycz4y5s6mlenh8pq6s3z70",
		"addr1z8snz7c4974vzdpxu65ruphl3zjdvtxw8strf2c2tmqnxzf7l6s8x2a8s6ql8yxyxe7ydjjlu8xux7j9ymuj2njwqs6qa9r65k",
		"addr1w9eu87z6ywets8talp9fv94kv6c7rjx9lnllv7pan39p53gkjg05e",
		"addr1wxvd7wcq59gqljmhm2s9yp2slvygljfr8xtc3wykx7uaukgt8lqh6",
		"addr1wxdct40gvyv525zlq79wahkuhmgadjs3q5lkrclemxvlu3qyda7l2",
		"addr1wxwl25gyxf4ryexcq02yakr389vp68y39cnt4rnnhsu9utcjfhaef",
		"addr1wx5p836jswavyfd3nuwscz53fkyu43kmn2wwje73qhf48mqw02kqx",
		"addr1wxc45xspppp73takl93mq029905ptdfnmtgv6g7cr8pdyqgvks3s8",
		"addr1wy3fscaws62d59k6qqhg3xsarx7vstzczgjmdhx2jh7knksj7w3y7":
		return "🐱"
	// Optim
	case "addr1zywj8y96k38kye7qz329dhp0t782ykr0ev92mtz4yhv6gph8ucsr8rpyzewcf9jyf7gmjj052dednasdeznehw7aqc7q0z7vn2":
		return "🅾️"
	// Silk Toad
	case "addr1w9d85mfr73mk8pr5erd46d7e7whcah2tzcyqd5rr4hv2amg9sxgl8",
		"addr1xxj62lufz8se8rlr7r79ap7rwa845f4gnvm6qls85kuxpw9954lcjy0pjw878u8ut6ruxa60tgn23xeh5plq0fdcvzuq7kuswe":
		return "🕺"
	// Splash
	case "addr1w8d70g7c58vznyye9guwagdza74x36f3uff0eyk2zwpcpxcn3whlz",
		"addr1wysz2335xlh96e8gnq22vm88lxxtrp9xdt595taml46szps9nreda",
		"addr1w9ryamhgnuz6lau86sqytte2gz5rlktv2yce05e0h3207qssa8euj",
		"addr1wxvmst9ejnwz4azvzt94mt666f6zz93zsqzx0t6mmrpjx5scaz63e",
		"addr1wxu29wa80fd4ptpfwqe20vpxrum45f57ud3r6egh9vuyhfc2a3jhj",
		"addr1w95q755yrsr0xt8vmn007tpqee4hps49yjdef5dzknhl99qntsmh0",
		"addr1wxrl2p9s0tweu8t54cgz75at070ly3tda6yh5s7cufanfzc52gv39",
		"addr1wymhr2l96gm22xkwz0rn3zz79xz9l400nm5sa580kssdyagr5z7wq",
		"addr1w9884ny4sd83hpk9f6deuw8nc8mjlpy22t4ejnd9p40cyhc74rg6y":
		return "💦"
	// Sundae
	case "addr1wxaptpmxcxawvr3pzlhgnpmzz3ql43n2tc8mn3av5kx0yzs09tqh8",
		"addr1w9qzpelu9hn45pefc0xr4ac4kdxeswq7pndul2vuj59u8tqaxdznu",
		"addr1w9jx45flh83z6wuqypyash54mszwmdj8r64fydafxtfc6jgrw4rm3",
		"addr1x8srqftqemf0mjlukfszd97ljuxdp44r372txfcr75wrz26rnxqnmtv3hdu2t6chcfhl2zzjh36a87nmd6dwsu3jenqsslnz7e",
		"addr1z8ax5k9mutg07p2ngscu3chsauktmstq92z9de938j8nqal9r9z8yaghysf05atjyv79t73lercjdqnejetxm307m49qdfqcxd",
		"addr1w96dh8r6yds9et9wfmnrtg267mnrck3ej9wysyj5a6uhtzchkcr5d",
		"addr1w82z6yrftsxz77el0ce2q4vuspcym2x0xgpgneurrwvasfge778fd",
		"addr1z9ejwku7yelajfalc9x0v57eqng48zkcs6fxp2mr30mn7hxhy3954pmhklwxjz05vsx0qt4yw4a9275eldyrkp0c0hlq4gdz8r",
		"addr1w8ax5k9mutg07p2ngscu3chsauktmstq92z9de938j8nqacprc9mw":
		return "🍨"
	// VyFinance
	case "addr1w8ll74xa05dkn69n3rmp93h8maphmms2408nt0nyruarzvqr9zf64",
		"addr1z976yepnveus5uddth7qd66kn6cuzd7tccjd39dfdayc7lnend0q3h5twed567pu236a0sf6vfgruxgpr4rkxryyx0zqa550y7":
		return "🔵"
	// Wingriders
	case "addr1wxr2a8htmzuhj39y2gq7ftkpxv98y2g67tg8zezthgq4jkg0a4ul4",
		"addr1wx0aglsd278m2wdea3lqzmw9acjcgsgs49n3942e3xc82xcu406l6",
		"addr1wypr0np3xatwhddulsnj3aaac65qg768zgs2xpd2xuaj0zscmvh0n",
		"addr1w8nvjzjeydcn4atcd93aac8allvrpjn7pjr2qsweukpnayghhwcpj",
		"addr1wy9z0v8mrkhtyll43fu6mnhu0p87tna48xt4p56496x9f7g940jft",
		"addr1w8z7qwzszt2lqy93m3atg2axx22yq5k7yvs9rmrvuwlawts2wzadz",
		"addr1wxvx34v0hlxzk9x0clv7as9hvhn7dlzwj5xfcf6g4n5uucg4tkd7w",
		"addr1w94x54jsrlexga6cajw37lf7ltssssvg6ah7jpslnkd58eq6vc878",
		"addr1wxftfzwxavgcdj8at4adrmashphwccdldcxj0vg35gf2qhgmavq67",
		"addr1w8r3ulqlcjp739a3kzlapt7xxgzv5x5gwq04tsm36ytc2rs0vx42u":
		return "🦸"
	// Strike Finance
	case "addr1zym7cc6d37vgh2g40ucmclczff4zmzudfql6pqk7vt2rh5g6409492020k6xml8uvwn34wrexagjh5fsk5xk96jyxk2qy3jvzp",
		"addr1wy2gch9ua0700a3dg423wxcwx4p886m4ny5u3aqs66sluqcly9uud",
		"addr1wy0qcxjytcv9fpf80szy6a7jkx40sz4y3x3g5nq7z8kgmuqth60kl",
		"addr1z9yh4zcqs4gh78ysvh8nqp40fsnxg49nn3h6x25az9k8tms6409492020k6xml8uvwn34wrexagjh5fsk5xk96jyxk2qf3a7kj":
		return "⚡"
	// AdaHandle
	case "addr1w9kqg07fu06dlw47q8s8548ulz4ra23caqnh0vg0j3sct8qrsqrpc",
		"addr1w8vyuk74899edjmpzpzrjue0muclufezwh62xg6uzpduxvsszgvr0",
		"addr1w9wdrrvetdua8k4365vslpxh8uukvcgyyqj54sphnwaluxs753n9e",
		"addr1w9grm99rr5lxcdvjxguk9gnryulz2478f8e4udh95ttfqygu5z8er":
		return "💲"
	}
	return ""
}

// iconFromStakeAddress returns the protocol icon for a known stake address, or "".
func iconFromStakeAddress(addr string) string {
	switch addr {
	// Seal's Vending Machine
	case "stake1u8ffzkegp8h48mare3g3ntf3xmjce3jqptsdtj38ee3yh3c9t4uum":
		return "🦭"
	}
	return ""
}

// iconFromCertificates returns the icon for the first recognised certificate
// type, or "". Order of precedence matches the prior loop: the first matching
// certificate wins.
func iconFromCertificates(certs []lcommon.Certificate) string {
	for _, certificate := range certs {
		switch certificate.(type) {
		case *lcommon.StakeRegistrationCertificate,
			*lcommon.StakeDeregistrationCertificate,
			*lcommon.StakeDelegationCertificate:
			return "🥩"
		case *lcommon.PoolRegistrationCertificate,
			*lcommon.PoolRetirementCertificate:
			return "🏊"
		case *lcommon.VoteDelegationCertificate,
			*lcommon.StakeVoteDelegationCertificate,
			*lcommon.VoteRegistrationDelegationCertificate,
			*lcommon.StakeVoteRegistrationDelegationCertificate,
			*lcommon.AuthCommitteeHotCertificate,
			*lcommon.ResignCommitteeColdCertificate,
			*lcommon.RegistrationDrepCertificate,
			*lcommon.DeregistrationDrepCertificate,
			*lcommon.UpdateDrepCertificate:
			return "🏛️"
		}
	}
	return ""
}

// certTypeNames returns a short type name for each certificate, e.g.
// "StakeDelegation" for *lcommon.StakeDelegationCertificate. Used by the detail
// page. Unknown types are reported as "Unknown".
func certTypeNames(certs []lcommon.Certificate) []string {
	names := make([]string, 0, len(certs))
	for _, c := range certs {
		switch c.(type) {
		case *lcommon.StakeRegistrationCertificate:
			names = append(names, "StakeRegistration")
		case *lcommon.StakeDeregistrationCertificate:
			names = append(names, "StakeDeregistration")
		case *lcommon.StakeDelegationCertificate:
			names = append(names, "StakeDelegation")
		case *lcommon.PoolRegistrationCertificate:
			names = append(names, "PoolRegistration")
		case *lcommon.PoolRetirementCertificate:
			names = append(names, "PoolRetirement")
		case *lcommon.VoteDelegationCertificate:
			names = append(names, "VoteDelegation")
		case *lcommon.StakeVoteDelegationCertificate:
			names = append(names, "StakeVoteDelegation")
		case *lcommon.VoteRegistrationDelegationCertificate:
			names = append(names, "VoteRegistrationDelegation")
		case *lcommon.StakeVoteRegistrationDelegationCertificate:
			names = append(names, "StakeVoteRegistrationDelegation")
		case *lcommon.AuthCommitteeHotCertificate:
			names = append(names, "AuthCommitteeHot")
		case *lcommon.ResignCommitteeColdCertificate:
			names = append(names, "ResignCommitteeCold")
		case *lcommon.RegistrationDrepCertificate:
			names = append(names, "RegistrationDrep")
		case *lcommon.DeregistrationDrepCertificate:
			names = append(names, "DeregistrationDrep")
		case *lcommon.UpdateDrepCertificate:
			names = append(names, "UpdateDrep")
		default:
			names = append(names, "Unknown")
		}
	}
	return names
}

// iconFromCip20Messages maps the first CIP-20 (label 674) message line to a
// protocol icon, or "" if there is no match. Only the first line is considered,
// matching the prior behaviour.
func iconFromCip20Messages(msgs []string) string {
	if len(msgs) == 0 {
		return ""
	}
	switch msgs[0] {
	case "Dexhunter Trade":
		return "🏹"
	case "Minswap: Deposit Order",
		"Minswap: Cancel Order",
		"Minswap: Create Pool",
		"Minswap: Launch Bowl Redemption",
		"Minswap: LBE Deposit ADA",
		"Minswap: Liquidity Migration",
		"Minswap: MasterChef",
		"Minswap: Order Executed",
		"Minswap: Swap Exact In Order",
		"Minswap: Swap Exact In Limit Order",
		"Minswap: Swap Exact Out Order",
		"Minswap: Swap Exact Out Limit Order",
		"Minswap: V2 Harvest reward",
		"Minswap: V2 Stake liquidity",
		"Minswap: Withdraw Order",
		"Minswap: Zap Order":
		return "🐱"
	case "SSP: Swap Request":
		return "🍨"
	}
	return ""
}

// newTxFromLedger aggregates a parsed ledger transaction into a Tx. size is the
// length of the raw CBOR. Detection precedence (metadata → output address →
// stake address → certificate, later overrides earlier) matches the prior code.
func newTxFromLedger(ltx ledger.Transaction, size int) Tx {
	tx := Tx{
		Hash:    ltx.Hash().String(),
		Size:    size,
		IsValid: ltx.IsValid(),
	}
	if fee := ltx.Fee(); fee != nil {
		tx.Fee = fee.Uint64()
	}

	// CIP-20 messages + metadata labels
	md := ltx.Metadata()
	if md != nil {
		tx.MetadataLabels = metadataLabels(md)
		tx.Cip20Message = cip20Messages(md)
	}

	// icon detection (precedence preserved)
	icon := ""
	matchedAddr := ""
	if md != nil {
		if isMateriosMetadata(md) {
			icon = "Ⓜ️"
		}
		if i := iconFromCip20Messages(tx.Cip20Message); i != "" {
			icon = i
		}
	}
	for _, out := range ltx.Outputs() {
		addr := out.Address().String()
		if i := iconFromOutputAddress(addr); i != "" {
			icon = i
			matchedAddr = addr
		}
	}
	for _, out := range ltx.Outputs() {
		if sa := out.Address().StakeAddress(); sa != nil {
			if i := iconFromStakeAddress(sa.String()); i != "" {
				icon = i
				matchedAddr = sa.String()
			}
		}
	}
	if i := iconFromCertificates(ltx.Certificates()); i != "" {
		icon = i
	}
	tx.Icon = icon
	tx.MatchedAddress = matchedAddr
	tx.ProtocolLabel = protocolLabelForIcon(icon)

	// inputs
	for _, in := range ltx.Inputs() {
		tx.Inputs = append(tx.Inputs, in.String())
	}

	// outputs + total out
	total := big.NewInt(0)
	for _, out := range ltx.Outputs() {
		amt := out.Amount()
		if amt != nil {
			total.Add(total, amt)
		}
		numAssets := 0
		if assets := out.Assets(); assets != nil {
			for _, policy := range assets.Policies() {
				numAssets += len(assets.Assets(policy))
			}
		}
		o := TxOutput{Address: out.Address().String(), NumAssets: numAssets}
		if amt != nil {
			o.Amount = amt.Uint64()
		}
		tx.Outputs = append(tx.Outputs, o)
	}
	tx.TotalOut = total.Uint64()

	// certs / governance / mint
	tx.CertTypes = certTypeNames(ltx.Certificates())
	// NumVotes counts the number of voters (outer map keys) in VotingProcedures.
	for range ltx.VotingProcedures() {
		tx.NumVotes++
	}
	tx.NumProposals = len(ltx.ProposalProcedures())
	withdraw := big.NewInt(0)
	for _, amt := range ltx.Withdrawals() {
		if amt != nil {
			withdraw.Add(withdraw, amt)
		}
	}
	tx.Withdrawals = withdraw.Uint64()
	if mint := ltx.AssetMint(); mint != nil {
		for _, policy := range mint.Policies() {
			tx.MintedAssets += len(mint.Assets(policy))
		}
	}
	tx.TTL = ltx.TTL()
	tx.ValidityStart = ltx.ValidityIntervalStart()

	return tx
}

// cip20Messages returns the CIP-20 (label 674) message lines, or nil.
func cip20Messages(md lcommon.TransactionMetadatum) []string {
	var msg models.Cip20Metadata
	if err := cbor.Unmarshal(md.Cbor(), &msg); err != nil {
		return nil
	}
	return msg.Num674.Msg
}

// extractTx parses raw transaction CBOR into a Tx. It returns an error for
// malformed input so the caller can skip the transaction.
func extractTx(raw []byte) (Tx, error) {
	txType, err := ledger.DetermineTransactionType(raw)
	if err != nil {
		return Tx{}, fmt.Errorf("determine tx type: %w", err)
	}
	ltx, err := ledger.NewTransactionFromCbor(txType, raw)
	if err != nil {
		return Tx{}, fmt.Errorf("decode tx: %w", err)
	}
	return newTxFromLedger(ltx, len(raw)), nil
}

func hasMateriosMarker(m lcommon.TransactionMetadatum) bool {
	switch meta := m.(type) {
	case lcommon.MetaList:
		for _, item := range meta.Items {
			if hasMateriosMarker(item) {
				return true
			}
		}
	case lcommon.MetaMap:
		hasProtocolKey := false
		hasMateriosValue := false
		for _, pair := range meta.Pairs {
			switch {
			case isMetaText(pair.Key, "k") && isMetaText(pair.Value, "p"):
				hasProtocolKey = true
			case isMetaText(pair.Key, "v") && isMetaText(pair.Value, "materios"):
				hasMateriosValue = true
			default:
				if hasMateriosMarker(pair.Value) {
					return true
				}
			}
		}
		return hasProtocolKey && hasMateriosValue
	}
	return false
}
