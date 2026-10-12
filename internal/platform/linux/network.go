// Package linux implements the platform interfaces for Linux.
package linux

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yoockh/sebab/internal/platform"
)

// Network reads interfaces through netlink (net.Interfaces) and routes from procfs.
type Network struct {
	// ProcRoot is "/proc" in production; tests point it at a fixture directory.
	ProcRoot string
	// ListInterfaces defaults to net.Interfaces, which on Linux is an RTM_GETLINK dump.
	ListInterfaces func() ([]net.Interface, error)
}

// NewNetwork returns the production implementation.
func NewNetwork() *Network { return &Network{ProcRoot: "/proc", ListInterfaces: net.Interfaces} }

// Links implements platform.Network.
func (n *Network) Links() ([]platform.Link, error) {
	ifaces, err := n.ListInterfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	links := make([]platform.Link, 0, len(ifaces))
	for _, i := range ifaces {
		links = append(links, platform.Link{
			Name:     i.Name,
			Up:       i.Flags&net.FlagUp != 0,
			Running:  i.Flags&net.FlagRunning != 0,
			Loopback: i.Flags&net.FlagLoopback != 0,
		})
	}
	return links, nil
}

const (
	rtfUp     = 0x0001
	rtfReject = 0x0200
)

// HasDefaultRoute implements platform.Network. It looks for an IPv4 or IPv6
// default route in the main table that is up, not a reject route, and not on lo.
func (n *Network) HasDefaultRoute() (bool, error) {
	v4, err4 := n.defaultRouteV4()
	if v4 {
		return true, nil
	}
	v6, err6 := n.defaultRouteV6()
	if v6 {
		return true, nil
	}
	if err4 != nil && err6 != nil {
		return false, fmt.Errorf("read routes: %w", err4)
	}
	return false, nil
}

func (n *Network) defaultRouteV4() (bool, error) {
	f, err := os.Open(filepath.Join(n.ProcRoot, "net", "route"))
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || fields[0] == "lo" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil {
			continue
		}
		if fields[1] == "00000000" && fields[7] == "00000000" && flags&rtfUp != 0 && flags&rtfReject == 0 {
			return true, nil
		}
	}
	return false, sc.Err()
}

func (n *Network) defaultRouteV6() (bool, error) {
	f, err := os.Open(filepath.Join(n.ProcRoot, "net", "ipv6_route"))
	if err != nil {
		return false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	zero := strings.Repeat("0", 32)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 || fields[9] == "lo" {
			continue
		}
		flags, err := strconv.ParseUint(fields[8], 16, 32)
		if err != nil {
			continue
		}
		if fields[0] == zero && fields[1] == "00" && flags&rtfUp != 0 && flags&rtfReject == 0 {
			return true, nil
		}
	}
	return false, sc.Err()
}
