package utils

import (
	"sort"

	ld "gopkg.in/launchdarkly/go-server-sdk.v4"
)

func transformUnorderedDataToOrderedData(allData map[ld.VersionedDataKind]map[string]ld.VersionedData) []StoreCollection {
	colls := make([]StoreCollection, 0, len(allData))
	for kind, itemsMap := range allData {
		items := make([]ld.VersionedData, 0, len(itemsMap))
		if doesDataKindSupportDependencies(kind) {
			addItemsInDependencyOrder(itemsMap, &items)
		} else {
			for _, item := range itemsMap {
				items = append(items, item)
			}
		}
		colls = append(colls, StoreCollection{Kind: kind, Items: items})
	}
	sort.Slice(colls, func(i, j int) bool {
		return dataKindPriority(colls[i].Kind) < dataKindPriority(colls[j].Kind)
	})
	return colls
}

func doesDataKindSupportDependencies(kind ld.VersionedDataKind) bool {
	return kind == ld.Features //nolint:megacheck
}

func addItemsInDependencyOrder(itemsMap map[string]ld.VersionedData, out *[]ld.VersionedData) {
	remainingItems := make(map[string]ld.VersionedData, len(itemsMap))
	for key, item := range itemsMap { // copy the map because we'll be consuming it
		remainingItems[key] = item
	}
	for len(remainingItems) > 0 {
		// pick a random item that hasn't been visited yet
		for _, item := range remainingItems {
			addWithDependenciesFirst(item, remainingItems, out)
			break
		}
	}
}

func addWithDependenciesFirst(startItem ld.VersionedData, remainingItems map[string]ld.VersionedData, out *[]ld.VersionedData) {
	delete(remainingItems, startItem.GetKey()) // we won't need to visit this item again
	for _, prereqKey := range getDependencyKeys(startItem) {
		prereqItem := remainingItems[prereqKey]
		if prereqItem != nil {
			addWithDependenciesFirst(prereqItem, remainingItems, out)
		}
	}
	*out = append(*out, startItem)
}

func getDependencyKeys(item ld.VersionedData) []string {
	var ret []string
	switch i := item.(type) {
	case *ld.FeatureFlag: //nolint:megacheck // allow deprecated usage
		for _, p := range i.Prerequisites {
			ret = append(ret, p.Key)
		}
	}
	return ret
}

// Logic for ensuring that segments are processed before features; if we get any other data types that
// haven't been accounted for here, they'll come after those two in an arbitrary order.
func dataKindPriority(kind ld.VersionedDataKind) int {
	switch kind {
	case ld.Segments: //nolint:megacheck // allow deprecated usage
		return 0
	case ld.Features: //nolint:megacheck // allow deprecated usage
		return 1
	default:
		return len(kind.GetNamespace()) + 2
	}
}
