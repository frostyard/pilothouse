package sysext

import "slices"

func activeFirst(features []Feature) []Feature {
	result := slices.Clone(features)
	slices.SortStableFunc(result, func(a, b Feature) int {
		if a.Merged != b.Merged {
			if a.Merged {
				return -1
			}
			return 1
		}
		if a.Enabled != b.Enabled {
			if a.Enabled {
				return -1
			}
			return 1
		}
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return result
}

func enabledCount(features []Feature) int {
	count := 0
	for _, feature := range features {
		if feature.Enabled {
			count++
		}
	}
	return count
}

func mergedCount(features []Feature) int {
	count := 0
	for _, feature := range features {
		if feature.Merged {
			count++
		}
	}
	return count
}
