package cmd

import (
	"testing"
)

func TestStatuslineHasNoProviderFlags(t *testing.T) {
	for _, name := range []string{"provider", "show-provider"} {
		if statuslineCmd.Flags().Lookup(name) != nil {
			t.Errorf("statusline 仍暴露 --%s", name)
		}
	}
}
