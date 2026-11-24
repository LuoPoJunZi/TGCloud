//go:build !linux

package entry

import (
	"io"
	"net/netip"
	"regexp"
	"strings"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
)

var httpsDomainLabelOther = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func HTTPSEntrySupported() bool { return false }

func HTTPSEntry(args []string, progress io.Writer) (any, error) {
	return nil, hostutil.ErrLinuxRequired
}

func LANEntrySupported() bool { return false }

func LANEntry(args []string) (any, error) {
	return nil, hostutil.ErrLinuxRequired
}

func RenewDryRun(domain string, progress io.Writer) error {
	return hostutil.ErrLinuxRequired
}

func ValidHTTPSDomain(domain string) bool {
	if len(domain) > 253 || !strings.Contains(domain, ".") {
		return false
	}
	if _, err := netip.ParseAddr(domain); err == nil {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if !httpsDomainLabelOther.MatchString(label) {
			return false
		}
	}
	return true
}
