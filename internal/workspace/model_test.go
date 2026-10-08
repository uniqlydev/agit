package workspace

import "testing"

func TestWorkspaceTransitions(t *testing.T) {
	states := []State{Creating, Ready, Error, Removing, Removed, "unknown"}
	legal := map[[2]State]bool{
		{Creating, Ready}: true, {Creating, Error}: true, {Ready, Error}: true, {Ready, Removing}: true,
		{Error, Ready}: true, {Error, Removing}: true, {Error, Removed}: true, {Removing, Error}: true, {Removing, Removed}: true,
	}
	for _, from := range states {
		for _, to := range states {
			if got := ValidateTransition(from, to) == nil; got != legal[[2]State{from, to}] {
				t.Fatalf("%s -> %s", from, to)
			}
		}
	}
}
