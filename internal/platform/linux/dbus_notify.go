package linux

import (
	"context"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

// Notifier sends desktop notifications through org.freedesktop.Notifications.
// The session bus connection is opened lazily and reopened after an error.
type Notifier struct {
	// Address is the bus address; empty means the session bus.
	Address string
	AppName string

	mu   sync.Mutex
	conn *dbus.Conn
}

// Urgency levels from the Desktop Notifications spec.
var urgencies = map[string]byte{"low": 0, "normal": 1, "critical": 2}

// Notify shows one notification and returns the server's notification id.
func (n *Notifier) Notify(ctx context.Context, title, body, urgency string) (uint32, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn == nil {
		var err error
		if n.Address != "" {
			n.conn, err = dbus.Connect(n.Address, dbus.WithContext(ctx))
		} else {
			n.conn, err = dbus.ConnectSessionBus(dbus.WithContext(ctx))
		}
		if err != nil {
			n.conn = nil
			return 0, fmt.Errorf("connect to the session bus: %w", err)
		}
	}
	u, ok := urgencies[urgency]
	if !ok {
		u = 1
	}
	icon := "network-idle"
	if urgency == "critical" {
		icon = "network-error"
	}
	app := n.AppName
	if app == "" {
		app = "Sebab"
	}
	obj := n.conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	var id uint32
	err := obj.CallWithContext(ctx, "org.freedesktop.Notifications.Notify", 0,
		app, uint32(0), icon, title, body, []string{},
		map[string]dbus.Variant{"urgency": dbus.MakeVariant(u), "desktop-entry": dbus.MakeVariant("sebab")},
		int32(-1)).Store(&id)
	if err != nil {
		n.conn.Close()
		n.conn = nil
		return 0, fmt.Errorf("send notification: %w", err)
	}
	return id, nil
}

// Close releases the bus connection.
func (n *Notifier) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn != nil {
		n.conn.Close()
		n.conn = nil
	}
}

// WatchSleep calls onSleep(true) before the system suspends and onSleep(false)
// after it resumes, using logind's PrepareForSleep signal on the system bus.
// It returns when ctx ends, or immediately with an error when the system bus
// or logind is unavailable.
func WatchSleep(ctx context.Context, address string, onSleep func(bool)) error {
	var conn *dbus.Conn
	var err error
	if address != "" {
		conn, err = dbus.Connect(address, dbus.WithContext(ctx))
	} else {
		conn, err = dbus.ConnectSystemBus(dbus.WithContext(ctx))
	}
	if err != nil {
		return fmt.Errorf("connect to the system bus: %w", err)
	}
	defer conn.Close()
	if err := conn.AddMatchSignalContext(ctx, dbus.WithMatchInterface("org.freedesktop.login1.Manager"),
		dbus.WithMatchMember("PrepareForSleep")); err != nil {
		return fmt.Errorf("subscribe to PrepareForSleep: %w", err)
	}
	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)
	defer conn.RemoveSignal(ch)
	for {
		select {
		case <-ctx.Done():
			return nil
		case sig, ok := <-ch:
			if !ok {
				return fmt.Errorf("system bus connection closed")
			}
			if sig.Name != "org.freedesktop.login1.Manager.PrepareForSleep" || len(sig.Body) != 1 {
				continue
			}
			if start, ok := sig.Body[0].(bool); ok {
				onSleep(start)
			}
		}
	}
}
