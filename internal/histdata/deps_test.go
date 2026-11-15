package histdata

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenDeps are the packages that can place orders or read secrets.
var forbiddenDeps = []string{
	"github.com/James-Chapman/LLMTradingBot/internal/app",
	"github.com/James-Chapman/LLMTradingBot/internal/secret",
	"github.com/James-Chapman/LLMTradingBot/internal/server",
	"github.com/James-Chapman/LLMTradingBot/internal/kraken",
	"github.com/James-Chapman/LLMTradingBot/internal/storage",
}

// TestImportGraph enforces that historical data code cannot reach live trading.
func TestImportGraph(t *testing.T) {
	t.Run("GIVEN the histdata package and its tests WHEN their dependencies are listed THEN no live-execution, secret or bot-database package is linked", func(t *testing.T) {
		goTool := "go"
		out, err := exec.Command(goTool, "list", "-deps", "-test", "-f", "{{.ImportPath}}", ".").CombinedOutput()
		if err != nil {
			t.Fatalf("go list: %v\n%s", err, out)
		}
		deps := strings.Fields(string(out))
		if len(deps) < 5 {
			t.Fatalf("suspiciously short dependency list: %v", deps)
		}
		for _, dep := range deps {
			for _, bad := range forbiddenDeps {
				if dep == bad || strings.HasPrefix(dep, bad+"/") {
					t.Errorf("histdata depends on %s", dep)
				}
			}
		}
	})
}
