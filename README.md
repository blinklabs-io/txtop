# txtop

Mempool inspector for Cardano Node software.

# Usage

There's no configuration file. Everything is handled by environment variables.

This tool can connect to a cardano-node using either UNIX socket or TCP, such
as an exposed socat.

## Global variables

`NETWORK` - Sets network and forces container defaults for `NETWORK` mode
`REFRESH` - Sets how fast we refresh data (in seconds), defaults to 10
`RETRIES` - Sets how many retries before aborting (currently unused)

## Cardano variables

`CARDANO_NETWORK` - Sets network to connect to node unless NETWORK is set,
    defaults to mainnet
`CARDANO_NODE_NETWORK_MAGIC` - (optional) Manually configure network magic
`CARDANO_NODE_SOCKET_PATH` - Sets path to UNIX socket of node, defaults to
    /opt/cardano/ipc/socket unless NETWORK is set, then uses /ipc/node.socket
`CARDANO_NODE_SOCKET_TCP_HOST` - Sets the TCP host for NtC communication
    (socat), defaults to empty
`CARDANO_NODE_SOCKET_TCP_PORT` - Sets the TCP port for NtC communication
    (socat), defaults to 30001

# Development / Building

This requires Go 1.20 or better is installed. You also need `make`.

```bash
# Build
make
# Run
./bluefin
```

You can also run the code without building a binary, first
```bash
go run .
```
