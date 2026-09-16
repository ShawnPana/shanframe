package peer

import (
	"fmt"
	"net"
	"time"

	"github.com/pion/transport/v4"
	"github.com/pion/transport/v4/stdnet"
)

// phoneNet is pion's network for Android, where apps may not enumerate
// interfaces (netlink is denied: "netlinkrib: permission denied"), which
// otherwise fails every session before it starts. The address that matters
// is the one the kernel would use to reach the internet; we learn it from a
// connected UDP socket and present it as the single interface. Everything
// else is the standard net package.
type phoneNet struct {
	*stdnet.Net
	ifs []*transport.Interface
}

func newPhoneNet() *phoneNet {
	n := &phoneNet{Net: &stdnet.Net{}}
	ifc := transport.NewInterface(net.Interface{Index: 1, MTU: 1500, Name: "wlan0", Flags: net.FlagUp | net.FlagRunning | net.FlagBroadcast | net.FlagMulticast})
	for _, probe := range []struct{ network, addr string }{{"udp4", "8.8.8.8:53"}, {"udp6", "[2001:4860:4860::8888]:53"}} {
		c, err := net.DialTimeout(probe.network, probe.addr, 2*time.Second) // no packets: UDP connect only picks a route
		if err != nil {
			continue
		}
		ip := c.LocalAddr().(*net.UDPAddr).IP
		c.Close()
		if ip == nil || ip.IsUnspecified() {
			continue
		}
		mask := net.CIDRMask(32, 32)
		if ip.To4() == nil {
			mask = net.CIDRMask(128, 128)
		}
		ifc.AddAddress(&net.IPNet{IP: ip, Mask: mask})
	}
	n.ifs = []*transport.Interface{ifc}
	return n
}

func (n *phoneNet) Interfaces() ([]*transport.Interface, error) { return n.ifs, nil }

func (n *phoneNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	for _, ifc := range n.ifs {
		if ifc.Index == index {
			return ifc, nil
		}
	}
	return nil, fmt.Errorf("%w: index=%d", transport.ErrInterfaceNotFound, index)
}

func (n *phoneNet) InterfaceByName(name string) (*transport.Interface, error) {
	for _, ifc := range n.ifs {
		if ifc.Name == name {
			return ifc, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", transport.ErrInterfaceNotFound, name)
}
