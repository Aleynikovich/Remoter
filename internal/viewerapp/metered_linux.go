package viewerapp

import (
	"os"

	"github.com/godbus/dbus/v5"
)

// metered reports whether NetworkManager says the connection is metered
// (a phone's hotspot or USB tethering, say: Android tells DHCP clients).
// REMOTER_METERED=1 or 0 overrides it.
func metered() (bool, error) {
	if v := os.Getenv("REMOTER_METERED"); v != "" {
		return v == "1", nil
	}
	bus, err := dbus.SystemBus()
	if err != nil {
		return false, err
	}
	v, err := bus.Object("org.freedesktop.NetworkManager", "/org/freedesktop/NetworkManager").
		GetProperty("org.freedesktop.NetworkManager.Metered")
	if err != nil {
		return false, err
	}
	// NMMetered: 1 yes, 3 guessed yes; 0 unknown, 2 no, 4 guessed no.
	m, _ := v.Value().(uint32)
	return m == 1 || m == 3, nil
}
