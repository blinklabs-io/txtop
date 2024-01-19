// Copyright 2023 Blink Labs Software
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
	"os"
	"strings"
	"time"

	"github.com/blinklabs-io/cardano-models"
	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/fxamacker/cbor/v2"
	"github.com/gdamore/tcell/v2"
	"github.com/kelseyhightower/envconfig"
	"github.com/rivo/tview"
)

var globalConfig = &Config{
	App: AppConfig{
		Network: "",
		Refresh: 3,
		Retries: 3,
	},
	Node: NodeConfig{
		Network:    "mainnet",
		Port:       30001,
		SocketPath: "/opt/cardano/ipc/socket",
	},
}

var app = tview.NewApplication()
var pages = tview.NewPages()
var flex = tview.NewFlex()

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

var paused bool = false
var content string

// These are populated at build time
var Version string
var CommitHash string

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
	Network string `envconfig:"NETWORK"`
	Refresh uint32 `envconfig:"REFRESH"`
	Retries uint32 `envconfig:"RETRIES"`
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
		return nil, fmt.Errorf("error processing environment: %s", err)
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
			network := ouroboros.NetworkByName(c.App.Network)
			if network == ouroboros.NetworkInvalid {
				return fmt.Errorf("unknown network: %s", c.App.Network)
			}
			// Set Node's network, networkMagic, port, and socketPath
			c.Node.Network = c.App.Network
			c.Node.NetworkMagic = uint32(network.NetworkMagic)
			c.Node.SocketPath = "/ipc/node.socket"
			return nil
		} else if c.Node.Network != "" {
			network := ouroboros.NetworkByName(c.Node.Network)
			if network == ouroboros.NetworkInvalid {
				return fmt.Errorf("unknown network: %s", c.Node.Network)
			}
			c.Node.NetworkMagic = uint32(network.NetworkMagic)
			return nil
		} else {
			return fmt.Errorf("unable to set network magic")
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
		return nil, fmt.Errorf("failure creating ouroboros connection: %s", err)
	}
	if cfg.Node.Address != "" && cfg.Node.Port > 0 {
		err := oConn.Dial(
			"tcp",
			fmt.Sprintf("%s:%d", cfg.Node.Address, cfg.Node.Port),
		)
		if err != nil {
			return nil, fmt.Errorf(
				"failure connecting to node via TCP: %s",
				err,
			)
		}
	} else if cfg.Node.SocketPath != "" {
		_, err := os.Stat(cfg.Node.SocketPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf(
					"node socket path does not exist: %s",
					cfg.Node.SocketPath,
				)
			} else {
				return nil, fmt.Errorf(
					"unknown error checking if node socket path exists: %s",
					err,
				)
			}
		}
		err = oConn.Dial("unix", cfg.Node.SocketPath)
		if err != nil {
			return nil, fmt.Errorf(
				"failure connecting to node via UNIX socket: %s",
				err,
			)
		}
	} else {
		return nil, fmt.Errorf(
			"specify either the UNIX socket path or the address/port",
		)
	}
	return oConn, nil
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
	var sb strings.Builder
	// sb.WriteString(" [white]Transactions:\n")
	sb.WriteString(
		fmt.Sprintf(" [white]%-10s %-10s %s\n", "Size:", "Icon:", "TxHash:"),
	)
	for {
		txRawBytes, err := oConn.LocalTxMonitor().Client.NextTx()
		if err != nil {
			sb.WriteString(fmt.Sprintf(" [red]ERROR: NextTx: %s\n", err))
			return fmt.Sprint(sb.String())
		}
		if txRawBytes == nil {
			break
		}
		size := len(txRawBytes)
		txType, err := ledger.DetermineTransactionType(txRawBytes)
		if err != nil {
			sb.WriteString(fmt.Sprintf(" [red]ERROR: TxType: %s\n", err))
			return fmt.Sprint(sb.String())
		}
		tx, err := ledger.NewTransactionFromCbor(txType, txRawBytes)
		if err != nil {
			sb.WriteString(fmt.Sprintf(" [red]ERROR: Tx: %s\n", err))
			return fmt.Sprint(sb.String())
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
					"Minswap: Launch Bowl Redemption",
					"Minswap: LBE Deposit ADA",
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
			// Optim
			case "addr1zywj8y96k38kye7qz329dhp0t782ykr0ev92mtz4yhv6gph8ucsr8rpyzewcf9jyf7gmjj052dednasdeznehw7aqc7q0z7vn2":
				icon = "🅾️"
			// Spectrum
			case "addr1wyr4uz0tp75fu8wrg6gm83t20aphuc9vt6n8kvu09ctkugqpsrmeh",
				"addr1x94ec3t25egvhqy2n265xfhq882jxhkknurfe9ny4rl9k6dj764lvrxdayh2ux30fl0ktuh27csgmpevdu89jlxppvrst84slu",
				"addr1x8nz307k3sr60gu0e47cmajssy4fmld7u493a4xztjrll0aj764lvrxdayh2ux30fl0ktuh27csgmpevdu89jlxppvrswgxsta",
				"addr1wynp362vmvr8jtc946d3a3utqgclfdl5y9d3kn849e359hsskr20n":
				icon = "🌈"
			// Sundae
			case "addr1wxaptpmxcxawvr3pzlhgnpmzz3ql43n2tc8mn3av5kx0yzs09tqh8",
				"addr1w9qzpelu9hn45pefc0xr4ac4kdxeswq7pndul2vuj59u8tqaxdznu",
				"addr1w9jx45flh83z6wuqypyash54mszwmdj8r64fydafxtfc6jgrw4rm3":
				icon = "🍨"
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

		spaces := "10"
		if icon != "" {
			spaces = "9"
		}
		sb.WriteString(
			fmt.Sprintf(
				" [white]%-10d %-"+spaces+"s [blue]%s[white]\n",
				size,
				icon,
				tx.Hash(),
			),
		)
	}
	return fmt.Sprint(sb.String())
}

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Printf("failed to load config: %s", err)
		os.Exit(1)
	}
	// text.SetBorder(true)
	errorChan := make(chan error)
	go func() {
		for {
			err := <-errorChan
			text.SetText(fmt.Sprintf(" [red]ERROR: async: %s", err))
		}
	}()
	oConn, err := GetConnection(errorChan)
	if err != nil {
		text.SetText(fmt.Sprintf(" [red]failed to connect to node: %s", err))
	} else {
		text.SetText(fmt.Sprintf("%s\n%s",
			GetSizes(oConn),
			GetTransactions(oConn),
		))
	}
	headerText.SetText(fmt.Sprintln(" > txtop -", GetVersionString()))
	footerText.SetText(
		fmt.Sprintln(" [yellow](esc/q)[white] Quit | [yellow](p)[white] Pause"),
	)
	legendText.SetText(
		fmt.Sprintf(" Legend: [white]%s\n %s",
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
			2,
			0,
			false).
		AddItem(footerText,
			2,
			0,
			false)
	flex.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Rune() == 112 { // p
			paused = !paused
			footerText.Clear()
			if paused {
				footerText.SetText(
					fmt.Sprintln(
						" [yellow](esc/q)[white] Quit | [yellow](p)[white] Pause [yellow](paused)",
					),
				)
				return event
			}
			footerText.SetText(
				fmt.Sprintln(
					" [yellow](esc/q)[white] Quit | [yellow](p)[white] Pause",
				),
			)
		}
		if event.Rune() == 113 || event.Key() == tcell.KeyEscape { // q
			app.Stop()
		}
		return event
	})
	pages.AddPage("Main", flex, true, true)
	go func(cfg *Config) {
		for {
			if paused {
				// do nothing
			} else {
				time.Sleep(time.Second * time.Duration(cfg.App.Refresh))
				oConn, err := GetConnection(errorChan)
				if err != nil {
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
		}
	}(cfg)

	if err := app.SetRoot(pages, true).EnableMouse(false).Run(); err != nil {
		panic(err)
	}
}
