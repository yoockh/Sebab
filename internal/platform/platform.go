// Package platform defines the OS-specific interfaces the engine depends on.
// The Linux implementations live in platform/linux; tests use fakes.
package platform

// Link is a network interface as seen by the OS.
type Link struct {
	Name     string
	Up       bool // administratively up (IFF_UP)
	Running  bool // carrier / operational (IFF_RUNNING)
	Loopback bool
}

// Network reports local network state.
type Network interface {
	Links() ([]Link, error)
	HasDefaultRoute() (bool, error)
}

// HasActiveLink reports whether any non-loopback interface is up and running.
func HasActiveLink(links []Link) bool {
	for _, l := range links {
		if !l.Loopback && l.Up && l.Running {
			return true
		}
	}
	return false
}

// FindLink returns the named link and whether it exists.
func FindLink(links []Link, name string) (Link, bool) {
	for _, l := range links {
		if l.Name == name {
			return l, true
		}
	}
	return Link{}, false
}

// StaticNetwork is a fixed Network for tests and the failure lab.
type StaticNetwork struct {
	LinkList     []Link
	DefaultRoute bool
	Err          error
}

// Links implements Network.
func (s StaticNetwork) Links() ([]Link, error) { return s.LinkList, s.Err }

// HasDefaultRoute implements Network.
func (s StaticNetwork) HasDefaultRoute() (bool, error) { return s.DefaultRoute, s.Err }

// Online is a StaticNetwork with one active link and a default route.
func Online(extra ...Link) StaticNetwork {
	links := append([]Link{{Name: "lo", Up: true, Running: true, Loopback: true},
		{Name: "eth0", Up: true, Running: true}}, extra...)
	return StaticNetwork{LinkList: links, DefaultRoute: true}
}
