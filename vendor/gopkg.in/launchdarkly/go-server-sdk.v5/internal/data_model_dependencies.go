package internal

import (
	"sort"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

type kindAndKey struct {
	kind interfaces.StoreDataKind
	key  string
}

// This set type is implemented as a map, but the values do not matter, just the keys.
type kindAndKeySet map[kindAndKey]bool

func (s kindAndKeySet) add(value kindAndKey) {
	s[value] = true
}

func (s kindAndKeySet) contains(value kindAndKey) bool {
	_, ok := s[value]
	return ok
}

func computeDependenciesFrom(kind interfaces.StoreDataKind, fromItem interfaces.StoreItemDescriptor) kindAndKeySet {
	if kind == interfaces.DataKindFeatures() {
		if flag, ok := fromItem.Item.(*ldmodel.FeatureFlag); ok {
			var ret kindAndKeySet
			if len(flag.Prerequisites) > 0 {
				ret = make(kindAndKeySet, len(flag.Prerequisites))
				for _, p := range flag.Prerequisites {
					ret.add(kindAndKey{interfaces.DataKindFeatures(), p.Key})
				}
			}
			for _, r := range flag.Rules {
				for _, c := range r.Clauses {
					if c.Op == ldmodel.OperatorSegmentMatch {
						for _, v := range c.Values {
							if v.Type() == ldvalue.StringType {
								if ret == nil {
									ret = make(kindAndKeySet)
								}
								ret.add(kindAndKey{interfaces.DataKindSegments(), v.StringValue()})
							}
						}
					}
				}
			}
			return ret
		}
	}
	return nil
}

func sortCollectionsForDataStoreInit(allData []interfaces.StoreCollection) []interfaces.StoreCollection {
	colls := make([]interfaces.StoreCollection, 0, len(allData))
	for _, coll := range allData {
		if doesDataKindSupportDependencies(coll.Kind) {
			itemsOut := make([]interfaces.StoreKeyedItemDescriptor, 0, len(coll.Items))
			addItemsInDependencyOrder(coll.Kind, coll.Items, &itemsOut)
			colls = append(colls, interfaces.StoreCollection{Kind: coll.Kind, Items: itemsOut})
		} else {
			colls = append(colls, coll)
		}
	}
	sort.Slice(colls, func(i, j int) bool {
		return dataKindPriority(colls[i].Kind) < dataKindPriority(colls[j].Kind)
	})
	return colls
}

func doesDataKindSupportDependencies(kind interfaces.StoreDataKind) bool {
	return kind == interfaces.DataKindFeatures() //nolint:megacheck
}

func addItemsInDependencyOrder(
	kind interfaces.StoreDataKind,
	itemsIn []interfaces.StoreKeyedItemDescriptor,
	out *[]interfaces.StoreKeyedItemDescriptor,
) {
	remainingItems := make(map[string]interfaces.StoreItemDescriptor, len(itemsIn))
	for _, item := range itemsIn {
		remainingItems[item.Key] = item.Item
	}
	for len(remainingItems) > 0 {
		// pick a random item that hasn't been visited yet
		for firstKey := range remainingItems {
			addWithDependenciesFirst(kind, firstKey, remainingItems, out)
			break
		}
	}
}

func addWithDependenciesFirst(
	kind interfaces.StoreDataKind,
	startingKey string,
	remainingItems map[string]interfaces.StoreItemDescriptor,
	out *[]interfaces.StoreKeyedItemDescriptor,
) {
	startItem := remainingItems[startingKey]
	delete(remainingItems, startingKey) // we won't need to visit this item again
	for dep := range computeDependenciesFrom(kind, startItem) {
		if dep.kind == kind {
			if _, ok := remainingItems[dep.key]; ok {
				addWithDependenciesFirst(kind, dep.key, remainingItems, out)
			}
		}
	}
	*out = append(*out, interfaces.StoreKeyedItemDescriptor{Key: startingKey, Item: startItem})
}

// Logic for ensuring that segments are processed before features; if we get any other data types that
// haven't been accounted for here, they'll come after those two in an arbitrary order.
func dataKindPriority(kind interfaces.StoreDataKind) int {
	switch kind.GetName() {
	case "segments":
		return 0
	case "features":
		return 1
	default:
		return len(kind.GetName()) + 2
	}
}

// Maintains a bidirectional dependency graph that can be updated whenever an item has changed.
type dependencyTracker struct {
	dependenciesFrom map[kindAndKey]kindAndKeySet
	dependenciesTo   map[kindAndKey]kindAndKeySet
}

func newDependencyTracker() *dependencyTracker {
	return &dependencyTracker{make(map[kindAndKey]kindAndKeySet), make(map[kindAndKey]kindAndKeySet)}
}

// Updates the dependency graph when an item has changed.
func (d *dependencyTracker) updateDependenciesFrom(
	kind interfaces.StoreDataKind,
	fromKey string,
	fromItem interfaces.StoreItemDescriptor,
) {
	fromWhat := kindAndKey{kind, fromKey}
	updatedDependencies := computeDependenciesFrom(kind, fromItem)

	oldDependencySet := d.dependenciesFrom[fromWhat]
	for oldDep := range oldDependencySet {
		depsToThisOldDep := d.dependenciesTo[oldDep]
		if depsToThisOldDep != nil {
			delete(depsToThisOldDep, fromWhat)
		}
	}

	d.dependenciesFrom[fromWhat] = updatedDependencies
	for newDep := range updatedDependencies {
		depsToThisNewDep := d.dependenciesTo[newDep]
		if depsToThisNewDep == nil {
			depsToThisNewDep = make(kindAndKeySet)
			d.dependenciesTo[newDep] = depsToThisNewDep
		}
		depsToThisNewDep.add(fromWhat)
	}
}

func (d *dependencyTracker) reset() {
	d.dependenciesFrom = make(map[kindAndKey]kindAndKeySet)
	d.dependenciesTo = make(map[kindAndKey]kindAndKeySet)
}

// Populates the given set with the union of the initial item and all items that directly or indirectly
// depend on it (based on the current state of the dependency graph).
func (d *dependencyTracker) addAffectedItems(itemsOut kindAndKeySet, initialModifiedItem kindAndKey) {
	if !itemsOut.contains(initialModifiedItem) {
		itemsOut.add(initialModifiedItem)
		affectedItems := d.dependenciesTo[initialModifiedItem]
		for affectedItem := range affectedItems {
			d.addAffectedItems(itemsOut, affectedItem)
		}
	}
}
