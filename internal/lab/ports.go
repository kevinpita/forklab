package lab

import (
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
)

// portStride separates the ports of consecutive nodes: node i listens on
// base + i*portStride.
const portStride = 100

// basePort is a listen address every node gets. An optional one is set only
// when the chain's config file has the key.
type basePort struct {
	file, key string
	base      int
	optional  bool
}

var corePorts = []basePort{
	{file: "config.toml", key: "p2p.laddr", base: 26656},
	{file: "config.toml", key: "rpc.laddr", base: 26657},
	{file: "config.toml", key: "rpc.pprof_laddr", base: 6060},
	{file: "app.toml", key: "api.address", base: 1317},
	{file: "app.toml", key: "grpc.address", base: 9090},
	// SDK v0.50 serves gRPC-web on the API port and dropped this key.
	{file: "app.toml", key: "grpc-web.address", base: 9091, optional: true},
}

// allocatePorts returns each node's ports: the core ports plus the profile's
// extra ports (config file -> key -> base port), offset per node. Two keys
// landing on one port, or a port past 65535, is an error.
func allocatePorts(n int, extra map[string]map[string]int) ([][]Port, error) {
	bases := slices.Clone(corePorts)
	for _, file := range slices.Sorted(maps.Keys(extra)) {
		for _, key := range slices.Sorted(maps.Keys(extra[file])) {
			bases = append(bases, basePort{file: file, key: key, base: extra[file][key]})
		}
	}
	owner := map[int]string{}
	nodes := make([][]Port, n)
	for i := range n {
		for _, b := range bases {
			port := b.base + i*portStride
			name := fmt.Sprintf("node%d %s %s", i, b.file, b.key)
			if port > 65535 {
				return nil, fmt.Errorf("%s: port %d is past 65535; use fewer validators", name, port)
			}
			if prev, ok := owner[port]; ok {
				return nil, fmt.Errorf("%s: port %d is also %s", name, port, prev)
			}
			owner[port] = name
			nodes[i] = append(nodes[i], Port{File: b.file, Key: b.key, Port: port, optional: b.optional})
		}
	}
	return nodes, nil
}

// checkFree fails on the first port something already listens on.
func checkFree(nodes [][]Port) error {
	for i, ports := range nodes {
		for _, p := range ports {
			ln, err := net.Listen("tcp", ":"+strconv.Itoa(p.Port))
			if err != nil {
				return fmt.Errorf("port %d (node%d %s %s) is in use: %w", p.Port, i, p.File, p.Key, err)
			}
			_ = ln.Close()
		}
	}
	return nil
}
