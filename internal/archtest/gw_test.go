package archtest

import (
	"os/exec"
	"strings"
	"testing"
)

// TestAitsExcludesGateway enforces P1 and P15(4): nothing the aits binary
// builds is a package of aits-gw, so the gateway's dependencies (such as
// SQLite) never reach aits and P3 and P14 still hold for every aits user.
func TestAitsExcludesGateway(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/aits")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	deps := strings.Fields(string(out))
	if len(deps) == 0 {
		t.Fatal("go list -deps ./cmd/aits listed nothing")
	}
	for _, pkg := range deps {
		if gatewayPackage(pkg) {
			t.Errorf("aits depends on %s, a package of aits-gw (P1)", pkg)
		}
	}
}

// gatewayPackage reports whether pkg is internal/gw or a package under it.
func gatewayPackage(pkg string) bool {
	return pkg == module+"gw" || strings.HasPrefix(pkg, module+"gw/")
}

func TestGatewayPackage(t *testing.T) {
	for pkg, want := range map[string]bool{
		module + "gw":       true,
		module + "gw/route": true,
		module + "gwx":      false,
		module + "shell":    false,
		"example.com/gw":    false,
	} {
		if got := gatewayPackage(pkg); got != want {
			t.Errorf("gatewayPackage(%q) = %t, want %t", pkg, got, want)
		}
	}
}
