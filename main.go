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
	"os"
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

var text = tview.NewTextView().
	SetDynamicColors(true).
	SetChangedFunc(func() { app.Draw() })

var (
	paused        int32 = 0 // 0 = false, 1 = true (atomic)
	content       string
	sortMu        sync.RWMutex
	currentSortBy string = "size"
)

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
	App  AppConfig
	Node NodeConfig
}

type AppConfig struct {
	Network                  string `envconfig:"NETWORK"`
	Refresh                  uint32 `envconfig:"REFRESH"`
	Retries                  uint32 `envconfig:"RETRIES"`
	LogBufferSize            uint32 `envconfig:"LOG_BUFFER_SIZE"`
	MaxBackoff               uint32 `envconfig:"MAX_BACKOFF"`
	MaxDisplayedTransactions uint32 `envconfig:"MAX_DISPLAYED_TRANSACTIONS"`
	SortBy                   string `envconfig:"SORT_BY"`
}

type NodeConfig struct {
	Network      string `envconfig:"CARDANO_NETWORK"`
	NetworkMagic uint32 `envconfig:"CARDANO_NODE_NETWORK_MAGIC"`
	SocketPath   string `envconfig:"CARDANO_NODE_SOCKET_PATH"`
	Address      string `envconfig:"CARDANO_NODE_SOCKET_TCP_HOST"`
	Port         uint32 `envconfig:"CARDANO_NODE_SOCKET_TCP_PORT"`
}

func LoadConfig() (*Config, error) {
	err := envconfig.Process("txtop", globalConfig)
	if err != nil {
		return nil, fmt.Errorf("error processing environment: %w", err)
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
	oConn, err := ouroboros.NewConnection(
		ouroboros.WithNetworkMagic(uint32(cfg.Node.NetworkMagic)),
		ouroboros.WithErrorChan(errorChan),
		ouroboros.WithNodeToNode(false),
		ouroboros.WithKeepAlive(true),
	)
	if err != nil {
		return nil, fmt.Errorf("failure creating ouroboros connection: %w", err)
	}
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
				continue
			}
		} else if cfg.Node.SocketPath != "" {
			err = oConn.Dial("unix", cfg.Node.SocketPath)
			if err != nil {
				slog.Warn("Failed to connect via UNIX socket", "path", cfg.Node.SocketPath, "error", err, "attempt", attempt)
				lastErr = err
				continue
			}
		} else {
			return nil, errors.New("specify either the UNIX socket path or the address/port")
		}
		slog.Info("Successfully connected to node")
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

func GetTransactions(oConn *ouroboros.Connection) string {
	if oConn == nil {
		return ""
	}
	cfg := GetConfig()
	maxTx := int(cfg.App.MaxDisplayedTransactions)
	type txInfo struct {
		size int
		icon string
		hash string
	}
	var txs []txInfo
	for {
		txRawBytes, err := oConn.LocalTxMonitor().Client.NextTx()
		if err != nil {
			return fmt.Sprintf(" [red]ERROR: NextTx: %s", err)
		}
		if txRawBytes == nil {
			break
		}
		size := len(txRawBytes)
		txType, err := ledger.DetermineTransactionType(txRawBytes)
		if err != nil {
			return fmt.Sprintf(" [red]ERROR: TxType: %s", err)
		}
		tx, err := ledger.NewTransactionFromCbor(txType, txRawBytes)
		if err != nil {
			return fmt.Sprintf(" [red]ERROR: Tx: %s", err)
		}
		var icon string
		// Check if Tx has metadata and compare against our list
		if tx.Metadata() != nil {
			mdCbor := tx.Metadata().Cbor()
			var msgMetadata models.Cip20Metadata
			_ = cbor.Unmarshal(mdCbor, &msgMetadata)
			if msgMetadata.Num674.Msg != nil {
				// Only check first line
				switch msgMetadata.Num674.Msg[0] {
				// Dexhunter
				case "Dexhunter Trade":
					icon = "🏹"
				// Minswap
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
					icon = "🐱"
				// Sundae
				case "SSP: Swap Request":
					icon = "🍨"
				}
			}
		}
		// Check if output includes known script addresses
		for _, output := range tx.Outputs() {
			switch output.Address().String() {
			// Axo
			case "addr1w8ytzffgwpf94dy20kgw72gn9ujjhqu3md34vhggenkakeszhjpl3",
				"addr1z8ytzffgwpf94dy20kgw72gn9ujjhqu3md34vhggenkakejv7ncp3yppt0gcr50u60y43x32fgadhnl35u9hfqyql2pqr3p0j4":
				icon = "❌"
			// Dripdropz
			case "addr1v8pr9mwnqarw808gtllvmlxvk70hnszrukjeqfstr9t9g5crud8c4":
				icon = "🚰"
			// Indigo
			case "addr1w80ptp0qgmcklhmeweesqgeurtlma8fsxsr9dt8au30fzss0czhl9",
				"addr1w92w34pys9h4h02zxdfsp8lhcvdd5t9aaln9z96szsgh73scty4aj",
				"addr1w8q673nyx6vtcules4aqess7e9yuu6geja95xhg90hzy3wqpsjzzz",
				"addr1wxj88juwkzmpcqacd9hua2cur2yl50kgx3tjs588c2470qc2ftfae":
				icon = "👁️ " // space because it's only 1 char wide
			// JPG
			case "addr1zxgx3far7qygq0k6epa0zcvcvrevmn0ypsnfsue94nsn3tvpw288a4x0xf8pxgcntelxmyclq83s0ykeehchz2wtspks905plm":
				icon = "🦛"
			// Liqwid
			case "addr1wx6htk5hfmr4dw32lhxdcp7t6xpe4jhs5fxylq90mqwnldsvr87c6",
				"addr1wyn2aflq8ff7xaxpmqk9vz53ks28hz256tkyaj739rsvrrq3u5ft3",
				"addr1w8arvq7j9qlrmt0wpdvpp7h4jr4fmfk8l653p9t907v2nsss7w7r4":
				icon = "💧"
			// Minswap
			case "addr1z84q0denmyep98ph3tmzwsmw0j7zau9ljmsqx6a4rvaau66j2c79gy9l76sdg0xwhd7r0c0kna0tycz4y5s6mlenh8pq777e2a":
				icon = "🐱"
			// Optim
			case "addr1zywj8y96k38kye7qz329dhp0t782ykr0ev92mtz4yhv6gph8ucsr8rpyzewcf9jyf7gmjj052dednasdeznehw7aqc7q0z7vn2":
				icon = "🅾️"
			// Silk Toad
			case "addr1w9d85mfr73mk8pr5erd46d7e7whcah2tzcyqd5rr4hv2amg9sxgl8",
				"addr1xxj62lufz8se8rlr7r79ap7rwa845f4gnvm6qls85kuxpw9954lcjy0pjw878u8ut6ruxa60tgn23xeh5plq0fdcvzuq7kuswe":
				icon = "🕺"
			// Spectrum
			case "addr1wyr4uz0tp75fu8wrg6gm83t20aphuc9vt6n8kvu09ctkugqpsrmeh",
				"addr1x94ec3t25egvhqy2n265xfhq882jxhkknurfe9ny4rl9k6dj764lvrxdayh2ux30fl0ktuh27csgmpevdu89jlxppvrst84slu",
				"addr1x8nz307k3sr60gu0e47cmajssy4fmld7u493a4xztjrll0aj764lvrxdayh2ux30fl0ktuh27csgmpevdu89jlxppvrswgxsta",
				"addr1wynp362vmvr8jtc946d3a3utqgclfdl5y9d3kn849e359hsskr20n":
				icon = "🌈"
			// Sundae
			case "addr1wxaptpmxcxawvr3pzlhgnpmzz3ql43n2tc8mn3av5kx0yzs09tqh8",
				"addr1w9qzpelu9hn45pefc0xr4ac4kdxeswq7pndul2vuj59u8tqaxdznu",
				"addr1w9jx45flh83z6wuqypyash54mszwmdj8r64fydafxtfc6jgrw4rm3",
				"addr1x8srqftqemf0mjlukfszd97ljuxdp44r372txfcr75wrz26rnxqnmtv3hdu2t6chcfhl2zzjh36a87nmd6dwsu3jenqsslnz7e",
				"addr1z8ax5k9mutg07p2ngscu3chsauktmstq92z9de938j8nqal9r9z8yaghysf05atjyv79t73lercjdqnejetxm307m49qdfqcxd":
				icon = "🍨"
			// VyFinance
			case "addr1w8ll74xa05dkn69n3rmp93h8maphmms2408nt0nyruarzvqr9zf64",
				"addr1z976yepnveus5uddth7qd66kn6cuzd7tccjd39dfdayc7lnend0q3h5twed567pu236a0sf6vfgruxgpr4rkxryyx0zqa550y7":
				icon = "🔵"
			// Wingriders
			case "addr1wxr2a8htmzuhj39y2gq7ftkpxv98y2g67tg8zezthgq4jkg0a4ul4":
				icon = "🦸"
			}
		}
		// Check if output includes known stake addresses
		for _, output := range tx.Outputs() {
			if output.Address().StakeAddress() != nil {
				switch output.Address().StakeAddress().String() {
				// Seal's Vending Machine
				case "stake1u8ffzkegp8h48mare3g3ntf3xmjce3jqptsdtj38ee3yh3c9t4uum":
					icon = "🦭"
				}
			}
		}

		// Check if Tx has certificates and compare against known types
		if tx.Certificates() != nil {
			for _, certificate := range tx.Certificates() {
				eject := false
				switch certificate.(type) {
				case *lcommon.StakeRegistrationCertificate,
					*lcommon.StakeDeregistrationCertificate,
					*lcommon.StakeDelegationCertificate:
					icon = "🥩"
					eject = true
				case *lcommon.PoolRegistrationCertificate,
					*lcommon.PoolRetirementCertificate:
					icon = "🏊"
					eject = true
				case *lcommon.VoteDelegationCertificate,
					*lcommon.StakeVoteDelegationCertificate,
					*lcommon.VoteRegistrationDelegationCertificate,
					*lcommon.StakeVoteRegistrationDelegationCertificate,
					*lcommon.AuthCommitteeHotCertificate,
					*lcommon.ResignCommitteeColdCertificate,
					*lcommon.RegistrationDrepCertificate,
					*lcommon.DeregistrationDrepCertificate,
					*lcommon.UpdateDrepCertificate:
					icon = "🏛️"
					eject = true
				}
				if eject {
					break
				}
			}
		}

		txs = append(txs, txInfo{size, icon, tx.Hash().String()})
	}
	// sort txs by size desc if configured
	sortMu.RLock()
	sortBy := currentSortBy
	sortMu.RUnlock()
	if sortBy == "size" {
		sort.Slice(txs, func(i, j int) bool {
			return txs[i].size > txs[j].size
		})
	}
	// take top maxTx
	if len(txs) > maxTx {
		txs = txs[:maxTx]
	}
	// build sb
	var sb strings.Builder
	fmt.Fprintf(&sb, " [white]%-10s %-10s %s\n", "Size:", "Icon:", "TxHash:")
	for _, t := range txs {
		spaces := "10"
		if t.icon != "" {
			spaces = "9"
		}
		fmt.Fprintf(
			&sb,
			" [white]%-10d %-"+spaces+"s [blue]%s[white]\n",
			t.size,
			t.icon,
			t.hash,
		)
	}
	return sb.String()
}

func initializeData(errorChan chan error) {
	oConn, err := GetConnection(errorChan)
	if err != nil {
		slog.Error("Failed to initialize connection", "error", err)
		text.SetText(fmt.Sprintf(" [red]failed to connect to node: %s", err))
	} else {
		text.SetText(fmt.Sprintf("%s\n%s",
			GetSizes(oConn),
			GetTransactions(oConn),
		))
	}
}

func updateFooterText(paused bool, sortBy string) string {
	pausedText := ""
	if paused {
		pausedText = " [yellow](paused)"
	}
	return fmt.Sprintf(
		" [yellow](esc/q)[white] Quit | [yellow](p)[white] Pause%s | [yellow](s)[white] Sort: %s",
		pausedText,
		sortBy,
	)
}

func setupUI() {
	headerText.SetText(fmt.Sprintln(" > txtop -", GetVersionString()))
	sortMu.RLock()
	sortBy := currentSortBy
	sortMu.RUnlock()
	footerText.SetText(updateFooterText(false, sortBy))
	legendText.SetText(
		fmt.Sprintf(" Legend: [white]%s\n %s\n %s",
			fmt.Sprintf("%12s %12s %12s %12s %12s %12s",
				"🏹 Dexhunter",
				"🚰 DripDropz",
				"👁️ Indigo",
				"🦛 JPGstore",
				"💧 Liqwid",
				"🐱 Minswap",
			),
			// Text formatting the wrong way for the win
			fmt.Sprintf("%17s %15s %12s %10s %18s",
				"🅾️ Optim",
				"🌈 Spectrum",
				"🍨 Sundae",
				"🦭 SealVM",
				"🦸 Wingriders",
			),
			fmt.Sprintf("%18s %9s %12s",
				"🥩 Staking",
				"🏊 SPOs",
				"🏛️ Governance",
			),
		),
	)
	flex.SetDirection(tview.FlexRow).
		AddItem(headerText,
			1,
			1,
			false).
		AddItem(text,
			0,
			6,
			true).
		AddItem(legendText,
			3,
			0,
			false).
		AddItem(footerText,
			2,
			0,
			false)
	flex.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Rune() == 112 { // p
			newPaused := togglePaused()
			footerText.Clear()
			sortMu.RLock()
			sortBy := currentSortBy
			sortMu.RUnlock()
			footerText.SetText(updateFooterText(newPaused, sortBy))
			if newPaused {
				return event
			}
		}
		if event.Rune() == 115 { // s
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
		}
		if event.Rune() == 113 || event.Key() == tcell.KeyEscape { // q
			app.Stop()
		}
		return event
	})
	pages.AddPage("Main", flex, true, true)
}

func startRefreshLoop(cfg *Config, errorChan chan error) {
	go func(cfg *Config) {
		for {
			if !isPaused() {
				oConn, err := GetConnection(errorChan)
				if err != nil {
					slog.Error("Failed to refresh connection", "error", err)
					text.Clear()
					text.SetText(fmt.Sprintf(" [red]failed to connect to node: %s", err))
				} else {
					tmpText := fmt.Sprintf("%s\n%s",
						GetSizes(oConn),
						GetTransactions(oConn),
					)
					if tmpText != "" && tmpText != content {
						content = tmpText
						text.Clear()
						text.SetText(content)
					}
				}
			}
			time.Sleep(time.Second * time.Duration(cfg.App.Refresh))
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
	// text.SetBorder(true)
	errorChan := make(chan error)
	go func() {
		for {
			err := <-errorChan
			slog.Error("Async error", "error", err)
			text.SetText(fmt.Sprintf(" [red]ERROR: async: %s", err))
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
