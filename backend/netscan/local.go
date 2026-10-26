package netscan

import (
	"net"
	"strconv"
	"strings"
)

// LocalSubnet describes the /24 subnet of the primary outbound interface.
type LocalSubnet struct {
	// Base is the first three octets joined by dots, e.g. "192.168.60".
	Base string `json:"base"`
	// LastOctet is this machine's own last octet within the subnet.
	LastOctet int `json:"lastOctet"`
}

// DetectLocalSubnet finds the IPv4 address the OS would use to reach the
// public internet (UDP dial trick — no packet is sent) and splits it into the
// first three octets plus the machine's own last octet, so the scanner can
// prefill a Moba-style base + last-octet-range input.
func DetectLocalSubnet() (LocalSubnet, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return LocalSubnet{}, err
	}
	defer conn.Close()

	ip := conn.LocalAddr().(*net.UDPAddr).IP.To4()
	if ip == nil {
		return LocalSubnet{}, errNoIPv4
	}
	base := strings.Join([]string{
		strconv.Itoa(int(ip[0])),
		strconv.Itoa(int(ip[1])),
		strconv.Itoa(int(ip[2])),
	}, ".")
	return LocalSubnet{Base: base, LastOctet: int(ip[3])}, nil
}

type localSubnetError string

func (e localSubnetError) Error() string { return string(e) }

const errNoIPv4 = localSubnetError("no IPv4 address on the outbound interface")
