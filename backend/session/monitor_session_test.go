package session

import (
	"bytes"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// --- systemctl list-units parsing ---

func TestParseSystemctlUnits(t *testing.T) {
	out := `nginx.service                       loaded    active     running     A high performance web server and a reverse proxy
ssh.service                         loaded    active     running     OpenBSD Secure Shell server
systemd-fsck@dev-sda1.service       loaded    active     exited      File System Check on /dev/sda1
docker.service                      loaded    failed     failed      Docker Application Container Engine
unattended-upgrades.service         loaded    inactive   dead        Unattended Upgrades Shutdown
`
	got := parseSystemctlUnits(out)
	want := []ServiceInfo{
		{Name: "nginx.service", Load: "loaded", Active: "active", Sub: "running", Description: "A high performance web server and a reverse proxy"},
		{Name: "ssh.service", Load: "loaded", Active: "active", Sub: "running", Description: "OpenBSD Secure Shell server"},
		{Name: "systemd-fsck@dev-sda1.service", Load: "loaded", Active: "active", Sub: "exited", Description: "File System Check on /dev/sda1"},
		{Name: "docker.service", Load: "loaded", Active: "failed", Sub: "failed", Description: "Docker Application Container Engine"},
		{Name: "unattended-upgrades.service", Load: "loaded", Active: "inactive", Sub: "dead", Description: "Unattended Upgrades Shutdown"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSystemctlUnits mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseSystemctlUnitsShortLine(t *testing.T) {
	// A unit whose description is missing must not panic or drop fields.
	out := "foo.service    loaded    active     running\n"
	got := parseSystemctlUnits(out)
	if len(got) != 1 {
		t.Fatalf("want 1 service, got %d", len(got))
	}
	if got[0].Description != "" {
		t.Errorf("want empty description, got %q", got[0].Description)
	}
	if got[0].Name != "foo.service" || got[0].Active != "active" {
		t.Errorf("unexpected service: %#v", got[0])
	}
}

// --- systemctl list-unit-files parsing ---

func TestParseSystemctlUnitFiles(t *testing.T) {
	// Newer systemd prints a VENDOR-PRESET column; older prints only two.
	out := `nginx.service                        enabled      enabled
ssh.service                          enabled      enabled
cups.service                         disabled     disabled
systemd-fsck@dev-sda1.service        static       -
docker.service                       enabled      enabled
`
	got := parseSystemctlUnitFiles(out)
	want := map[string]string{
		"nginx.service":                 "enabled",
		"ssh.service":                   "enabled",
		"cups.service":                  "disabled",
		"docker.service":                "enabled",
		"systemd-fsck@dev-sda1.service": "static",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSystemctlUnitFiles mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// --- lspci -mm parsing ---

func TestParseLspciMm(t *testing.T) {
	out := `00:00.0 "Host bridge" "Intel Corporation" "82G33/G31/P35/P31 Express DRAM Controller" -r0a "Dell" "OptiPlex 755"
00:1f.2 "SATA controller" "Intel Corporation" "82801IR/IO/IH (ICH9R/DO/DH) 6 port SATA Controller [AHCI mode]" -r02 "Dell" "OptiPlex 755"
01:00.0 "VGA compatible controller" "NVIDIA Corporation" "GK107 [GeForce GT 640]"
`
	got := parseLspciMm(out)
	if len(got) != 3 {
		t.Fatalf("want 3 devices, got %d", len(got))
	}
	d := got[0]
	if d.ID != "00:00.0" || d.Class != "Host bridge" || d.Vendor != "Intel Corporation" ||
		d.Product != "82G33/G31/P35/P31 Express DRAM Controller" || d.Rev != "0a" {
		t.Errorf("device 0 mismatch: %#v", d)
	}
	if got[2].Rev != "" || got[2].Vendor != "NVIDIA Corporation" {
		t.Errorf("device 2 mismatch: %#v", got[2])
	}
}

func TestParseLspciTextFallback(t *testing.T) {
	out := `00:00.0 Host bridge: Intel Corporation 82G33/G31/P35/P31 Express DRAM Controller (rev 0a)
01:00.0 VGA compatible controller: NVIDIA Corporation GK107 [GeForce GT 640] (rev a1)
`
	got := parseLspciText(out)
	if len(got) != 2 {
		t.Fatalf("want 2 devices, got %d", len(got))
	}
	// The text format has no vendor/device boundary, so the vendor keeps only
	// its first word and everything else lands in Product.
	d := got[0]
	if d.ID != "00:00.0" || d.Class != "Host bridge" || d.Vendor != "Intel" ||
		d.Product != "Corporation 82G33/G31/P35/P31 Express DRAM Controller" || d.Rev != "0a" {
		t.Errorf("device 0 mismatch: %#v", d)
	}
	if got[1].Product != "Corporation GK107 [GeForce GT 640]" {
		t.Errorf("device 1 mismatch: %#v", got[1])
	}
}

// --- ipmitool sensor list parsing ---

func TestParseIpmitoolSensors(t *testing.T) {
	out := `CPU Temp        | 34.000     | degrees C  | ok    | na        | 3.000     | 0.000     | 0.000     | 85.000    | 90.000
SYS FAN1        | 2400.000   | RPM        | cr    | na        | 100.000   | 200.000   | 300.000   | na        | na
+12V            | 12.100     | Volts      | ok    | na        | 0.000     | 0.000     | 0.000     | 11.000    | 13.000
PS Status       | 0x0        | discrete   | 0x0100| na        | na        | na        | na        | na        | na
`
	got := parseIpmitoolSensors(out)
	if len(got) != 4 {
		t.Fatalf("want 4 sensors, got %d", len(got))
	}
	if got[0].Name != "CPU Temp" || got[0].Value != "34.000" || got[0].Unit != "degrees C" || got[0].Status != "ok" {
		t.Errorf("sensor 0 mismatch: %#v", got[0])
	}
	if got[1].Status != "cr" {
		t.Errorf("sensor 1 status mismatch: %#v", got[1])
	}
	if got[3].Status != "0x0100" {
		t.Errorf("discrete status mismatch: %#v", got[3])
	}
}

// --- ipmitool fru print parsing ---

func TestParseIpmiFru(t *testing.T) {
	out := `FRU Device Description : Builtin FRU Device (ID 0)
 Board Product         : PowerEdge R740
 Board Manufacturer    : Dell Inc.
 Board Serial          : BOARD123
 Board Part Number     : 0PART
 Product Manufacturer  : Dell Inc.
 Product Name          : PowerEdge R740
 Product Serial        : SER456
 Product Part Number   : 0PN789
`
	got := parseIpmiFru(out)
	if got == nil {
		t.Fatal("want non-nil FruInfo")
	}
	if got.Product != "PowerEdge R740" || got.Manufacturer != "Dell Inc." ||
		got.Serial != "SER456" || got.PartNumber != "0PN789" {
		t.Errorf("FruInfo mismatch: %#v", got)
	}
}

func TestParseIpmiFruEmpty(t *testing.T) {
	if got := parseIpmiFru(""); got != nil {
		t.Errorf("want nil for empty output, got %#v", got)
	}
	if got := parseIpmiFru("not fru output at all"); got != nil {
		t.Errorf("want nil for non-FRU output, got %#v", got)
	}
}

// --- device category classification ---

func TestDeviceCategory(t *testing.T) {
	cases := map[string]string{
		// lshw classes
		"processor":     "processor",
		"memory":        "memory",
		"disk":          "storage",
		"storage":       "storage",
		"volume":        "storage",
		"tape":          "storage",
		"network":       "network",
		"communication": "network",
		"display":       "display",
		"multimedia":    "display",
		"bus":           "bus",
		"bridge":        "bus",
		"input":         "other",
		"generic":       "other",
		// lspci class texts (fallback path)
		"Host bridge":                    "bus",
		"USB controller":                 "bus",
		"VGA compatible controller":      "display",
		"Audio device":                   "display",
		"Ethernet controller":            "network",
		"Network controller":             "network",
		"SATA controller":                "storage",
		"Non-Volatile memory controller": "storage",
		"RAM memory":                     "memory",
		"ISA bridge":                     "bus",
		"Signal processing controller":   "other",
	}
	for in, want := range cases {
		if got := deviceCategory(in); got != want {
			t.Errorf("deviceCategory(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- lshw -json parsing ---

const lshwSample = `{
  "id": "machine", "class": "system", "description": "Computer",
  "children": [
    {
      "id": "core", "class": "bus", "description": "Motherboard",
      "children": [
        {
          "id": "usb", "class": "bus", "description": "USB controller",
          "businfo": "pci@0000:00:14.0", "vendor": "Intel Corp.",
          "product": "8000 Series USB Controller", "version": "00",
          "configuration": {"driver": "xhci_hcd", "latency": "0"},
          "children": [
            {
              "id": "usbhost", "class": "bus", "description": "USB mass storage",
              "businfo": "usb@1:3", "vendor": "SanDisk",
              "product": "Ultra USB 3.0", "serial": "ABC123"
            }
          ]
        },
        {
          "id": "nvme", "class": "storage", "description": "Non-Volatile memory controller",
          "businfo": "pci@0000:01:00.0", "vendor": "Samsung",
          "product": "NVMe Controller", "configuration": {"driver": "nvme"},
          "children": [
            {
              "id": "namespace", "class": "disk", "description": "NVMe disk",
              "businfo": "nvme@0:1", "logicalname": ["nvme0n1"],
              "product": "Samsung SSD 970 EVO", "serial": "S123456",
              "version": "1B2QEXM7", "size": 512110190592
            }
          ]
        },
        {
          "id": "pci", "class": "bridge", "description": "PCI bridge",
          "businfo": "pci@0000:00:1c.0", "vendor": "Intel Corp.",
          "product": "6th Gen PCI Express Root Port", "configuration": {"driver": "pcieport"}
        },
        {
          "id": "sda", "class": "disk", "description": "SATA disk",
          "businfo": "scsi@0:0.0.0", "logicalname": ["/dev/sda"],
          "product": "SATA SSD", "size": 250059350016
        }
      ]
    }
  ]
}`

func TestParseLshwDevices(t *testing.T) {
	got := parseLshwDevices(lshwSample)
	// Every tree node becomes a row, in tree order; no filtering by bus type.
	if len(got) != 8 {
		t.Fatalf("want 8 devices, got %d: %#v", len(got), got)
	}
	d := got[2] // USB controller
	if d.ID != "pci@0000:00:14.0" || d.Vendor != "Intel Corp." ||
		d.Product != "8000 Series USB Controller" || d.Driver != "xhci_hcd" || d.Rev != "00" {
		t.Errorf("usb controller mismatch: %#v", d)
	}
	if got[3].ID != "usb@1:3" || got[3].Vendor != "SanDisk" || got[3].Serial != "ABC123" {
		t.Errorf("usb device mismatch: %#v", got[3])
	}
	if got[4].ID != "pci@0000:01:00.0" || got[4].Driver != "nvme" {
		t.Errorf("nvme controller mismatch: %#v", got[4])
	}
	nvmeDisk := got[5]
	if nvmeDisk.ID != "nvme0n1" || nvmeDisk.Serial != "S123456" ||
		nvmeDisk.Rev != "1B2QEXM7" || nvmeDisk.Capacity == "" {
		t.Errorf("nvme disk mismatch: %#v", nvmeDisk)
	}
	if got[6].ID != "pci@0000:00:1c.0" {
		t.Errorf("pci bridge mismatch: %#v", got[6])
	}
	if got[7].ID != "/dev/sda" || got[7].Capacity == "" {
		t.Errorf("sata disk mismatch: %#v", got[7])
	}
}

func TestParseLshwDevicesInvalid(t *testing.T) {
	if got := parseLshwDevices("lshw: not json"); len(got) != 0 {
		t.Errorf("want empty for non-JSON output, got %#v", got)
	}
	if got := parseLshwDevices(""); len(got) != 0 {
		t.Errorf("want empty for empty output, got %#v", got)
	}
}

// --- ipmitool lan print parsing ---

func TestParseIpmiLan(t *testing.T) {
	out := `Set in Progress         : Set Complete
Auth Type Support       :
Auth Type Enable        : Callback :
                        : MD2 :
                        : MD5 :
IP Address Source       : Static Address
IP Address              : 192.168.1.100
Subnet Mask             : 255.255.255.0
MAC Address             : 0c:c4:7a:11:22:33
SNMP Community String   : public
IP Header               : TTL=0x40 Flags=0x40 Precedence=0x00 TOS=0x10
Default Gateway IP      : 192.168.1.1
Default Gateway MAC     : 00:00:00:00:00:00
`
	got := parseIpmiLan(out)
	// Continuation lines (empty key) and empty values must be skipped.
	want := []LanField{
		{Key: "Set in Progress", Value: "Set Complete"},
		{Key: "Auth Type Enable", Value: "Callback :"},
		{Key: "IP Address Source", Value: "Static Address"},
		{Key: "IP Address", Value: "192.168.1.100"},
		{Key: "Subnet Mask", Value: "255.255.255.0"},
		{Key: "MAC Address", Value: "0c:c4:7a:11:22:33"},
		{Key: "SNMP Community String", Value: "public"},
		{Key: "IP Header", Value: "TTL=0x40 Flags=0x40 Precedence=0x00 TOS=0x10"},
		{Key: "Default Gateway IP", Value: "192.168.1.1"},
		{Key: "Default Gateway MAC", Value: "00:00:00:00:00:00"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseIpmiLan mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseIpmiLanEmpty(t *testing.T) {
	if got := parseIpmiLan(""); len(got) != 0 {
		t.Errorf("want empty for empty output, got %#v", got)
	}
}

func TestStandardLanFields(t *testing.T) {
	in := []LanField{
		{Key: "Set in Progress", Value: "Set Complete"},
		{Key: "IP Address Source", Value: "Static Address"},
		{Key: "IP Address", Value: "192.168.1.100"},
		{Key: "Subnet Mask", Value: "255.255.255.0"},
		{Key: "MAC Address", Value: "0c:c4:7a:11:22:33"},
		{Key: "SNMP Community String", Value: "public"},
		{Key: "Default Gateway IP", Value: "192.168.1.1"},
		{Key: "Default Gateway MAC", Value: "00:00:00:00:00:00"},
	}
	got := standardLanFields(in)
	// Only the standard network fields, in a fixed display order.
	want := []LanField{
		{Key: "IP Address", Value: "192.168.1.100"},
		{Key: "Subnet Mask", Value: "255.255.255.0"},
		{Key: "MAC Address", Value: "0c:c4:7a:11:22:33"},
		{Key: "Default Gateway IP", Value: "192.168.1.1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("standardLanFields mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// --- systemctl show parsing ---

func TestParseSystemctlShow(t *testing.T) {
	out := `LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=enabled
Description=OpenBSD Secure Shell server
ExecMainPID=1234
MemoryCurrent=15728640
`
	got := parseSystemctlShow(out)
	if got["LoadState"] != "loaded" || got["ActiveState"] != "active" ||
		got["Description"] != "OpenBSD Secure Shell server" || got["MemoryCurrent"] != "15728640" {
		t.Errorf("parseSystemctlShow mismatch: %#v", got)
	}
}

// --- service log line clamping ---

func TestClampLogLines(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, defaultLogLines},
		{-5, defaultLogLines},
		{50, 50},
		{200, 200},
		{100000, maxLogLines},
	}
	for _, c := range cases {
		if got := clampLogLines(c.in); got != c.want {
			t.Errorf("clampLogLines(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// --- service action / unit name validation ---

func TestValidateServiceAction(t *testing.T) {
	for _, a := range []string{"start", "stop", "restart", "enable", "disable"} {
		if !validServiceAction(a) {
			t.Errorf("action %q should be valid", a)
		}
	}
	for _, a := range []string{"", "reboot", "start;", "mask", "START"} {
		if validServiceAction(a) {
			t.Errorf("action %q should be invalid", a)
		}
	}
}

func TestValidUnitName(t *testing.T) {
	for _, n := range []string{"nginx.service", "systemd-fsck@dev-sda1.service", "user@1000.service", "php8.2-fpm.service"} {
		if !validUnitName(n) {
			t.Errorf("unit %q should be valid", n)
		}
	}
	for _, n := range []string{"", "nginx;reboot", "a b", "$(reboot)", "a/b", "nginx\n", "a`id`", ".ssh/authorized_keys",
		"-H", "-network.service", "-.service", "-"} {
		if validUnitName(n) {
			t.Errorf("unit %q should be invalid", n)
		}
	}
}

// --- df output parsing ---

func TestParseDfOutputWithTypeColumn(t *testing.T) {
	out := `Filesystem Type Size Used Avail Use% Mounted on
overlay overlay 200G 50G 150G 25% /
tmpfs tmpfs 64M 0 64M 0% /dev/shm
10.0.0.1:/export/nfs nfs4 1.0T 100G 900G 10% /mnt/nfs
`
	got := parseDfOutput(out)
	want := []dfEntry{
		{Spec: "overlay", FSType: "overlay", Total: "200G", Used: "50G", Usage: 25, Mount: "/"},
		{Spec: "tmpfs", FSType: "tmpfs", Total: "64M", Used: "0", Usage: 0, Mount: "/dev/shm"},
		{Spec: "10.0.0.1:/export/nfs", FSType: "nfs4", Total: "1.0T", Used: "100G", Usage: 10, Mount: "/mnt/nfs"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseDfOutput mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseDfOutputWithoutTypeColumn(t *testing.T) {
	// Busybox/old df without -T falls back to the six-column form.
	out := `Filesystem Size Used Avail Use% Mounted on
/dev/sda1 731.0M 93M 601M 13% /boot
`
	got := parseDfOutput(out)
	want := []dfEntry{
		{Spec: "/dev/sda1", Total: "731.0M", Used: "93M", Usage: 13, Mount: "/boot"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseDfOutput mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestParseDfOutputIgnoresShortLines(t *testing.T) {
	got := parseDfOutput("Filesystem Type Size\n\ngarbage\n")
	if len(got) != 0 {
		t.Errorf("want no rows, got %#v", got)
	}
}

// --- merging df-only filesystems into the lsblk list ---

func TestAppendDfOnlyDisks(t *testing.T) {
	disks := []DiskInfo{
		{Name: "sda", Type: "disk"},
		{Name: "  sda1", Type: "part", MountPoint: "/boot", Used: "93M", Total: "731.0M", Usage: 13},
	}
	rows := []dfEntry{
		{Spec: "/dev/sda1", FSType: "ext4", Total: "731.0M", Used: "93M", Usage: 13, Mount: "/boot"},
		{Spec: "overlay", FSType: "overlay", Total: "200G", Used: "50G", Usage: 25, Mount: "/"},
		{Spec: "tmpfs", FSType: "tmpfs", Total: "64M", Used: "0", Usage: 0, Mount: "/dev/shm"},
		{Spec: "tmpfs", FSType: "tmpfs", Total: "64M", Used: "0", Usage: 0, Mount: "/dev/shm"},
	}
	got := appendDfOnlyDisks(disks, rows)
	if len(got) != 4 {
		t.Fatalf("want 4 disks (2 lsblk + 2 df-only), got %d: %#v", len(got), got)
	}
	overlay := got[2]
	if overlay.Name != "overlay" || overlay.MountPoint != "/" || overlay.FSType != "overlay" || overlay.Usage != 25 {
		t.Errorf("unexpected overlay row: %#v", overlay)
	}
	if got[3].MountPoint != "/dev/shm" {
		t.Errorf("want deduped tmpfs row, got %#v", got[3])
	}
	if got[1].MountPoint != "/boot" || got[1].Used != "93M" {
		t.Errorf("lsblk row must keep its own usage: %#v", got[1])
	}
}

func TestAppendDfOnlyDisksEmptyLsblk(t *testing.T) {
	// In containers lsblk can come back empty; every df row must survive.
	rows := []dfEntry{
		{Spec: "overlay", FSType: "overlay", Total: "200G", Used: "50G", Usage: 25, Mount: "/"},
	}
	got := appendDfOnlyDisks(nil, rows)
	if len(got) != 1 || got[0].MountPoint != "/" {
		t.Errorf("want overlay row, got %#v", got)
	}
}

// --- ps listing parsing ---

func TestParseProcessRows(t *testing.T) {
	out := `10092     1 111 S 16.5 8.2 [TS_MAIN] /usr/bin/tsmain --config /etc/ts.conf
  40678   900 root S 13.0 0.6 OCRServer python3 /opt/ocr/server.py
bad line
`
	got := parseProcessRows(out)
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	first := got[0]
	if first["pid"] != 10092 || first["ppid"] != 1 {
		t.Errorf("unexpected pids: %#v", first)
	}
	if first["user"] != "111" || first["state"] != "S" {
		t.Errorf("unexpected user/state: %#v", first)
	}
	if first["name"] != "[TS_MAIN]" {
		t.Errorf("unexpected name: %#v", first)
	}
	if first["cmd"] != "/usr/bin/tsmain --config /etc/ts.conf" {
		t.Errorf("unexpected cmd: %#v", first)
	}
	cpu := first["cpu"].(float64)
	if cpu != 16.5 {
		t.Errorf("unexpected cpu: %v", cpu)
	}
}

// --- remote command deadlines ---

// unwrapMonitorCmd splits `timeout N sh -c '<script>'` back into its parts so
// a test can assert both the deadline and that the script survived the
// quoting unchanged.
func unwrapMonitorCmd(t *testing.T, cmd string) (timeoutSec int, script string) {
	t.Helper()
	const prefix = "timeout "
	if !strings.HasPrefix(cmd, prefix) {
		t.Fatalf("command is not wrapped in `timeout`: %.80q", cmd)
	}
	rest := cmd[len(prefix):]
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		t.Fatalf("malformed timeout command: %.80q", cmd)
	}
	sec, err := strconv.Atoi(rest[:sp])
	if err != nil {
		t.Fatalf("timeout duration %q is not a number: %v", rest[:sp], err)
	}
	rest = rest[sp+1:]
	const shPrefix = "sh -c '"
	if !strings.HasPrefix(rest, shPrefix) || !strings.HasSuffix(rest, "'") {
		t.Fatalf("command is not `sh -c`-wrapped: %.80q", cmd)
	}
	quoted := rest[len(shPrefix) : len(rest)-1]
	return sec, strings.ReplaceAll(quoted, `'"'"'`, `'`)
}

// A wedged remote command (D-state process, BMC that never answers) must not
// hang the poll loop: every monitor command runs under a deadline.
func TestMonitorRemoteCommandsHaveTimeout(t *testing.T) {
	hardwareProbe := "exec 2>/dev/null\nipmitool sensor list\nexit 0"

	for _, tc := range []struct {
		name string
		cmd  string
		want int
		raw  string
	}{
		{"performance poll", monitorPerformanceCmd(), monitorPollTimeoutSec, monitorPerformanceScript},
		{"process poll", monitorProcessCmd(), monitorPollTimeoutSec, monitorProcessScript},
		{"hardware probe", monitorRemoteCmd(monitorHardwareTimeoutSec, hardwareProbe), monitorHardwareTimeoutSec, hardwareProbe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, script := unwrapMonitorCmd(t, tc.cmd)
			if got != tc.want {
				t.Errorf("timeout = %ds, want %ds", got, tc.want)
			}
			if script != tc.raw {
				t.Errorf("script changed by quoting:\n got: %q\nwant: %q", script, tc.raw)
			}
		})
	}
}

// --- process list bounds ---

// The process tab re-fetches every 3 s; the remote side must bound both the
// row count and the per-row width so a host with thousands of processes (or
// megabyte Java command lines) cannot generate megabytes per tick.
func TestMonitorProcessCommandBounded(t *testing.T) {
	cmd := monitorProcessCmd()
	if !strings.Contains(cmd, "head -n 2000") {
		t.Errorf("process command has no `head -n 2000` row limit: %.120q", cmd)
	}
	if !strings.Contains(cmd, "cut -c") {
		t.Errorf("process command has no `cut -c` width limit: %.120q", cmd)
	}
}

// startExecOutputServer serves SSH connections whose exec requests are
// answered by onExec (the command line plus the channel). pty-req/shell are
// accepted so a session can attach.
func startExecOutputServer(t *testing.T, onExec func(cmd string, ch ssh.Channel)) string {
	t.Helper()
	signer, err := ssh.ParsePrivateKey([]byte(testHostKeyPEM))
	if err != nil {
		t.Fatalf("parse test host key: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, got []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				_, chans, reqs, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					if newCh.ChannelType() != "session" {
						newCh.Reject(ssh.UnknownChannelType, "only session channels")
						continue
					}
					ch, chReqs, err := newCh.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer ch.Close()
						for req := range chReqs {
							if !req.WantReply {
								continue
							}
							switch req.Type {
							case "pty-req", "shell":
								req.Reply(true, nil)
							case "exec":
								cmd := ""
								if len(req.Payload) >= 4 {
									cmd = string(req.Payload[4:])
								}
								req.Reply(true, nil)
								onExec(cmd, ch)
							default:
								req.Reply(false, nil)
							}
						}
					}()
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// writeExecOK is the default onExec body: report a successful exit and close
// the channel (Session.Wait blocks until the channel is closed).
func writeExecOK(_ string, ch ssh.Channel) {
	_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
	ch.CloseWrite()
	ch.Close()
}

// connectMonitorSession wires a MonitorSession to a live client without
// going through Connect (which is under test elsewhere).
func connectMonitorSession(t *testing.T, addr string) *MonitorSession {
	t.Helper()
	client := dialTestSSHClient(t, addr)
	s := NewMonitorSession("monitor-test-" + t.Name())
	s.mu.Lock()
	s.client = client
	s.mu.Unlock()
	s.setStatus(StatusConnected)
	t.Cleanup(func() { s.Disconnect() })
	return s
}

// A host that answers `ps` with megabytes must not push them all into the
// parser: readSessionOutput stops at its byte cap.
func TestReadSessionOutputTruncatesLongOutput(t *testing.T) {
	const limit = 32 * 1024
	payload := bytes.Repeat([]byte("0123456789abcdef"), 64*1024) // 1 MiB
	addr := startExecOutputServer(t, func(_ string, ch ssh.Channel) {
		_, _ = ch.Write(payload)
		_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
		ch.CloseWrite()
		ch.Close()
	})

	client := dialTestSSHClient(t, addr)
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer session.Close()

	out, err := readSessionOutput(session, "big-output", limit)
	if err != nil {
		t.Fatalf("readSessionOutput: %v", err)
	}
	if int64(len(out)) > limit {
		t.Errorf("read %d bytes, limit is %d — a 1 MiB response reached the parser", len(out), limit)
	}
	if len(out) == 0 {
		t.Error("readSessionOutput returned no output")
	}
}

// --- memory / swap counters ---

// In LXC containers MemAvail (and SwapFree) can exceed the total; the
// unsigned subtraction must clamp at 0 instead of wrapping to a huge value.
func TestMemoryMetricsClampsUnderflow(t *testing.T) {
	usedGB, freeGB, usage := memoryMetrics(1024*1024, 3*1024*1024) // avail > total
	if usedGB != 0 {
		t.Errorf("used = %v GiB, want 0 when avail exceeds total", usedGB)
	}
	if usage != 0 {
		t.Errorf("usage = %v, want 0 when avail exceeds total", usage)
	}
	if freeGB <= 0 {
		t.Errorf("free = %v, want the reported avail", freeGB)
	}

	usedGB, _, usage = memoryMetrics(1000, 400)
	if usedGB != 600.0/1024/1024 {
		t.Errorf("used = %v, want %v", usedGB, 600.0/1024/1024)
	}
	if usage < 59.9 || usage > 60.1 {
		t.Errorf("usage = %v, want ~60", usage)
	}

	// A swap-less host reports SwapFree == SwapTotal == 0 → no underflow.
	usedGB, _, usage = memoryMetrics(0, 0)
	if usedGB != 0 || usage != 0 {
		t.Errorf("empty swap: used=%v usage=%v, want 0/0", usedGB, usage)
	}
}

// --- paused sessions ---

// pushSystemInfo forks a dozen remote processes per run; a paused session
// must not keep paying for them every 30 s.
func TestSystemInfoTickRespectsPaused(t *testing.T) {
	const ticksPerRefresh = 30 / performancePollSec
	if systemInfoTick(ticksPerRefresh, false) != true {
		t.Error("active session should refresh system info on its tick")
	}
	if systemInfoTick(1, false) != false {
		t.Error("non-refresh tick must not fire")
	}
	if systemInfoTick(ticksPerRefresh, true) != false {
		t.Error("paused session must skip the system info refresh")
	}
	if systemInfoTick(1, true) != false {
		t.Error("paused session must not fire on a non-refresh tick either")
	}
}

// --- KillProcess pid validation ---

// `kill -9 -1` signals every process the SSH user may touch; anything below
// pid 2 must be rejected before a session is even opened.
func TestKillProcessRejectsSmallPids(t *testing.T) {
	var mu sync.Mutex
	var cmds []string
	addr := startExecOutputServer(t, func(cmd string, ch ssh.Channel) {
		mu.Lock()
		cmds = append(cmds, cmd)
		mu.Unlock()
		writeExecOK(cmd, ch)
	})
	s := connectMonitorSession(t, addr)

	for _, pid := range []int{-1, 0, 1} {
		if err := s.KillProcess(pid, "KILL"); err == nil {
			t.Errorf("KillProcess(%d, KILL) succeeded, want an error", pid)
		}
	}
	mu.Lock()
	leaked := len(cmds)
	mu.Unlock()
	if leaked != 0 {
		t.Errorf("server received %d kill commands for invalid pids: %q", leaked, cmds)
	}

	if err := s.KillProcess(2, "KILL"); err != nil {
		t.Errorf("KillProcess(2, KILL): %v", err)
	}
	mu.Lock()
	ok := len(cmds) == 1 && strings.Contains(cmds[0], "kill -9 2")
	mu.Unlock()
	if !ok {
		t.Errorf("expected one `kill -9 2`, got %q", cmds)
	}
}

// --- Connect / Disconnect interleaving ---

// The dial takes up to 30 s; the tab can be closed during it. Disconnect must
// win: no stored client left open, no status flipped back to Connected.
func TestMonitorDisconnectDuringDialDoesNotLeakClient(t *testing.T) {
	addr := startExecOutputServer(t, writeExecOK)

	dialing := make(chan struct{})
	release := make(chan struct{})
	rawConns := make(chan net.Conn, 1)

	origDial := monitorDial
	monitorDial = func(a string, cfg *ssh.ClientConfig, proxy *SocksProxy) (*ssh.Client, error) {
		close(dialing)
		<-release
		raw, err := net.Dial("tcp", addr)
		if err != nil {
			return nil, err
		}
		cc, chans, reqs, err := ssh.NewClientConn(raw, a, cfg)
		if err != nil {
			raw.Close()
			return nil, err
		}
		rawConns <- raw
		return ssh.NewClient(cc, chans, reqs), nil
	}
	t.Cleanup(func() { monitorDial = origDial })

	s := NewMonitorSession("monitor-race-" + t.Name())
	t.Cleanup(func() { s.Disconnect() })

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Connect(ConnectionConfig{
			User: "tester", Host: "testhost", Port: 22,
			AuthType: "password", Password: "pw",
		})
	}()

	select {
	case <-dialing:
	case err := <-errCh:
		t.Fatalf("Connect returned before reaching the dial: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Connect never reached the dial")
	}
	if err := s.Disconnect(); err != nil {
		t.Fatalf("Disconnect during dial: %v", err)
	}
	close(release)

	var connectErr error
	select {
	case connectErr = <-errCh:
	case <-time.After(15 * time.Second):
		t.Fatal("Connect never returned after the dial was released")
	}

	if s.Status() == StatusConnected {
		t.Error("status flipped back to Connected after Disconnect")
	}
	if connectErr == nil {
		t.Error("Connect succeeded after the session was closed")
	}
	s.mu.RLock()
	stored := s.client
	s.mu.RUnlock()
	if stored != nil {
		t.Error("Connect stored a client for a session that was already disconnected")
	}

	var raw net.Conn
	select {
	case raw = <-rawConns:
	default:
		t.Fatal("fake dialer never produced a connection")
	}
	defer raw.Close()
	_ = raw.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := raw.Write([]byte{0}); err == nil {
		t.Error("dialled SSH client was never closed — leaked connection")
	}
}
