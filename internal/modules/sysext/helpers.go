package sysext

import "slices"

func activeFirst(extensions []Extension) []Extension {
	result := slices.Clone(extensions)
	slices.SortStableFunc(result, func(a, b Extension) int {
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

func enabledCount(extensions []Extension) int {
	count := 0
	for _, extension := range extensions {
		if extension.Enabled {
			count++
		}
	}
	return count
}

func mergedCount(extensions []Extension) int {
	count := 0
	for _, extension := range extensions {
		if extension.Merged {
			count++
		}
	}
	return count
}

// updateCount is the aggregate pending-component-update total across every
// extension — the number the Summary card's "Updates" mini-row shows. It is
// naturally zero on a host without updex: Check() is updex-only, so
// Extension.Updates is empty whenever UpdexAvailable is false, and no extra
// capability flag is needed to suppress the update surfaces.
func updateCount(extensions []Extension) int {
	count := 0
	for _, extension := range extensions {
		count += len(extension.Updates)
	}
	return count
}

// pendingUpdates flattens every extension's Updates slice into the flat row
// list the "Available updates" table renders. AvailableUpdate.Feature
// carries the owning extension's name, so the flattened rows stay
// self-describing without a parallel key.
func pendingUpdates(extensions []Extension) []AvailableUpdate {
	updates := []AvailableUpdate{}
	for _, extension := range extensions {
		updates = append(updates, extension.Updates...)
	}
	return updates
}
