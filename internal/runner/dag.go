package runner

import (
	"fmt"
	"sort"

	"github.com/akhanatoon/quench/internal/config"
)

// StatusDepFailed is recorded in statusByName when a check is skipped because
// an upstream dependency failed, allowing transitive skip propagation across waves.
const StatusDepFailed = "dependency_failed"

// BuildWaves groups check indices into topological execution waves using Kahn's algorithm.
// Wave 0 contains checks with no dependencies (or whose dependencies are not in checks).
// Wave k contains checks whose dependencies are all satisfied in waves < k.
// Indices within each wave are sorted in ascending order for deterministic execution.
// Returns an error if a dependency cycle exists.
func BuildWaves(checks []config.CheckConfig) ([][]int, error) {
	n := len(checks)
	if n == 0 {
		return nil, nil
	}

	idxByName := make(map[string]int, n)
	for i, ch := range checks {
		idxByName[ch.Name] = i
	}

	inDegree := make([]int, n)
	dependents := make([][]int, n)

	for i, ch := range checks {
		if len(ch.DependsOn) == 0 {
			continue
		}
		seenDep := make(map[int]struct{}, len(ch.DependsOn))
		for _, depName := range ch.DependsOn {
			depIdx, ok := idxByName[depName]
			if !ok {
				// Dependency is outside the selected subset (e.g. filtered via --only).
				continue
			}
			if _, dup := seenDep[depIdx]; dup {
				continue
			}
			seenDep[depIdx] = struct{}{}
			if depIdx == i {
				// Self-dependency forms a 1-node cycle.
				inDegree[i]++
				continue
			}
			dependents[depIdx] = append(dependents[depIdx], i)
			inDegree[i]++
		}
	}

	var current []int
	for i := 0; i < n; i++ {
		if inDegree[i] == 0 {
			current = append(current, i)
		}
	}

	var waves [][]int
	visited := 0

	for len(current) > 0 {
		sort.Ints(current)
		waves = append(waves, current)
		visited += len(current)

		var next []int
		for _, u := range current {
			for _, v := range dependents[u] {
				inDegree[v]--
				if inDegree[v] == 0 {
					next = append(next, v)
				}
			}
		}
		current = next
	}

	if visited != n {
		return nil, fmt.Errorf("dependency cycle detected among checks")
	}

	return waves, nil
}

// FailedDependency returns the first dependency of ch whose recorded status in
// statusByName is StatusFailed or StatusDepFailed (skipped due to a failed dependency).
func FailedDependency(ch config.CheckConfig, statusByName map[string]string) (string, bool) {
	for _, dep := range ch.DependsOn {
		switch statusByName[dep] {
		case StatusFailed, StatusDepFailed:
			return dep, true
		}
	}
	return "", false
}
