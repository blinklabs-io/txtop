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
var text = tview.NewTextView().
	SetDynamicColors(true).
	SetChangedFunc(func() { app.Draw() })

var paused bool = false

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
		// Check if Tx has metadata and compare against our list
		var icon string
		if tx.Metadata() != nil {
			mdCbor := tx.Metadata().Cbor()
			var msgMetadata models.Cip20Metadata
			_ = cbor.Unmarshal(mdCbor, &msgMetadata)
			if msgMetadata.Num674.Msg != nil {
				// Only check first line
				switch msgMetadata.Num674.Msg[0] {
				case "Dexhunter Trade":
					icon = "🐉"
				case "Minswap: Deposit Order",
					"Minswap: MasterChef",
					"Minswap: Order Executed",
					"Minswap: Swap Exact In Order",
					"Minswap: Swap Exact Out Order",
					"Minswap: V2 Harvest reward",
					"Minswap: V2 Stake liquidity",
					"Minswap: Zap Order":
					icon = "🐱"
				case "SSP: Swap Request":
					icon = "🍨"
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
	headerText.SetText(fmt.Sprintln(" > txtop"))
	footerText.SetText(
		fmt.Sprintln(" [yellow](esc/q)[white] Quit | [yellow](p)[white] Pause"),
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
					text.Clear()
					text.SetText(fmt.Sprintf("%s\n%s",
						GetSizes(oConn),
						GetTransactions(oConn),
					))
				}
			}
		}
	}(cfg)

	if err := app.SetRoot(pages, true).EnableMouse(false).Run(); err != nil {
		panic(err)
	}
}
