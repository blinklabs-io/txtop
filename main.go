// Copyright 2023 Blink Labs, LLC.
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
	"os"
	"strings"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/gdamore/tcell/v2"
	"github.com/kelseyhightower/envconfig"
	"github.com/rivo/tview"
	"golang.org/x/crypto/blake2b"
)

var globalConfig = &Config{
	App: AppConfig{
		Network: "",
		Refresh: 10,
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
	SetTextColor(tcell.ColorGreen)
var text = tview.NewTextView().
	SetDynamicColors(true).
	SetChangedFunc(func() { app.Draw() })

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
		err := oConn.Dial("tcp", fmt.Sprintf("%s:%d", cfg.Node.Address, cfg.Node.Port))
		if err != nil {
			return nil, fmt.Errorf("failure connecting to node via TCP: %s", err)
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
		return " [green]> txtop[white]"
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
		return " [red]failed to connect to node"
	}
	var sb strings.Builder
	sb.WriteString(" [white]Transactions:\n")
	sb.WriteString(fmt.Sprintf(" [white]%-20s %s\n", "Size", "TxHash"))
	for {
		tx, err := oConn.LocalTxMonitor().Client.NextTx()
		if err != nil {
			sb.WriteString(fmt.Sprintf(" [red]ERROR: NextTx: %s\n", err))
			return fmt.Sprint(sb.String())
		}
		if tx == nil {
			break
		}
		size := len(tx)
		var txUnwrap []cbor.RawMessage
		_, err = cbor.Decode(tx, &txUnwrap)
		if err != nil {
			sb.WriteString(fmt.Sprintf(" [red]ERROR: txUnwrap: %s", err))
			return fmt.Sprint(sb.String())
		}
		txBody := txUnwrap[0]
		txIdHash := blake2b.Sum256(txBody)
		txIdHex := hex.EncodeToString(txIdHash[:])

		sb.WriteString(fmt.Sprintf(" [white]%-20d [blue]%s[white]\n", size, txIdHex))
	}
	return fmt.Sprint(sb.String())
}

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Printf("failed to load config: %s", err)
		os.Exit(1)
	}
	text.SetBorder(true)
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
	headerText.SetText(fmt.Sprintln(" [green]> txtop[white]"))
	footerText.SetText(fmt.Sprintln(" [yellow](esc/q) Quit[white]"))
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
		if event.Rune() == 113 || event.Key() == tcell.KeyEscape { // q
			app.Stop()
		}
		return event
	})
	pages.AddPage("Main", flex, true, true)
	go func(cfg *Config) {
		for {
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
	}(cfg)

	if err := app.SetRoot(pages, true).EnableMouse(false).Run(); err != nil {
		panic(err)
	}
}
